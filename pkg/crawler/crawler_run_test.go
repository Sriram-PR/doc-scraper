package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
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

// testSiteKey is the site key every harness in this file crawls under; output
// and state are keyed by it, not by the domain.
const testSiteKey = "testsite"

// siteOutputDir mirrors the layout Crawler computes internally
// (OutputBaseDir/SanitizeFilename(siteKey)).
func siteOutputDir(appCfg *config.AppConfig, _ *config.SiteConfig) string {
	return filepath.Join(appCfg.OutputBaseDir, utils.SanitizeFilename(testSiteKey))
}

// runCrawl wires up a real store/fetcher/rate-limiter and drives Crawler.Run
// to completion (or a 20s safety timeout), matching how orchestrate.Orchestrator
// builds a crawler for a single site.
func runCrawl(t *testing.T, appCfg *config.AppConfig, siteCfg *config.SiteConfig) error {
	t.Helper()
	crawlLog := newCrawlLog(t)
	logger := crawlLog.logger()
	ctx, cancel := crawlLog.deadlineContext(t, appCfg)
	defer cancel()

	store, err := storage.NewBadgerStore(ctx, appCfg.StateDir, testSiteKey, false, logger)
	require.NoError(t, err, "NewBadgerStore")
	defer store.Close()

	httpClient := fetch.NewClient(appCfg.HTTPClientSettings, logger)
	fetcher := fetch.NewFetcher(httpClient, appCfg, logger)
	rateLimiter := fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, logger)

	c, err := NewCrawlerWithOptions(appCfg, siteCfg, testSiteKey, logger, store, fetcher, rateLimiter, ctx, cancel, false, nil)
	require.NoError(t, err, "NewCrawlerWithOptions")

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

	assert.False(t, rec.wasRequested("/docs/grandchild.html"), "over-depth page must not be fetched")
}

// TestCrawlerRun_MaxDepthQueuesNoOverDepthTasks pins that links one level past
// the bound are never queued, so they are not recorded as failed tasks (and
// requeued as failures on every resume).
func TestCrawlerRun_MaxDepthQueuesNoOverDepthTasks(t *testing.T) {
	server, ms := newMutableDocServer(t)
	ms.set("/docs/index.html", `<html><head><title>Index</title></head><body>
		<a href="/docs/a.html">A</a><a href="/docs/b.html">B</a></body></html>`)
	ms.set("/docs/a.html", `<html><head><title>A</title></head><body>A</body></html>`)
	ms.set("/docs/b.html", `<html><head><title>B</title></head><body>B</body></html>`)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.MaxDepth = 1

	p := runResumableCrawl(t, appCfg, siteCfg, false)

	assert.EqualValues(t, 1, p.PagesProcessed, "only the start page is a task")
}

// newSitemapDocServer serves /docs/index.html, which links nowhere, and
// /docs/orphan.html, reachable only through a sitemap at sitemapPath. A
// non-empty robots body is served at /robots.txt.
func newSitemapDocServer(t *testing.T, sitemapPath, robots string) (*httptest.Server, *requestRecorder) {
	t.Helper()
	rec := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.URL.Path)
		origin := "http://" + r.Host
		switch r.URL.Path {
		case "/robots.txt":
			if robots == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, strings.ReplaceAll(robots, "{origin}", origin))
		case sitemapPath:
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>`+
				origin+`/docs/orphan.html</loc></url></urlset>`)
		case "/docs/index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html><head><title>Index</title></head><body>No links.</body></html>`)
		case "/docs/orphan.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html><head><title>Orphan</title></head><body>Only in the sitemap.</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, rec
}

func TestCrawlerRun_ProbesProjectPathSitemapWithoutRobots(t *testing.T) {
	server, rec := newSitemapDocServer(t, "/docs/sitemap-index.xml", "")
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	require.NoError(t, runCrawl(t, appCfg, siteCfg))

	assert.FileExists(t, filepath.Join(siteOutputDir(appCfg, siteCfg), "orphan.md"))
	assert.True(t, rec.wasRequested("/sitemap.xml"), "root candidates are probed first")
	assert.False(t, rec.wasRequested("/docs/sitemap_index.xml"), "probing stops at the first sitemap found")
}

func TestCrawlerRun_ProbingHonorsRobotsDisallow(t *testing.T) {
	server, rec := newSitemapDocServer(t, "/docs/sitemap-index.xml", "User-agent: *\nDisallow: /sitemap.xml\n")
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	require.NoError(t, runCrawl(t, appCfg, siteCfg))

	assert.FileExists(t, filepath.Join(siteOutputDir(appCfg, siteCfg), "orphan.md"))
	assert.False(t, rec.wasRequested("/sitemap.xml"), "a candidate disallowed by robots.txt must not be probed")
	assert.True(t, rec.wasRequested("/sitemap-index.xml"))
}

