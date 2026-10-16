package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func pageRecord(url, body string) models.PageJSONL {
	return models.PageJSONL{
		RecordType:  models.RecordTypePage,
		URL:         url,
		Title:       "Page",
		Content:     "# Page\n\n" + body + " " + strings.Repeat("filler ", 40),
		ContentHash: "h-" + url,
	}
}

// Sites crawled before the full-text index existed have JSONL but no chunks;
// startup backfill must index those and leave the others alone.
func TestBackfillChunks(t *testing.T) {
	ctx := context.Background()
	s, jsonlPath := newTestServer(t, "legacy", "legacy.example.com")
	idx := attachIndex(t, s)
	app := s.cfg.AppConfig
	off := false
	app.Sites["chunked"] = &config.SiteConfig{AllowedDomain: "chunked.example.com", AllowedPathPrefix: "/"}
	app.Sites["optedout"] = &config.SiteConfig{AllowedDomain: "optedout.example.com", AllowedPathPrefix: "/", EnableJSONLOutput: &off}

	writeJSONLRecords(t, jsonlPath, []interface{}{pageRecord("https://legacy.example.com/a", "Backfilled retry guidance")})
	writeJSONLRecords(t, filepath.Join(app.SiteOutputDir("chunked"), "pages.jsonl"),
		[]interface{}{pageRecord("https://chunked.example.com/new", "Text only in the JSONL")})
	require.NoError(t, idx.ReplaceChunks(ctx, "chunked", "https://chunked.example.com/old", "Old", "h-old",
		chunk.Split("# Old\n\nAlready indexed text. "+strings.Repeat("filler ", 40))))
	writeJSONLRecords(t, filepath.Join(app.SiteOutputDir("optedout"), "pages.jsonl"),
		[]interface{}{pageRecord("https://optedout.example.com/x", "Opted out text")})

	s.backfillChunks(ctx)

	has, err := idx.SiteHasChunks(ctx, "legacy")
	require.NoError(t, err)
	assert.True(t, has, "a site with JSONL but no chunks should be backfilled")

	got, _ := callSearchDocs(t, s, map[string]any{"query": "backfilled retry", "site_key": "legacy"})
	assert.EqualValues(t, 1, got["count"])

	got, _ = callSearchDocs(t, s, map[string]any{"query": "JSONL", "site_key": "chunked"})
	assert.EqualValues(t, 0, got["count"], "a site that already has chunks is skipped")

	has, err = idx.SiteHasChunks(ctx, "optedout")
	require.NoError(t, err)
	assert.False(t, has, "a site with JSONL output disabled is skipped")
}

func TestBackfillChunks_StopsOnCancelledContext(t *testing.T) {
	s, jsonlPath := newTestServer(t, "legacy", "legacy.example.com")
	idx := attachIndex(t, s)
	writeJSONLRecords(t, jsonlPath, []interface{}{pageRecord("https://legacy.example.com/a", "Text")})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.backfillChunks(ctx)

	has, err := idx.SiteHasChunks(context.Background(), "legacy")
	require.NoError(t, err)
	assert.False(t, has)
}

func TestBackfillChunks_NoIndex(t *testing.T) {
	s, jsonlPath := newTestServer(t, "legacy", "legacy.example.com")
	writeJSONLRecords(t, jsonlPath, []interface{}{pageRecord("https://legacy.example.com/a", "Text")})
	assert.NotPanics(t, func() { s.backfillChunks(context.Background()) })
}

// crawlJobServer returns a Server whose single site points at a local docs
// server with two linked pages.
func crawlJobServer(t *testing.T) (*Server, *config.SiteConfig) {
	t.Helper()
	docs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/docs/index.html":
			_, _ = io.WriteString(w, `<html><head><title>Home</title></head><body><a href="/docs/guide.html">Guide</a></body></html>`)
		case "/docs/guide.html":
			_, _ = io.WriteString(w, `<html><head><title>Guide</title></head><body>Configure retries with max_retries.</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(docs.Close)
	u, err := url.Parse(docs.URL)
	require.NoError(t, err)

	s, _ := newTestServer(t, "docs", u.Hostname())
	siteCfg := s.cfg.AppConfig.Sites["docs"]
	siteCfg.StartURLs = []string{docs.URL + "/docs/index.html"}
	siteCfg.AllowedPathPrefix = "/docs/"
	siteCfg.ContentSelector = "body"
	app := s.cfg.AppConfig
	app.StateDir = t.TempDir()
	app.NumWorkers = 2
	app.MaxRequests = 4
	app.MaxRequestsPerHost = 2
	app.MaxRetries = 1
	app.DefaultUserAgent = "test-agent"
	app.HTTPClientSettings = config.HTTPClientConfig{Timeout: 10 * time.Second, AllowPrivateNetworks: true}
	attachIndex(t, s)
	return s, siteCfg
}

func TestRunCrawlJob_Completes(t *testing.T) {
	s, siteCfg := crawlJobServer(t)
	job, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)

	s.runCrawlJob(job, siteCfg, "docs")

	got := s.jobManager.GetJob(job.ID)
	require.Equal(t, JobStatusCompleted, got.Status, "error: %s", got.ErrorMessage)
	assert.EqualValues(t, 2, got.PagesProcessed, "progress callback should report both pages")

	jsonl, err := os.ReadFile(filepath.Join(s.cfg.AppConfig.SiteOutputDir("docs"), "pages.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(jsonl), "Configure retries")

	latest, err := s.idx.GetLatestCrawl(context.Background(), "docs")
	require.NoError(t, err)
	require.NotNil(t, latest, "the job should record the crawl in the history index")
	assert.Equal(t, 2, latest.TotalPages)
}

func TestRunCrawlJob_FailsWithoutValidStartURLs(t *testing.T) {
	s, siteCfg := crawlJobServer(t)
	siteCfg.StartURLs = []string{"https://elsewhere.invalid/"}
	job, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)

	s.runCrawlJob(job, siteCfg, "docs")

	got := s.jobManager.GetJob(job.ID)
	assert.Equal(t, JobStatusFailed, got.Status)
	assert.NotEmpty(t, got.ErrorMessage)
}

func TestRunCrawlJob_CancelledJobEndsCancelled(t *testing.T) {
	s, siteCfg := crawlJobServer(t)
	job, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)
	require.True(t, s.jobManager.CancelJob(job.ID))

	s.runCrawlJob(job, siteCfg, "docs")

	assert.Equal(t, JobStatusCancelled, s.jobManager.GetJob(job.ID).Status)
}

func TestRunCrawlJob_IncrementalJobCrawlsIncrementally(t *testing.T) {
	s, siteCfg := crawlJobServer(t)
	for _, incremental := range []bool{false, true} {
		job, err := s.jobManager.CreateJob("docs", incremental)
		require.NoError(t, err)
		s.runCrawlJob(job, siteCfg, "docs")
		got := s.jobManager.GetJob(job.ID)
		require.Equal(t, JobStatusCompleted, got.Status, "error: %s", got.ErrorMessage)
	}

	latest, err := s.idx.GetLatestCrawl(context.Background(), "docs")
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, index.ModeIncremental, latest.Mode)
}
