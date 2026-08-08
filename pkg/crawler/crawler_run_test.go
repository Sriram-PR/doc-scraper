package crawler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

// These tests characterize the CURRENT end-to-end behavior of Crawler.Run
// against an in-memory httptest server, with no real network access. They
// exist to lock in observable behavior before Run is split into smaller
// functions.

// requestRecorder tracks every path requested against a test server so tests
// can assert that out-of-scope / disallowed / over-depth URLs were never
// actually fetched, not merely that they weren't saved.
type requestRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *requestRecorder) record(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, path)
}

func (r *requestRecorder) wasRequested(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.paths {
		if p == path {
			return true
		}
	}
	return false
}

func (r *requestRecorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.paths {
		if p == path {
			n++
		}
	}
	return n
}

// newDocServer starts an httptest server that serves the given path->HTML
// map with a text/html content type, recording every requested path
// (including unregistered ones, which get a 404 -- this covers robots.txt).
func newDocServer(t *testing.T, pages map[string]string) (*httptest.Server, *requestRecorder) {
	t.Helper()
	rec := &requestRecorder{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.URL.Path)
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, rec
}

// newTestAppConfig returns a minimal AppConfig suitable for a fast,
// network-free crawl of an httptest server. AllowPrivateNetworks is set so
// the SSRF guard doesn't block dials to 127.0.0.1.
func newTestAppConfig(t *testing.T) *config.AppConfig {
	t.Helper()
	return &config.AppConfig{
		NumWorkers:         2,
		MaxRequests:        4,
		MaxRequestsPerHost: 2,
		OutputBaseDir:      t.TempDir(),
		StateDir:           t.TempDir(),
		DefaultUserAgent:   "test-agent",
		MaxRetries:         1,
		HTTPClientSettings: config.HTTPClientConfig{
			Timeout:              10 * time.Second,
			AllowPrivateNetworks: true,
		},
		EnableJSONLOutput:   true,
		JSONLOutputFilename: "pages.jsonl",
	}
}

// baseSiteConfig returns a SiteConfig pointed at server with sane defaults;
// callers override fields (StartURLs, AllowedPathPrefix, MaxDepth, etc.) as
// needed for the scenario under test.
func baseSiteConfig(server *httptest.Server, startPath string) *config.SiteConfig {
	host := serverHostname(server)
	return &config.SiteConfig{
		StartURLs:         []string{server.URL + startPath},
		AllowedDomain:     host,
		AllowedPathPrefix: "/docs",
		ContentSelector:   "body",
		MaxDepth:          0, // unlimited
	}
}

func serverHostname(server *httptest.Server) string {
	u := server.URL
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	if idx := strings.LastIndex(u, ":"); idx >= 0 {
		return u[:idx]
	}
	return u
}

// siteOutputDir mirrors the layout Crawler computes internally
// (OutputBaseDir/SanitizeFilename(AllowedDomain)).
func siteOutputDir(appCfg *config.AppConfig, siteCfg *config.SiteConfig) string {
	return filepath.Join(appCfg.OutputBaseDir, utils.SanitizeFilename(siteCfg.AllowedDomain))
}

// runCrawl wires up a real store/fetcher/rate-limiter and drives Crawler.Run
// to completion (or a 20s safety timeout), matching how orchestrate.Orchestrator
// builds a crawler for a single site.
func runCrawl(t *testing.T, appCfg *config.AppConfig, siteCfg *config.SiteConfig) error {
	t.Helper()
	logger := silentLogger()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := storage.NewBadgerStore(ctx, appCfg.StateDir, siteCfg.AllowedDomain, false, logger)
	require.NoError(t, err, "NewBadgerStore")
	defer store.Close()

	httpClient := fetch.NewClient(appCfg.HTTPClientSettings, logger)
	fetcher := fetch.NewFetcher(httpClient, appCfg, logger)
	rateLimiter := fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, logger)

	c, err := NewCrawler(appCfg, siteCfg, "testsite", logger, store, fetcher, rateLimiter, ctx, cancel, false)
	require.NoError(t, err, "NewCrawler")

	return c.Run(false)
}