func TestCrawlerRun_RobotsSitemapSkipsProbing(t *testing.T) {
	server, rec := newSitemapDocServer(t, "/maps/docs.xml", "User-agent: *\nAllow: /\n\nSitemap: {origin}/maps/docs.xml\n")
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	require.NoError(t, runCrawl(t, appCfg, siteCfg))

	assert.FileExists(t, filepath.Join(siteOutputDir(appCfg, siteCfg), "orphan.md"))
	for _, p := range []string{"/sitemap.xml", "/docs/sitemap.xml"} {
		assert.False(t, rec.wasRequested(p), "%s must not be probed when robots.txt announces a sitemap", p)
	}
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

// noindexPages mirrors mdBook: print.html repeats the chapters, is marked
// noindex, and is the only route to one page.
func noindexPages(robots string) map[string]string {
	return map[string]string{
		"/docs/index.html": `<html><head><title>Index</title></head><body>
			<a href="/docs/print.html">Print</a></body></html>`,
		"/docs/print.html": `<html><head><title>Print</title><meta name="robots" content="` + robots + `"></head><body>
			<p>Every chapter again.</p><a href="/docs/only-from-print.html">Only here</a></body></html>`,
		"/docs/only-from-print.html": `<html><head><title>Only</title></head><body>Reached via print.</body></html>`,
	}
}

func jsonlHasURL(t *testing.T, appCfg *config.AppConfig, siteCfg *config.SiteConfig, u string) bool {
	t.Helper()
	for _, line := range readJSONLLines(t, filepath.Join(siteOutputDir(appCfg, siteCfg), "pages.jsonl")) {
		var p models.PageJSONL
		require.NoError(t, json.Unmarshal([]byte(line), &p))
		if p.RecordType == models.RecordTypePage && p.URL == u {
			return true
		}
	}
	return false
}

func TestCrawlerRun_NoindexPageNotSavedButFollowed(t *testing.T) {
	server, rec := newDocServer(t, noindexPages("noindex"))
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	require.NoError(t, runCrawl(t, appCfg, siteCfg))

	outDir := siteOutputDir(appCfg, siteCfg)
	assert.True(t, rec.wasRequested("/docs/print.html"))
	assert.NoFileExists(t, filepath.Join(outDir, "print.md"), "noindex page must not be saved")
	assert.False(t, jsonlHasURL(t, appCfg, siteCfg, server.URL+"/docs/print.html"), "noindex page must not reach the JSONL")
	assert.FileExists(t, filepath.Join(outDir, "only-from-print.md"), "links on a noindex page are still followed")
	assert.True(t, jsonlHasURL(t, appCfg, siteCfg, server.URL+"/docs/only-from-print.html"))
}

func TestCrawlerRun_NoindexFromHeader(t *testing.T) {
	pages := noindexPages("all")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/docs/print.html" {
			w.Header().Set("X-Robots-Tag", "noindex")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(srv, "/docs/index.html")

	require.NoError(t, runCrawl(t, appCfg, siteCfg))

	outDir := siteOutputDir(appCfg, siteCfg)
	assert.NoFileExists(t, filepath.Join(outDir, "print.md"))
	assert.FileExists(t, filepath.Join(outDir, "only-from-print.md"))
}

func TestCrawlerRun_NoindexNofollow(t *testing.T) {
	for _, respect := range []bool{false, true} {
		t.Run(fmt.Sprintf("respect_nofollow=%v", respect), func(t *testing.T) {
			server, rec := newDocServer(t, noindexPages("noindex, nofollow"))
			appCfg := newTestAppConfig(t)
			siteCfg := baseSiteConfig(server, "/docs/index.html")
			siteCfg.RespectNofollow = respect

			require.NoError(t, runCrawl(t, appCfg, siteCfg))

			assert.NoFileExists(t, filepath.Join(siteOutputDir(appCfg, siteCfg), "print.md"))
			assert.Equal(t, !respect, rec.wasRequested("/docs/only-from-print.html"),
				"meta nofollow stops link following only when respect_nofollow is set")
		})
	}
}

func TestCrawlerRun_RespectNoindexFalseSavesPage(t *testing.T) {
	server, _ := newDocServer(t, noindexPages("noindex"))
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	off := false
	siteCfg.RespectNoindex = &off

	require.NoError(t, runCrawl(t, appCfg, siteCfg))

	assert.FileExists(t, filepath.Join(siteOutputDir(appCfg, siteCfg), "print.md"))
	assert.True(t, jsonlHasURL(t, appCfg, siteCfg, server.URL+"/docs/print.html"))
}

// TestCrawlerRun_FrontmatterAndTables verifies that every saved .md file carries
// a YAML frontmatter block with page metadata, that its content_hash matches the
// JSONL record for the same page, and that HTML tables survive as GFM pipe tables
// (the GitHubFlavored converter plugin is wired up).
func TestCrawlerRun_FrontmatterAndTables(t *testing.T) {
	pages := map[string]string{
		"/docs/index.html": `<html><head><title>Tables Doc</title></head><body>
			<p>Reference page.</p>
			<table>
				<thead><tr><th>Name</th><th>Type</th></tr></thead>
				<tbody><tr><td>alpha</td><td>string</td></tr></tbody>
			</table>
		</body></html>`,
	}
	server, _ := newDocServer(t, pages)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	err := runCrawl(t, appCfg, siteCfg)
	require.NoError(t, err, "Run should succeed")

	outDir := siteOutputDir(appCfg, siteCfg)
	raw, err := os.ReadFile(filepath.Join(outDir, "index.md"))
	require.NoError(t, err, "read index.md")
	fileStr := string(raw)

	require.True(t, strings.HasPrefix(fileStr, "---\n"), "file must start with a frontmatter block")
	rest := fileStr[len("---\n"):]
	closeIdx := strings.Index(rest, "\n---\n")
	require.GreaterOrEqual(t, closeIdx, 0, "frontmatter block must be closed with ---")

	var fm struct {
		Title       string `yaml:"title"`
		URL         string `yaml:"url"`
		CrawledAt   string `yaml:"crawled_at"`
		ContentHash string `yaml:"content_hash"`
		Depth       int    `yaml:"depth"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(rest[:closeIdx]), &fm), "parse frontmatter YAML")

	assert.Equal(t, "Tables Doc", fm.Title)
	assert.Contains(t, fm.URL, "/docs/index.html")
	assert.Equal(t, 0, fm.Depth, "start URL is depth 0")
	assert.Len(t, fm.ContentHash, 64, "content_hash should be a hex SHA-256")
	_, tErr := time.Parse(time.RFC3339, fm.CrawledAt)
	require.NoError(t, tErr, "crawled_at should be RFC3339")

	body := rest[closeIdx+len("\n---\n"):]
	assert.Contains(t, body, "|", "GFM pipe tables must be present (tables plugin enabled)")
	assert.Contains(t, body, "Name")
	assert.Contains(t, body, "alpha")

	// The frontmatter hash must match the JSONL record's content_hash for the page.
	jsonlBytes, err := os.ReadFile(filepath.Join(outDir, "pages.jsonl"))
	require.NoError(t, err, "read pages.jsonl")
	var recordHash string
	for _, line := range strings.Split(strings.TrimSpace(string(jsonlBytes)), "\n") {
		if line == "" {
			continue
		}
		var p models.PageJSONL
		require.NoError(t, json.Unmarshal([]byte(line), &p))
		if p.RecordType == models.RecordTypePage && strings.Contains(p.URL, "/docs/index.html") {
			recordHash = p.ContentHash
		}
	}
	assert.Equal(t, recordHash, fm.ContentHash, "frontmatter content_hash must match JSONL content_hash")
}

// mutableDocServer serves per-path HTML that tests can change between crawls, and
// counts hits per path so incremental re-fetch behavior can be asserted.
type mutableDocServer struct {
	mu     sync.Mutex
	body   map[string]string
	status map[string]int
	hits   map[string]int
}

func newMutableDocServer(t *testing.T) (*httptest.Server, *mutableDocServer) {
	t.Helper()
	ms := &mutableDocServer{body: map[string]string{}, status: map[string]int{}, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		ms.hits[r.URL.Path]++
		body, ok := ms.body[r.URL.Path]
		code := ms.status[r.URL.Path]
		ms.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, ms
}

func (m *mutableDocServer) set(path, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.body[path] = body
}

func (m *mutableDocServer) remove(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.body, path)
}

func (m *mutableDocServer) setStatus(path string, code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status[path] = code
}

func (m *mutableDocServer) hitCount(path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits[path]
}

// runResumableCrawl drives Crawler.Run with an explicit resume flag, reusing the
// same StateDir/OutputBaseDir across calls so incremental re-crawls see prior state.
func runResumableCrawl(t *testing.T, appCfg *config.AppConfig, siteCfg *config.SiteConfig, resume bool) CrawlerProgress {
	t.Helper()
	p, err := runResumableCrawlErr(t, appCfg, siteCfg, resume)
	require.NoError(t, err)
	return p
}

func runResumableCrawlErr(t *testing.T, appCfg *config.AppConfig, siteCfg *config.SiteConfig, resume bool) (CrawlerProgress, error) {
	t.Helper()
	crawlLog := newCrawlLog(t)
	logger := crawlLog.logger()
	ctx, cancel := crawlLog.deadlineContext(t, appCfg)
	defer cancel()
	store, err := storage.NewBadgerStore(ctx, appCfg.StateDir, testSiteKey, resume, logger)
	require.NoError(t, err)
	defer store.Close()
	httpClient := fetch.NewClient(appCfg.HTTPClientSettings, logger)
	fetcher := fetch.NewFetcher(httpClient, appCfg, logger)
	rl := fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, logger)
	c, err := NewCrawlerWithOptions(appCfg, siteCfg, testSiteKey, logger, store, fetcher, rl, ctx, cancel, resume, nil)
	require.NoError(t, err)
	err = c.Run(resume)
	return c.GetProgress(), err
}

func mdFileIn(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			return filepath.Join(dir, e.Name())
		}
	}
	require.FailNowf(t, "no .md file found", "dir %s", dir)
	return ""
}

// TestCrawlerRun_IncrementalContentScoped is the regression test for content-scoped
// incremental crawling: an incremental re-crawl re-fetches known pages, skips
// reprocessing when only the page shell changed, and reprocesses when the content
// selector region actually changed.
func TestCrawlerRun_IncrementalContentScoped(t *testing.T) {
	server, ms := newMutableDocServer(t)
	page := func(content, shell string) string {
		return `<html><head><title>Doc</title></head><body>` +
			`<main id="c">` + content + `</main>` +
			`<footer>` + shell + `</footer></body></html>`
	}
	ms.set("/docs/index.html", page("<p>ORIGINAL</p>", "built at T0"))

	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	appCfg.StateDir = t.TempDir()
	appCfg.OutputBaseDir = t.TempDir()
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	// Crawl 1 (fresh).
	runResumableCrawl(t, appCfg, siteCfg, false)
	md := mdFileIn(t, siteOutputDir(appCfg, siteCfg))
	info1, err := os.Stat(md)
	require.NoError(t, err)
	body1, err := os.ReadFile(md)
	require.NoError(t, err)
	require.Contains(t, string(body1), "ORIGINAL")
	require.Equal(t, 1, ms.hitCount("/docs/index.html"))

	// Crawl 2: change ONLY the shell (outside #c). Content unchanged -> must re-fetch but NOT rewrite.
	ms.set("/docs/index.html", page("<p>ORIGINAL</p>", "built at T1-completely-different-shell"))
	runResumableCrawl(t, appCfg, siteCfg, true)
	info2, err := os.Stat(md)
	require.NoError(t, err)
	assert.Equal(t, 2, ms.hitCount("/docs/index.html"), "incremental must re-fetch the known page")
	assert.Equal(t, info1.ModTime().UnixNano(), info2.ModTime().UnixNano(),
		"shell-only change must NOT rewrite the .md (content-scoped hash unchanged)")

	// Crawl 3: change the CONTENT inside #c -> must reprocess and rewrite.
	ms.set("/docs/index.html", page("<p>UPDATED CONTENT</p>", "built at T2"))
	runResumableCrawl(t, appCfg, siteCfg, true)
	info3, err := os.Stat(md)
	require.NoError(t, err)
	body3, err := os.ReadFile(md)
	require.NoError(t, err)
	assert.Equal(t, 3, ms.hitCount("/docs/index.html"))
	assert.Contains(t, string(body3), "UPDATED CONTENT", "changed content must be rewritten")
	assert.NotContains(t, string(body3), "ORIGINAL")
	assert.NotEqual(t, info1.ModTime().UnixNano(), info3.ModTime().UnixNano(),
		"changed content must rewrite the .md")
}

func TestCrawlerRun_IncrementalCountsUnchangedPages(t *testing.T) {
	server, ms := newMutableDocServer(t)
	ms.set("/docs/index.html", `<html><head><title>Doc</title></head><body>`+
		`<main id="c"><p>HOME</p><a href="/docs/a.html">a</a></main></body></html>`)
	ms.set("/docs/a.html", `<html><head><title>A</title></head><body><main id="c"><p>A1</p></main></body></html>`)

	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	p := runResumableCrawl(t, appCfg, siteCfg, false)
	assert.EqualValues(t, 2, p.PagesProcessed, "fresh crawl")
	assert.EqualValues(t, 0, p.PagesUnchanged, "fresh crawl")

	p = runResumableCrawl(t, appCfg, siteCfg, true)
	assert.EqualValues(t, 0, p.PagesProcessed, "nothing changed")
	assert.EqualValues(t, 2, p.PagesUnchanged, "nothing changed")

	ms.set("/docs/a.html", `<html><head><title>A</title></head><body><main id="c"><p>A2</p></main></body></html>`)
	p = runResumableCrawl(t, appCfg, siteCfg, true)
	assert.EqualValues(t, 1, p.PagesProcessed, "one page changed")
	assert.EqualValues(t, 1, p.PagesUnchanged, "one page changed")
}

func TestCrawlerRun_IncrementalResavesUnchangedPageWithMissingFile(t *testing.T) {
	server, ms := newMutableDocServer(t)
	ms.set("/docs/index.html", `<html><head><title>Doc</title></head><body>`+
		`<main id="c"><p>HOME</p><a href="/docs/a.html">a</a></main></body></html>`)
	ms.set("/docs/a.html", `<html><head><title>A</title></head><body><main id="c"><p>A1</p></main></body></html>`)

	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	runResumableCrawl(t, appCfg, siteCfg, false)
	md := filepath.Join(siteOutputDir(appCfg, siteCfg), "a.md")
	require.FileExists(t, md)
	require.NoError(t, os.Remove(md))

	p := runResumableCrawl(t, appCfg, siteCfg, true)
	assert.EqualValues(t, 1, p.PagesProcessed, "the page whose file is gone is saved again")
	assert.EqualValues(t, 1, p.PagesUnchanged, "the page whose file is present is still skipped")
	assert.FileExists(t, md)
}

func TestCrawlerRun_CountsSavedAndFailedPages(t *testing.T) {
	server, ms := newMutableDocServer(t)
	ms.set("/docs/index.html", `<html><head><title>Index</title></head><body>
		<a href="/docs/a.html">A</a><a href="/docs/gone.html">Gone</a><a href="/docs/print.html">Print</a></body></html>`)
	ms.set("/docs/a.html", `<html><head><title>A</title></head><body>A</body></html>`)
	ms.set("/docs/print.html", `<html><head><title>P</title><meta name="robots" content="noindex"></head><body>P</body></html>`)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	p := runResumableCrawl(t, appCfg, siteCfg, false)

	assert.EqualValues(t, 4, p.PagesProcessed, "every attempted task")
	assert.EqualValues(t, 2, p.PagesSaved, "index and a; the noindex page writes nothing")
	assert.EqualValues(t, 1, p.PagesFailed, "gone.html 404s")
}

// TestCrawlerRun_IncrementalPartialFailureSucceeds keeps the degrade-not-fail
// policy for a re-crawl where some pages still check out.
func TestCrawlerRun_IncrementalPartialFailureSucceeds(t *testing.T) {
	server, ms := newMutableDocServer(t)
	ms.set("/docs/index.html", `<html><head><title>Doc</title></head><body>`+
		`<main id="c"><p>HOME</p><a href="/docs/a.html">a</a></main></body></html>`)
	ms.set("/docs/a.html", `<html><head><title>A</title></head><body><main id="c"><p>A</p></main></body></html>`)
	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	runResumableCrawl(t, appCfg, siteCfg, false)
	ms.remove("/docs/a.html")
	p := runResumableCrawl(t, appCfg, siteCfg, true)

	assert.EqualValues(t, 1, p.PagesUnchanged)
}

// TestCrawlerRun_ResumeRetryingOnlyFailedPagesSucceeds keeps today's rule for
// a non-incremental resume: the corpus from the earlier run is intact, so a
// page that still fails on retry does not fail the run.
func TestCrawlerRun_ResumeRetryingOnlyFailedPagesSucceeds(t *testing.T) {
	server, ms := newMutableDocServer(t)
	ms.set("/docs/index.html", `<html><head><title>Doc</title></head><body>`+
		`<main id="c"><p>HOME</p><a href="/docs/gone.html">gone</a></main></body></html>`)
	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	runResumableCrawl(t, appCfg, siteCfg, false)
	require.Equal(t, 1, ms.hitCount("/docs/gone.html"))

	p := runResumableCrawl(t, appCfg, siteCfg, true)

	require.Equal(t, 2, ms.hitCount("/docs/gone.html"), "resume retries the failed page")
	assert.EqualValues(t, 1, p.PagesProcessed)
}

// TestCrawlerRun_FirstWatchStyleRunOfDeadSiteFails covers watch's first run:
// resume and incremental are both on, there is no prior corpus, and nothing
// could be fetched.
func TestCrawlerRun_FirstWatchStyleRunOfDeadSiteFails(t *testing.T) {
	server, _ := newMutableDocServer(t) // serves nothing
	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	siteCfg := baseSiteConfig(server, "/docs/index.html")

	_, err := runResumableCrawlErr(t, appCfg, siteCfg, true)

	require.ErrorContains(t, err, "zero successful pages")
}

// TestCrawlerRun_IncrementalFollowsLinksOfUnchangedPages covers a new page
// linked only from the page shell (a docs sidebar gaining a chapter): the
// content hash still matches, but the new link must be discovered.
func TestCrawlerRun_IncrementalFollowsLinksOfUnchangedPages(t *testing.T) {
	server, ms := newMutableDocServer(t)
	page := func(nav string) string {
		return `<html><head><title>Doc</title></head><body><nav>` + nav + `</nav>` +
			`<main id="c"><p>SAME</p></main></body></html>`
	}
	ms.set("/docs/index.html", page(`<a href="/docs/a.html">a</a>`))
	ms.set("/docs/a.html", `<html><head><title>A</title></head><body><main id="c"><p>A</p></main></body></html>`)
	ms.set("/docs/b.html", `<html><head><title>B</title></head><body><main id="c"><p>NEW CHAPTER</p></main></body></html>`)

	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	runResumableCrawl(t, appCfg, siteCfg, false)
	require.Equal(t, 0, ms.hitCount("/docs/b.html"))

	ms.set("/docs/index.html", page(`<a href="/docs/a.html">a</a><a href="/docs/b.html">b</a>`))
	runResumableCrawl(t, appCfg, siteCfg, true)

	assert.Equal(t, 1, ms.hitCount("/docs/b.html"), "link added outside the content region must be followed")
	assert.True(t, jsonlHasURL(t, appCfg, siteCfg, server.URL+"/docs/b.html"))
}

// TestCrawlerRun_Incremental404LeavesOutput verifies that when a previously-crawled
// page returns 404 on an incremental re-crawl, its existing output is left as-is.
// It is the only page, so nothing was re-checked and the run reports failure.
func TestCrawlerRun_Incremental404LeavesOutput(t *testing.T) {
	server, ms := newMutableDocServer(t)
	ms.set("/docs/index.html", `<html><head><title>Doc</title></head><body><main id="c"><p>KEEP ME</p></main></body></html>`)

	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	appCfg.StateDir = t.TempDir()
	appCfg.OutputBaseDir = t.TempDir()
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	runResumableCrawl(t, appCfg, siteCfg, false)
	md := mdFileIn(t, siteOutputDir(appCfg, siteCfg))
	require.FileExists(t, md)

	// Page now 404s on the incremental re-crawl.
	ms.remove("/docs/index.html")
	_, err := runResumableCrawlErr(t, appCfg, siteCfg, true)
	require.ErrorContains(t, err, "zero successful pages")

	assert.FileExists(t, md, "existing output must be left as-is when a page now 404s")
	body, err := os.ReadFile(md)
	require.NoError(t, err)
	assert.Contains(t, string(body), "KEEP ME", "previously-crawled content must be preserved")
}

// TestCrawlerRun_IncrementalDropsGonePages covers a page deleted from the site:
// a 404 or 410 on an incremental re-check removes it from the corpus, while a
// 403 (often a block, not a deletion) or a server error keeps it.
func TestCrawlerRun_IncrementalDropsGonePages(t *testing.T) {
	for _, tc := range []struct {
		status int
		kept   bool
	}{
		{http.StatusNotFound, false},
		{http.StatusGone, false},
		{http.StatusForbidden, true},
		{http.StatusInternalServerError, true},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			server, ms := newMutableDocServer(t)
			ms.set("/docs/index.html", `<html><head><title>Doc</title></head><body>`+
				`<main id="c"><p>HOME</p><a href="/docs/a.html">a</a></main></body></html>`)
			ms.set("/docs/a.html", `<html><head><title>A</title></head><body><main id="c"><p>PAGE-A</p></main></body></html>`)
			appCfg := newTestAppConfig(t)
			appCfg.EnableIncremental = true
			siteCfg := baseSiteConfig(server, "/docs/index.html")
			siteCfg.ContentSelector = "#c"
			aURL := server.URL + "/docs/a.html"

			runResumableCrawl(t, appCfg, siteCfg, false)
			require.True(t, jsonlHasURL(t, appCfg, siteCfg, aURL))
			require.Len(t, mdFilesContaining(t, siteOutputDir(appCfg, siteCfg), "PAGE-A"), 1)

			ms.setStatus("/docs/a.html", tc.status)
			runResumableCrawl(t, appCfg, siteCfg, true)

			assert.Equal(t, tc.kept, jsonlHasURL(t, appCfg, siteCfg, aURL), "pages.jsonl record")
			assert.Equal(t, tc.kept, len(mdFilesContaining(t, siteOutputDir(appCfg, siteCfg), "PAGE-A")) == 1, "markdown file")
			assert.True(t, jsonlHasURL(t, appCfg, siteCfg, server.URL+"/docs/index.html"))
		})
	}
}

func mdFilesContaining(t *testing.T, dir, marker string) []string {
	t.Helper()
	var found []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		b, err := os.ReadFile(p)
		if err == nil && strings.Contains(string(b), marker) {
			found = append(found, p)
		}
		return err
	}))
	return found
}

// TestCrawlerRun_IncrementalDedupesJSONL is the regression test for the resume/
// incremental JSONL corruption: reprocessing a changed page must not leave its
// stale record behind. The finalized pages.jsonl (and the llms-full.txt and
// crawl_meta derived from it) must hold exactly one record per URL, carrying the
// fresh content and an accurate page count.
func TestCrawlerRun_IncrementalDedupesJSONL(t *testing.T) {
	server, ms := newMutableDocServer(t)
	ms.set("/docs/index.html", `<html><head><title>Doc</title></head><body>`+
		`<main id="c"><p>HOME</p><a href="/docs/a.html">a</a></main></body></html>`)
	ms.set("/docs/a.html", `<html><head><title>A</title></head><body><main id="c"><p>A-ORIGINAL</p></main></body></html>`)

	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	appCfg.StateDir = t.TempDir()
	appCfg.OutputBaseDir = t.TempDir()
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	// Crawl 1 (fresh): two pages.
	runResumableCrawl(t, appCfg, siteCfg, false)

	// Crawl 2 (incremental): only a.html's content region changes, so it is reprocessed.
	ms.set("/docs/a.html", `<html><head><title>A</title></head><body><main id="c"><p>A-UPDATED</p></main></body></html>`)
	runResumableCrawl(t, appCfg, siteCfg, true)

	outDir := siteOutputDir(appCfg, siteCfg)
	lines := readJSONLLines(t, filepath.Join(outDir, "pages.jsonl"))
	require.GreaterOrEqual(t, len(lines), 3, "expected two page records plus a crawl_meta line")

	counts := map[string]int{}
	var aRecord models.PageJSONL
	for _, line := range lines[:len(lines)-1] {
		var p models.PageJSONL
		require.NoError(t, json.Unmarshal([]byte(line), &p))
		require.Equal(t, models.RecordTypePage, p.RecordType)
		counts[p.URL]++
		if p.URL == server.URL+"/docs/a.html" {
			aRecord = p
		}
	}
	require.Len(t, counts, 2, "exactly two distinct page URLs")
	for u, n := range counts {
		assert.Equalf(t, 1, n, "URL %s must appear exactly once in JSONL (no stale duplicate)", u)
	}
	assert.Contains(t, aRecord.Content, "A-UPDATED", "the surviving record must carry the fresh content")
	assert.NotContains(t, aRecord.Content, "A-ORIGINAL", "the stale record must be dropped")

	var meta models.CrawlMetaJSONL
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &meta))
	assert.Equal(t, models.RecordTypeCrawlMeta, meta.RecordType)
	assert.Equal(t, 2, meta.TotalPages, "crawl_meta must count unique pages, not duplicated records")

	full, err := os.ReadFile(filepath.Join(outDir, "llms-full.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(full), "A-UPDATED")
	assert.NotContains(t, string(full), "A-ORIGINAL", "llms-full.txt must not carry stale content")
}

// TestCrawlerRun_ResumeKeepsEnqueuedDepth interrupts a crawl while discovered
// pages are still queued, then resumes and checks the requeued pages keep their
// link-graph depth so max_depth still bounds the crawl.
func TestCrawlerRun_ResumeKeepsEnqueuedDepth(t *testing.T) {
	page := func(links ...string) string {
		var b strings.Builder
		b.WriteString("<html><head><title>T</title></head><body>")
		for _, l := range links {
			b.WriteString(`<a href="` + l + `">x</a>`)
		}
		b.WriteString("</body></html>")
		return b.String()
	}
	pages := map[string]string{
		"/docs/index.html": page("/docs/a1.html", "/docs/a2.html", "/docs/a3.html"),
		"/docs/a1.html":    page("/docs/b1.html"),
		"/docs/a2.html":    page("/docs/b2.html"),
		"/docs/a3.html":    page("/docs/b3.html"),
		"/docs/b1.html":    page("/docs/c1.html"),
		"/docs/b2.html":    page("/docs/c2.html"),
		"/docs/b3.html":    page("/docs/c3.html"),
		"/docs/c1.html":    page("/docs/d1.html"),
		"/docs/c2.html":    page("/docs/d2.html"),
		"/docs/c3.html":    page("/docs/d3.html"),
		"/docs/d1.html":    page(),
		"/docs/d2.html":    page(),
		"/docs/d3.html":    page(),
	}

	depths := func(appCfg *config.AppConfig, siteCfg *config.SiteConfig) map[string]int {
		out := map[string]int{}
		lines := readJSONLLines(t, filepath.Join(siteOutputDir(appCfg, siteCfg), "pages.jsonl"))
		for _, line := range lines {
			var p models.PageJSONL
			require.NoError(t, json.Unmarshal([]byte(line), &p))
			if p.RecordType == models.RecordTypePage {
				out[p.URL] = p.Depth
			}
		}
		return out
	}

	newCfgs := func(server *httptest.Server) (*config.AppConfig, *config.SiteConfig) {
		appCfg := newTestAppConfig(t)
		appCfg.NumWorkers = 1
		appCfg.MaxRequests = 1
		appCfg.MaxRequestsPerHost = 1
		siteCfg := baseSiteConfig(server, "/docs/index.html")
		siteCfg.MaxDepth = 3
		return appCfg, siteCfg
	}

	baseServer, _ := newDocServer(t, pages)
	baseApp, baseSite := newCfgs(baseServer)
	require.NoError(t, runCrawl(t, baseApp, baseSite))
	want := map[string]int{}
	for u, d := range depths(baseApp, baseSite) {
		want[strings.TrimPrefix(u, baseServer.URL)] = d
	}
	require.Equal(t, 1, want["/docs/a1.html"])
	require.Equal(t, 2, want["/docs/b1.html"])
	require.NotContains(t, want, "/docs/c1.html", "max_depth=3 bounds the crawl to depths 0-2")

	crawlLog := newCrawlLog(t)
	ctx, cancel := crawlLog.deadlineContext(t, baseApp)
	defer cancel()
	var interrupted sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, body)
		if r.URL.Path == "/docs/a1.html" {
			interrupted.Do(cancel)
		}
	}))
	t.Cleanup(server.Close)

	appCfg, siteCfg := newCfgs(server)
	logger := crawlLog.logger()
	store, err := storage.NewBadgerStore(ctx, appCfg.StateDir, testSiteKey, false, logger)
	require.NoError(t, err)
	client := fetch.NewClient(appCfg.HTTPClientSettings, logger)
	c, err := NewCrawlerWithOptions(appCfg, siteCfg, testSiteKey, logger, store, fetch.NewFetcher(client, appCfg, logger),
		fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, logger), ctx, cancel, false, nil)
	require.NoError(t, err)
	_ = c.Run(false)
	require.NoError(t, store.Close())

	runResumableCrawl(t, appCfg, siteCfg, true)

	got := map[string]int{}
	for u, d := range depths(appCfg, siteCfg) {
		got[strings.TrimPrefix(u, server.URL)] = d
	}
	assert.Equal(t, want, got, "resumed crawl must match the uninterrupted baseline, depths included")
}

// A page whose fetch is cut off by cancelling the crawl was not processed: it
// must not count as processed, and it must keep its stored content hash, or the
// next incremental crawl reprocesses it as if it had changed.
func TestCrawlerRun_CancelledIncrementalLeavesInFlightPageUntouched(t *testing.T) {
	var block atomic.Bool
	blocked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/docs/index.html":
			_, _ = io.WriteString(w, `<html><head><title>Doc</title></head><body><main id="c"><p>HOME</p><a href="/docs/a.html">a</a></main></body></html>`)
		case "/docs/a.html":
			if block.Load() {
				select {
				case blocked <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, `<html><head><title>A</title></head><body><main id="c"><p>PAGE-A</p></main></body></html>`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	appCfg := newTestAppConfig(t)
	appCfg.EnableIncremental = true
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	siteCfg.ContentSelector = "#c"

	runResumableCrawl(t, appCfg, siteCfg, false)

	block.Store(true)
	logger := silentLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := storage.NewBadgerStore(ctx, appCfg.StateDir, testSiteKey, true, logger)
	require.NoError(t, err)
	fetcher := fetch.NewFetcher(fetch.NewClient(appCfg.HTTPClientSettings, logger), appCfg, logger)
	c, err := NewCrawlerWithOptions(appCfg, siteCfg, testSiteKey, logger, store, fetcher, fetch.NewRateLimiter(0, logger), ctx, cancel, true, nil)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(true)
	}()
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("a.html was never requested")
	}
	cancel()
	<-done
	require.NoError(t, store.Close())
	assert.EqualValues(t, 0, c.GetProgress().PagesProcessed, "the cut-off fetch of a.html is not a processed page")

	block.Store(false)
	p := runResumableCrawl(t, appCfg, siteCfg, true)
	assert.EqualValues(t, 0, p.PagesProcessed, "nothing changed, so nothing is reprocessed")
	assert.EqualValues(t, 2, p.PagesUnchanged)
}

func TestCrawlerRun_CancelDuringInitialRobotsFetchReturns(t *testing.T) {
	robotsRequested := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			once.Do(func() { close(robotsRequested) })
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><p>Page.</p></body></html>")
	}))
	t.Cleanup(server.Close)

	appCfg := newTestAppConfig(t)
	siteCfg := baseSiteConfig(server, "/docs/index.html")
	logger := silentLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := storage.NewBadgerStore(ctx, appCfg.StateDir, testSiteKey, false, logger)
	require.NoError(t, err)
	defer store.Close()
	httpClient := fetch.NewClient(appCfg.HTTPClientSettings, logger)
	fetcher := fetch.NewFetcher(httpClient, appCfg, logger)
	rateLimiter := fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, logger)
	c, err := NewCrawlerWithOptions(appCfg, siteCfg, testSiteKey, logger, store, fetcher, rateLimiter, ctx, cancel, false, nil)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(false)
	}()

	select {
	case <-robotsRequested:
	case <-time.After(10 * time.Second):
		t.Fatal("robots.txt was never requested")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the crawl was cancelled during the initial robots.txt fetch")
	}
}
