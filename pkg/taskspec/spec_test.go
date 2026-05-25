package taskspec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAndValidate_CrawlSingleSite(t *testing.T) {
	in := `{"command": "crawl", "site": "rust_cli_small"}`
	spec, err := Parse(strings.NewReader(in))
	require.NoError(t, err)
	require.NoError(t, spec.Validate())

	assert.Equal(t, CommandCrawl, spec.Command)
	assert.Equal(t, "config.yaml", spec.Config, "default config path")
	assert.Equal(t, "info", spec.Loglevel, "default loglevel")
	assert.Equal(t, []string{"rust_cli_small"}, spec.SiteKeys())
}

func TestParseAndValidate_CrawlMultipleSites(t *testing.T) {
	in := `{
		"command": "crawl",
		"sites": ["alpha", "beta"],
		"config": "/etc/doc-scraper.yaml",
		"loglevel": "debug",
		"json_logs": true,
		"incremental": true
	}`
	spec, err := Parse(strings.NewReader(in))
	require.NoError(t, err)
	require.NoError(t, spec.Validate())

	assert.Equal(t, []string{"alpha", "beta"}, spec.SiteKeys())
	assert.Equal(t, "/etc/doc-scraper.yaml", spec.Config)
	assert.Equal(t, "debug", spec.Loglevel)
	assert.True(t, spec.JSONLogs)
	assert.True(t, spec.Incremental)
}

func TestParseAndValidate_CrawlAllSites(t *testing.T) {
	in := `{"command": "crawl", "all_sites": true}`
	spec, err := Parse(strings.NewReader(in))
	require.NoError(t, err)
	require.NoError(t, spec.Validate())

	assert.Nil(t, spec.SiteKeys(), "all_sites is signaled by nil slice for callers to expand")
}

func TestParseAndValidate_WatchHappyPath(t *testing.T) {
	in := `{"command": "watch", "all_sites": true, "interval": "12h"}`
	spec, err := Parse(strings.NewReader(in))
	require.NoError(t, err)
	require.NoError(t, spec.Validate())

	assert.Equal(t, "12h", spec.Interval)
}

func TestValidate_WatchDefaultInterval(t *testing.T) {
	in := `{"command": "watch", "site": "x"}`
	spec, err := Parse(strings.NewReader(in))
	require.NoError(t, err)
	require.NoError(t, spec.Validate())

	assert.Equal(t, "24h", spec.Interval, "watch default interval")
}

func TestValidate_MissingCommand(t *testing.T) {
	spec, err := Parse(strings.NewReader(`{"site": "x"}`))
	require.NoError(t, err)
	err = spec.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}

func TestValidate_UnknownCommand(t *testing.T) {
	spec, err := Parse(strings.NewReader(`{"command": "describe", "site": "x"}`))
	require.NoError(t, err)
	err = spec.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown command")
}

func TestValidate_NoSiteSelection(t *testing.T) {
	spec, err := Parse(strings.NewReader(`{"command": "crawl"}`))
	require.NoError(t, err)
	err = spec.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one of site, sites, or all_sites is required")
}

func TestValidate_MultipleSiteSelections(t *testing.T) {
	cases := []string{
		`{"command": "crawl", "site": "a", "sites": ["b"]}`,
		`{"command": "crawl", "site": "a", "all_sites": true}`,
		`{"command": "crawl", "sites": ["a"], "all_sites": true}`,
		`{"command": "crawl", "site": "a", "sites": ["b"], "all_sites": true}`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			spec, err := Parse(strings.NewReader(in))
			require.NoError(t, err)
			err = spec.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "mutually exclusive")
		})
	}
}

func TestValidate_IncrementalAndFullConflict(t *testing.T) {
	spec, err := Parse(strings.NewReader(`{"command": "crawl", "site": "x", "incremental": true, "full": true}`))
	require.NoError(t, err)
	err = spec.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incremental and full are mutually exclusive")
}

func TestValidate_WatchRejectsCrawlOnlyKnobs(t *testing.T) {
	cases := []string{
		`{"command": "watch", "site": "x", "resume": true}`,
		`{"command": "watch", "site": "x", "incremental": true}`,
		`{"command": "watch", "site": "x", "full": true}`,
		`{"command": "watch", "site": "x", "pprof": "localhost:6060"}`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			spec, err := Parse(strings.NewReader(in))
			require.NoError(t, err)
			err = spec.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not valid for watch")
		})
	}
}

func TestValidate_WatchInvalidInterval(t *testing.T) {
	spec, err := Parse(strings.NewReader(`{"command": "watch", "site": "x", "interval": "soon"}`))
	require.NoError(t, err)
	err = spec.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid interval")
}

func TestValidate_EmptyStringInSitesArray(t *testing.T) {
	spec, err := Parse(strings.NewReader(`{"command": "crawl", "sites": ["alpha", "  "]}`))
	require.NoError(t, err)
	err = spec.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sites[1] is empty")
}

func TestParse_RejectsUnknownField(t *testing.T) {
	_, err := Parse(strings.NewReader(`{"command": "crawl", "site": "x", "sit": "typo"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sit")
}

func TestParse_RejectsTrailingJunk(t *testing.T) {
	in := `{"command": "crawl", "site": "x"} {"another": true}`
	_, err := Parse(strings.NewReader(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extra JSON content")
}

func TestParse_RejectsEmptyInput(t *testing.T) {
	_, err := Parse(strings.NewReader(""))
	require.Error(t, err)
}

func TestParse_RejectsMalformedJSON(t *testing.T) {
	_, err := Parse(strings.NewReader(`{"command": "crawl"`))
	require.Error(t, err)
}