func TestCrawlerRun_HappyPath(t *testing.T) {
	pages := map[string]string{
		"/docs/index.html": `<html><head><title>Index</title></head><body>
			<p>Welcome.</p>
			<a href="/docs/child1.html">Child 1</a>
			<a href="/docs/child2.html">Child 2</a>
			<a href="/other/out-of-scope.html">Out of scope</a>
			<a href="http://example.invalid/off-domain.html">Off domain</a>
		</body></html>`,
		"/docs/child1.html": `<html><head><title>Child One</title></head><body>
			<p>Child one content.</p>
			<a href="/docs/index.html">Back to index</a>
		</body></html>`,
		"/docs/child2.html": `<html><head><title>Child Two</title></head><body>
			<p>Child two content.</p>
			<a href="/docs/grandchild.html">Grandchild</a>
		</body></html>`,
		"/docs/grandchild.html": `<html><head><title>Grandchild</title></head><body>
			<p>Leaf page, no further links.</p>
		</body></html>`,
		"/other/out-of-scope.html": `<html><head><title>Nope</title></head><body>Should never be fetched.</body></html>`,
	}
	server, rec := newDocServer(t, pages)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	err := runCrawl(t, appCfg, siteCfg)
	require.NoError(t, err, "Run should succeed for a fully in-scope, reachable site")

	outDir := siteOutputDir(appCfg, siteCfg)
	expectedMarkdown := []string{"index.md", "child1.md", "child2.md", "grandchild.md"}
	for _, name := range expectedMarkdown {
		path := filepath.Join(outDir, name)
		assert.FileExists(t, path, "expected markdown output for %s", name)
	}

	entries, err := os.ReadDir(outDir)
	require.NoError(t, err)
	var mdFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			mdFiles = append(mdFiles, e.Name())
		}
	}
	assert.Len(t, mdFiles, len(expectedMarkdown), "no extra/missing markdown files: got %v", mdFiles)

	// Scope enforcement: the out-of-scope link must never be fetched, even
	// though the server would happily serve it.
	assert.False(t, rec.wasRequested("/other/out-of-scope.html"), "out-of-scope link must not be fetched")

	// llms.txt / llms-full.txt are emitted whenever JSONL output is enabled.
	assert.FileExists(t, filepath.Join(outDir, "llms.txt"))
	assert.FileExists(t, filepath.Join(outDir, "llms-full.txt"))

	// JSONL: one record per saved page, sorted by URL (fresh-crawl buffering
	// sorts by URL before flush), plus a trailing crawl_meta record.
	jsonlPath := filepath.Join(outDir, "pages.jsonl")
	lines := readJSONLLines(t, jsonlPath)
	require.Len(t, lines, len(expectedMarkdown)+1, "expected one JSONL line per page plus a trailing crawl_meta line")

	depthByURL := map[string]int{
		server.URL + "/docs/index.html":      0,
		server.URL + "/docs/child1.html":     1,
		server.URL + "/docs/child2.html":     1,
		server.URL + "/docs/grandchild.html": 2,
	}
	seenURLs := make(map[string]bool)
	for _, line := range lines[:len(lines)-1] { // all but the last are page records
		var p models.PageJSONL
		require.NoError(t, json.Unmarshal([]byte(line), &p))
		assert.Equal(t, models.RecordTypePage, p.RecordType)
		wantDepth, ok := depthByURL[p.URL]
		require.True(t, ok, "unexpected URL in JSONL: %s", p.URL)
		assert.Equal(t, wantDepth, p.Depth, "depth mismatch for %s", p.URL)
		seenURLs[p.URL] = true
	}
	assert.Len(t, seenURLs, len(depthByURL), "every expected page URL should appear exactly once")

	var meta models.CrawlMetaJSONL
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &meta))
	assert.Equal(t, models.RecordTypeCrawlMeta, meta.RecordType)
	assert.Equal(t, len(expectedMarkdown), meta.TotalPages)
	assert.Equal(t, siteCfg.AllowedDomain, meta.AllowedDomain)

	// Each in-scope page was fetched exactly once (seed dedup / visited-store
	// dedup works as expected, no duplicate fetches from the mutual links
	// between index and child1).
	assert.Equal(t, 1, rec.count("/docs/index.html"))
	assert.Equal(t, 1, rec.count("/docs/child1.html"))
	assert.Equal(t, 1, rec.count("/docs/child2.html"))
	assert.Equal(t, 1, rec.count("/docs/grandchild.html"))
}

