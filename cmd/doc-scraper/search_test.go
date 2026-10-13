package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func writeSearchFixture(t *testing.T, extraSites ...string) (cfgPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	cfgPath = filepath.Join(tmpDir, "config.yaml")
	// Single-quoted YAML plus forward slashes: a double-quoted Windows path
	// like D:\a\... is parsed as YAML escape sequences and breaks loading.
	content := `
state_dir: '` + filepath.ToSlash(stateDir) + `'
output_base_dir: '` + filepath.ToSlash(filepath.Join(tmpDir, "out")) + `'
sites:
  demo:
    start_urls: ["https://demo.example.com/docs/"]
    allowed_domain: "demo.example.com"
    content_selector: "main"
` + strings.Join(extraSites, "")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	idx, err := index.OpenAt(stateDir, 5, log)
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()
	md := "# Retries\n\n## Backoff\n\nExponential backoff doubles the ocelot delay each attempt. " + strings.Repeat("filler ", 40)
	require.NoError(t, idx.ReplaceChunks(context.Background(), "demo", "https://demo.example.com/docs/retries", "Retries", "h1", chunk.Split(md)))
	return cfgPath
}

func TestDoSearch_HumanOutput(t *testing.T) {
	cfgPath := writeSearchFixture(t)
	var stdout, stderr bytes.Buffer
	code := doSearch(cfgPath, "ocelot backoff", "", 10, false, &stdout, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	out := stdout.String()
	assert.Contains(t, out, "https://demo.example.com/docs/retries#backoff", "result links to the section anchor")
	assert.Contains(t, out, "(demo)")
	assert.Contains(t, out, "[ocelot]", "snippet marks match terms")
}

func TestDoSearch_JSONOutput(t *testing.T) {
	cfgPath := writeSearchFixture(t)
	var stdout, stderr bytes.Buffer
	code := doSearch(cfgPath, "ocelot", "demo", 5, true, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	var results []index.SearchResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &results))
	require.Len(t, results, 1)
	assert.Equal(t, "backoff", results[0].Anchor)
}

func TestDoSearch_NoMatchesAndErrors(t *testing.T) {
	cfgPath := writeSearchFixture(t)
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, doSearch(cfgPath, "wombatless", "", 5, false, &stdout, &stderr))
	assert.Contains(t, stdout.String(), "No matches")

	stderr.Reset()
	assert.Equal(t, 1, doSearch(cfgPath, "x", "nope", 5, false, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "not found in config")

	stderr.Reset()
	assert.Equal(t, 1, doSearch(filepath.Join(t.TempDir(), "missing.yaml"), "x", "", 5, false, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "read config")
}

func TestParseSearchArgs(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		query string
		limit int
		json  bool
	}{
		{"flags first", []string{"-limit", "3", "clap", "derive"}, "clap derive", 3, false},
		{"flags after query", []string{"clap", "-limit", "1"}, "clap", 1, false},
		{"flags between words", []string{"clap", "-json", "derive", "-limit=2"}, "clap derive", 2, true},
		{"double dash ends flags", []string{"-limit", "4", "--", "-limit", "x"}, "-limit x", 4, false},
		{"flag after double dash is text", []string{"clap", "--", "-json"}, "clap -json", 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := parseSearchArgs(tc.args, io.Discard)
			require.NoError(t, err)
			assert.Equal(t, tc.query, a.query)
			assert.Equal(t, tc.limit, a.limit)
			assert.Equal(t, tc.json, a.jsonOut)
		})
	}

	_, err := parseSearchArgs([]string{"-limit", "2"}, io.Discard)
	require.Error(t, err)
	_, err = parseSearchArgs([]string{"x", "-bogus"}, io.Discard)
	require.Error(t, err)
}

func TestDoSearch_RejectsNonPositiveLimit(t *testing.T) {
	cfg := writeSearchFixture(t)
	for _, l := range []int{0, -1} {
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 1, doSearch(cfg, "auth", "", l, false, &stdout, &stderr))
		assert.Contains(t, stderr.String(), "-limit must be a positive integer")
	}
}

func TestDoSearch_ExplainsEmptyResults(t *testing.T) {
	cfgPath := writeSearchFixture(t,
		"  off:\n    start_urls: [\"https://off.example.com/\"]\n    allowed_domain: off.example.com\n    content_selector: main\n    enable_jsonl_output: false\n",
		"  fresh:\n    start_urls: [\"https://fresh.example.com/\"]\n    allowed_domain: fresh.example.com\n    content_selector: main\n",
	)
	search := func(siteKey string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		require.Equal(t, 0, doSearch(cfgPath, "wombatless", siteKey, 5, false, &stdout, &stderr), stderr.String())
		assert.NotContains(t, stdout.String(), "Crawl a site first")
		return stdout.String()
	}

	out := search("off")
	assert.Contains(t, out, "enable_jsonl_output is false")
	assert.Contains(t, out, "doc-scraper crawl -site off")

	out = search("fresh")
	assert.Contains(t, out, "has no indexed pages")
	assert.Contains(t, out, "doc-scraper crawl -site fresh")

	out = search("demo")
	assert.Contains(t, out, "No matches")
	assert.NotContains(t, out, "enable_jsonl_output")

	out = search("")
	assert.Contains(t, out, "No matches")
	assert.Contains(t, out, "enable_jsonl_output is false: off")
	assert.Contains(t, out, "not crawled yet: fresh")
}

func TestDoSearch_NothingIndexedSaysSo(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	content := "state_dir: '" + filepath.ToSlash(filepath.Join(tmpDir, "state")) + "'\n" +
		"sites:\n  fresh:\n    start_urls: [\"https://fresh.example.com/\"]\n    allowed_domain: fresh.example.com\n    content_selector: main\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))

	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, doSearch(cfgPath, "anything", "", 5, false, &stdout, &stderr), stderr.String())
	assert.Contains(t, stdout.String(), "No site is indexed yet")
	assert.Contains(t, stdout.String(), "doc-scraper crawl")
}

// Chunks indexed before a site opted out stay searchable, so an empty result
// there is a plain miss, not "not indexed".
func TestDoSearch_OptedOutSiteWithOldChunksIsSearched(t *testing.T) {
	cfgPath := writeSearchFixture(t)
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, append(data, []byte("    enable_jsonl_output: false\n")...), 0o644))

	for _, siteKey := range []string{"", "demo"} {
		var stdout, stderr bytes.Buffer
		require.Equal(t, 0, doSearch(cfgPath, "wombatless", siteKey, 5, false, &stdout, &stderr), stderr.String())
		assert.Contains(t, stdout.String(), "No matches")
		assert.NotContains(t, stdout.String(), "enable_jsonl_output")
	}
}
