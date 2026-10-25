package mcp

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

// Long enough for readability, which the url case uses when no framework is detected.
var draftDocPage = `<html><head><title>Guide</title></head><body><nav><a href="/x">x</a></nav>` +
	`<main id="c"><h1>Guide</h1><p>DRAFT FIXTURE TEXT. ` +
	strings.Repeat("This guide explains how the demo tool is installed, configured and run from the command line. ", 8) +
	`</p><pre><code>demo --help</code></pre></main></body></html>`

func newDraftSiteServer(t *testing.T) (*Server, *atomic.Int32) {
	t.Helper()
	var robots atomic.Int32
	s, _, _ := newCrawlJobServerWith(t, "docs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			robots.Add(1)
			http.NotFound(w, r)
		case "/docs/index.html", "/docs/guide.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, draftDocPage)
		default:
			http.NotFound(w, r)
		}
	}))
	return s, &robots
}

func TestDraftSite_NewURL(t *testing.T) {
	s, _ := newDraftSiteServer(t)
	docs, _ := s.site("docs")
	seed := strings.Replace(docs.StartURLs[0], "index.html", "guide.html", 1)

	_, got := callJSON(t, s.handleDraftSite, map[string]any{"url": seed, "site_key": "guide"})
	require.NotNil(t, got)
	assert.Equal(t, "guide", got["site_key"])
	assert.Equal(t, false, got["exists"])
	assert.Equal(t, "disabled", got["config_writes"])
	entry := got["entry"].(map[string]any)
	assert.Equal(t, []any{seed}, entry["start_urls"])
	assert.Contains(t, got["entry_yaml"], "guide:")
	preview := got["preview"].(map[string]any)
	assert.Contains(t, preview["markdown"], "DRAFT FIXTURE TEXT")
}

func TestDraftSite_ExistingKeyIsFlagged(t *testing.T) {
	s, _ := newDraftSiteServer(t)
	docs, _ := s.site("docs")
	_, got := callJSON(t, s.handleDraftSite, map[string]any{"url": docs.StartURLs[0], "site_key": "docs"})
	require.NotNil(t, got)
	assert.Equal(t, true, got["exists"])
}

func TestDraftSite_ConfiguredSiteWithOverrides(t *testing.T) {
	s, robots := newDraftSiteServer(t)
	_, got := callJSON(t, s.handleDraftSite, map[string]any{
		"site_key":  "docs",
		"overrides": map[string]any{"content_selector": "main", "max_depth": float64(2)},
	})
	require.NotNil(t, got)
	entry := got["entry"].(map[string]any)
	assert.Equal(t, "main", entry["content_selector"])
	assert.EqualValues(t, 2, entry["max_depth"])
	assert.Equal(t, true, got["exists"])
	assert.Zero(t, robots.Load(), "a configured site is previewed without discovery")
	preview := got["preview"].(map[string]any)
	assert.Contains(t, preview["markdown"], "DRAFT FIXTURE TEXT")
}

func TestDraftSite_Errors(t *testing.T) {
	s, _ := newDraftSiteServer(t)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"neither url nor site_key", map[string]any{}, "needs url"},
		{"unknown site_key", map[string]any{"site_key": "nope"}, "site 'nope' not found"},
		{"overrides not an object", map[string]any{"site_key": "docs", "overrides": "x"}, `argument "overrides" must be object`},
		{"unknown override field", map[string]any{"site_key": "docs", "overrides": map[string]any{"bogus": true}}, `"bogus"`},
		{"refused override field", map[string]any{"site_key": "docs", "overrides": map[string]any{"enable_jsonl_output": false}}, `"enable_jsonl_output"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, got := callJSON(t, s.handleDraftSite, tc.args)
			require.Nil(t, got)
			require.True(t, res.IsError)
			assert.Contains(t, res.Content[0].(mcpgo.TextContent).Text, tc.want)
		})
	}
}

func TestDescribeServer_ReportsConfigWrites(t *testing.T) {
	s, _ := newDraftSiteServer(t)
	_, got := callJSON(t, s.handleDescribeServer, map[string]any{})
	assert.Equal(t, "disabled", got["config_writes"])
	s.cfg.AllowConfigWrites = true
	_, got = callJSON(t, s.handleDescribeServer, map[string]any{})
	assert.Equal(t, "enabled", got["config_writes"])
}

func TestDescribeServer_NoSitesPointsAtDraftSite(t *testing.T) {
	s, _ := newDraftSiteServer(t)
	s.cfg.AppConfig.Sites = map[string]*config.SiteConfig{}
	s.cfg.AllowConfigWrites = true
	_, got := callJSON(t, s.handleDescribeServer, map[string]any{})
	assert.Contains(t, got["next_actions"], "draft_site")
}