func TestCrawlerRun_ScopeEnforcement(t *testing.T) {
	pages := map[string]string{
		"/docs/index.html": `<html><head><title>Index</title></head><body>
			<a href="/docs/child.html">In scope child</a>
			<a href="/blog/post.html">Wrong prefix, same domain</a>
			<a href="http://evil.example.invalid/data.html">Off domain</a>
		</body></html>`,
		"/docs/child.html": `<html><head><title>Child</title></head><body>In-scope content.</body></html>`,
		"/blog/post.html":  `<html><head><title>Blog</title></head><body>Should never be fetched.</body></html>`,
	}
	server, rec := newDocServer(t, pages)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	err := runCrawl(t, appCfg, siteCfg)
	require.NoError(t, err)

	outDir := siteOutputDir(appCfg, siteCfg)
	assert.FileExists(t, filepath.Join(outDir, "index.md"))
	assert.FileExists(t, filepath.Join(outDir, "child.md"))
	assert.NoFileExists(t, filepath.Join(outDir, "post.md"))

	assert.False(t, rec.wasRequested("/blog/post.html"), "wrong-path-prefix link must not be fetched")
}

func TestCrawlerRun_MaxDepth(t *testing.T) {
	pages := map[string]string{
		"/docs/index.html": `<html><head><title>Index</title></head><body>
			<a href="/docs/child.html">Child</a>
		</body></html>`,
		"/docs/child.html": `<html><head><title>Child</title></head><body>
			<a href="/docs/grandchild.html">Grandchild</a>
		</body></html>`,
		"/docs/grandchild.html": `<html><head><title>Grandchild</title></head><body>Should not be reached at max_depth=2.</body></html>`,
	}
	server, rec := newDocServer(t, pages)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.MaxDepth = 2 // exclusive upper bound: depth 0 and 1 are crawled, depth 2 is not

	err := runCrawl(t, appCfg, siteCfg)
	require.NoError(t, err)

	outDir := siteOutputDir(appCfg, siteCfg)
	assert.FileExists(t, filepath.Join(outDir, "index.md"))
	assert.FileExists(t, filepath.Join(outDir, "child.md"))
	assert.NoFileExists(t, filepath.Join(outDir, "grandchild.md"))

	// Characterization note: the depth-2 link IS extracted and queued (the
	// extraction-time check only skips extraction when next_depth > MaxDepth,
	// and here next_depth == MaxDepth == 2). The queued task is instead
	// rejected by runPolicyChecks' depth >= MaxDepth check, which runs before
	// any HTTP fetch -- so the server never actually sees the request.
	assert.False(t, rec.wasRequested("/docs/grandchild.html"), "over-depth page must not be fetched")
}

func TestCrawlerRun_NoValidStartURLs(t *testing.T) {
	appCfg := newTestAppConfig(t)
	siteCfg := &config.SiteConfig{
		// Domain matches but the path prefix does not: every start URL is
		// rejected during scope validation, so none seed the crawl.
		StartURLs:         []string{"http://127.0.0.1:1/wrong-prefix/index.html"},
		AllowedDomain:     "127.0.0.1",
		AllowedPathPrefix: "/docs",
		ContentSelector:   "body",
	}

	err := runCrawl(t, appCfg, siteCfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no valid start_urls")
}

func TestCrawlerRun_DisallowedPathPatterns(t *testing.T) {
	pages := map[string]string{
		"/docs/index.html": `<html><head><title>Index</title></head><body>
			<a href="/docs/public.html">Public</a>
			<a href="/docs/secret.html">Secret</a>
		</body></html>`,
		"/docs/public.html": `<html><head><title>Public</title></head><body>Public content.</body></html>`,
		"/docs/secret.html": `<html><head><title>Secret</title></head><body>Should never be fetched.</body></html>`,
	}
	server, rec := newDocServer(t, pages)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.DisallowedPathPatterns = []string{`/docs/secret`}

	err := runCrawl(t, appCfg, siteCfg)
	require.NoError(t, err)

	outDir := siteOutputDir(appCfg, siteCfg)
	assert.FileExists(t, filepath.Join(outDir, "index.md"))
	assert.FileExists(t, filepath.Join(outDir, "public.md"))
	assert.NoFileExists(t, filepath.Join(outDir, "secret.md"))

	assert.False(t, rec.wasRequested("/docs/secret.html"), "disallowed-pattern link must not be fetched")
}
