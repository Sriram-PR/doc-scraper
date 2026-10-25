package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

type saveFixture struct {
	s       *Server
	cfgPath string
	host    string
	base    string
	newHits *atomic.Int32
	block   chan struct{}
}

// newSaveFixture serves /docs/index.html and /docs/new.html and writes a config
// file holding the "docs" site. While block is open, /docs/index.html hangs.
func newSaveFixture(t *testing.T, allow bool) *saveFixture {
	t.Helper()
	f := &saveFixture{newHits: &atomic.Int32{}}
	s, siteCfg, _ := newCrawlJobServerWith(t, "docs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/docs/index.html":
			if f.block != nil {
				select {
				case <-f.block:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(w, `<html><head><title>Index</title></head><body><main id="c"><p>INDEX PAGE</p></main></body></html>`)
		case "/docs/new.html":
			f.newHits.Add(1)
			_, _ = io.WriteString(w, `<html><head><title>New</title></head><body><main id="c"><p>ZEBRAFISH QUANTUM newly saved page</p></main></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	attachIndex(t, s)
	f.s = s
	f.host = siteCfg.AllowedDomain
	f.base = strings.TrimSuffix(siteCfg.StartURLs[0], "/docs/index.html")
	f.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, config.UpsertSite(f.cfgPath, "docs", siteCfg, false))
	s.cfg.ConfigPath = f.cfgPath
	s.cfg.AllowConfigWrites = allow
	return f
}

func (f *saveFixture) entry(page string) map[string]any {
	return map[string]any{
		"start_urls":          []any{f.base + page},
		"allowed_domain":      f.host,
		"allowed_path_prefix": "/docs",
		"content_selector":    "#c",
	}
}

func (f *saveFixture) fileBytes(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	return string(b)
}

func TestSaveSite_DisabledWithoutFlag(t *testing.T) {
	f := newSaveFixture(t, false)
	before := f.fileBytes(t)
	res, _ := callJSON(t, f.s.handleSaveSite, map[string]any{"site_key": "newdocs", "entry": f.entry("/docs/new.html")})
	assert.Contains(t, errText(t, res), "-allow-config-writes")
	assert.Equal(t, before, f.fileBytes(t))
}

func TestSaveSite_NewSiteCrawlsAndSearchesInTheSameSession(t *testing.T) {
	f := newSaveFixture(t, true)
	_, got := callJSON(t, f.s.handleSaveSite, map[string]any{"site_key": "newdocs", "entry": f.entry("/docs/new.html")})
	require.NotNil(t, got)
	assert.Equal(t, true, got["written"])
	assert.Equal(t, false, got["replaced"])

	loaded, _, err := config.Load(f.cfgPath)
	require.NoError(t, err)
	require.Contains(t, loaded.Sites, "newdocs")
	require.Contains(t, loaded.Sites, "docs")

	_, started := callJSON(t, f.s.handleCrawlSite, map[string]any{"site_key": "newdocs"})
	require.Equal(t, "started", started["status"])
	id := started["job_id"].(string)
	require.Eventually(t, func() bool { return isTerminalStatus(f.s.jobManager.GetJob(id).Status) }, time.Minute, 10*time.Millisecond)
	require.Equal(t, JobStatusCompleted, f.s.jobManager.GetJob(id).Status, f.s.jobManager.GetJob(id).ErrorMessage)

	_, found := callJSON(t, f.s.handleSearchDocs, map[string]any{"query": "zebrafish quantum", "site_key": "newdocs"})
	require.NotNil(t, found)
	assert.NotEmpty(t, found["results"])
}

func TestSaveSite_ReplaceRules(t *testing.T) {
	f := newSaveFixture(t, true)
	before := f.fileBytes(t)

	res, _ := callJSON(t, f.s.handleSaveSite, map[string]any{"site_key": "docs", "entry": f.entry("/docs/new.html")})
	assert.Contains(t, errText(t, res), "replace: true")
	res, _ = callJSON(t, f.s.handleSaveSite, map[string]any{"site_key": "missing", "entry": f.entry("/docs/new.html"), "replace": true})
	assert.Contains(t, errText(t, res), "doesn't exist")
	assert.Equal(t, before, f.fileBytes(t))

	changed := f.entry("/docs/new.html")
	changed["content_selector"] = "main"
	_, got := callJSON(t, f.s.handleSaveSite, map[string]any{"site_key": "docs", "entry": changed, "replace": true})
	require.NotNil(t, got)
	assert.Equal(t, true, got["replaced"])
	docs, _ := f.s.site("docs")
	assert.Equal(t, "main", docs.ContentSelector)
	loaded, _, err := config.Load(f.cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "main", loaded.Sites["docs"].ContentSelector)
}

func TestSaveSite_InvalidEntryLeavesFileUntouched(t *testing.T) {
	f := newSaveFixture(t, true)
	before := f.fileBytes(t)
	cases := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"missing content_selector", map[string]any{"start_urls": []any{f.base + "/docs/new.html"}, "allowed_domain": f.host}, "content_selector"},
		{"bad regex", map[string]any{"start_urls": []any{f.base + "/docs/new.html"}, "allowed_domain": f.host, "content_selector": "#c", "disallowed_path_patterns": []any{"("}}, "invalid site entry"},
		{"refused field", map[string]any{"start_urls": []any{f.base + "/docs/new.html"}, "allowed_domain": f.host, "content_selector": "#c", "enable_jsonl_output": false}, `"enable_jsonl_output"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := callJSON(t, f.s.handleSaveSite, map[string]any{"site_key": "newdocs", "entry": tc.entry})
			assert.Contains(t, errText(t, res), tc.want)
			assert.Equal(t, before, f.fileBytes(t))
			_, ok := f.s.site("newdocs")
			assert.False(t, ok)
		})
	}
}

func TestSaveSite_RefusedWhileCrawlRuns(t *testing.T) {
	f := newSaveFixture(t, true)
	f.block = make(chan struct{})
	_, started := callJSON(t, f.s.handleCrawlSite, map[string]any{"site_key": "docs"})
	require.Equal(t, "started", started["status"])
	t.Cleanup(func() {
		close(f.block)
		waitForSiteReleased(t, f.s, "docs")
	})

	before := f.fileBytes(t)
	res, _ := callJSON(t, f.s.handleSaveSite, map[string]any{"site_key": "docs", "entry": f.entry("/docs/new.html"), "replace": true})
	assert.Contains(t, errText(t, res), "crawl running for docs")
	assert.Equal(t, before, f.fileBytes(t))
}

func TestCrawlSite_UsesTheEntrySavedWhileItWaited(t *testing.T) {
	f := newSaveFixture(t, true)
	release, err := f.s.jobManager.ReserveSite("docs")
	require.NoError(t, err)

	var wg sync.WaitGroup
	var res *mcpgo.CallToolResult
	var callErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := mcpgo.CallToolRequest{}
		req.Params.Arguments = map[string]any{"site_key": "docs"}
		res, callErr = f.s.handleCrawlSite(context.Background(), req)
	}()
	time.Sleep(50 * time.Millisecond)
	moved, err := config.MergeSiteFields(config.SiteConfig{}, f.entry("/docs/new.html"))
	require.NoError(t, err)
	f.s.putSite("docs", &moved)
	release()
	wg.Wait()

	require.NoError(t, callErr)
	require.False(t, res.IsError, "%v", res.Content)
	var started map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcpgo.TextContent).Text), &started))
	require.Equal(t, "started", started["status"])
	id := started["job_id"].(string)
	require.Eventually(t, func() bool { return isTerminalStatus(f.s.jobManager.GetJob(id).Status) }, time.Minute, 10*time.Millisecond)
	assert.Positive(t, f.newHits.Load(), "the crawl used the entry saved while crawl_site waited")
}

// Under -race this fails if saving races handlers reading sites.
func TestSaveSite_ConcurrentWithReads(t *testing.T) {
	f := newSaveFixture(t, true)
	var wg sync.WaitGroup
	saveErrs := make([]string, 0, 5)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 5 {
			req := mcpgo.CallToolRequest{}
			req.Params.Arguments = map[string]any{"site_key": "s" + string(rune('a'+i)), "entry": f.entry("/docs/new.html")}
			if res, err := f.s.handleSaveSite(context.Background(), req); err != nil || res.IsError {
				saveErrs = append(saveErrs, fmt.Sprint(err, res))
			}
		}
	}()
	for range 20 {
		callJSON(t, f.s.handleListSites, map[string]any{})
	}
	wg.Wait()
	assert.Empty(t, saveErrs)
	assert.Len(t, f.s.siteKeys(), 6)
}
