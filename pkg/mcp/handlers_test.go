package mcp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

// silentTestLogger returns a *slog.Logger that discards output (tests should
// not produce stderr noise).
func silentTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeJSONLRecords writes one JSON object per line to path. Each value is
// JSON-marshalable.
func writeJSONLRecords(t *testing.T, path string, records []interface{}) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	for _, r := range records {
		b, err := json.Marshal(r)
		require.NoError(t, err)
		_, err = f.Write(append(b, '\n'))
		require.NoError(t, err)
	}
}

// newTestServer builds a Server with a single in-memory site mapped to a temp
// output directory. The JSONL output filename is set to "pages.jsonl".
func newTestServer(t *testing.T, siteKey, allowedDomain string) (*Server, string) {
	t.Helper()
	outDir := t.TempDir()
	siteCfg := &config.SiteConfig{
		AllowedDomain:     allowedDomain,
		AllowedPathPrefix: "/",
	}
	appCfg := &config.AppConfig{
		OutputBaseDir:       outDir,
		JSONLOutputFilename: "pages.jsonl",
		Sites:               map[string]*config.SiteConfig{siteKey: siteCfg},
	}
	s := &Server{
		cfg: &ServerConfig{
			AppConfig: appCfg,
			Logger:    silentTestLogger(),
		},
		log:        silentTestLogger(),
		jobManager: NewJobManager("", nil),
	}
	return s, filepath.Join(outDir, siteKey, "pages.jsonl")
}

// callListPages invokes handleListPages with the given arguments and parses the
// JSON response into a map for assertions.
func callListPages(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleListPages(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok, "expected TextContent")
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &got))
	return got
}

func TestHandleListPages_HappyPath(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/b", Title: "B", Depth: 1, CrawledAt: "2026-05-23T10:00:00Z", Content: "content B"},
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/a", Title: "A", Depth: 0, CrawledAt: "2026-05-23T10:00:01Z", Content: "content A"},
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/c", Title: "C", Depth: 2, CrawledAt: "2026-05-23T10:00:02Z", Content: "content C"},
		models.CrawlMetaJSONL{RecordType: models.RecordTypeCrawlMeta, SiteKey: "docs", TotalPages: 3},
	})

	got := callListPages(t, s, map[string]any{"site_key": "docs"})

	assert.Equal(t, "docs", got["site_key"])
	assert.EqualValues(t, 3, got["total"], "crawl_meta record must not be counted")
	assert.EqualValues(t, 0, got["offset"])
	assert.EqualValues(t, 3, got["returned"])

	pages, ok := got["pages"].([]any)
	require.True(t, ok)
	require.Len(t, pages, 3)
	// Sorted by URL ascending.
	urls := []string{
		pages[0].(map[string]any)["url"].(string),
		pages[1].(map[string]any)["url"].(string),
		pages[2].(map[string]any)["url"].(string),
	}
	assert.Equal(t, []string{
		"https://docs.example.com/a",
		"https://docs.example.com/b",
		"https://docs.example.com/c",
	}, urls)
	// Metadata shape includes content_length but not content body.
	first := pages[0].(map[string]any)
	assert.Equal(t, "A", first["title"])
	assert.EqualValues(t, 9, first["content_length"], "content_length should be len(\"content A\")")
	assert.NotContains(t, first, "content")
}

func TestHandleListPages_Pagination(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	letters := []string{"a", "b", "c", "d", "e"}
	records := make([]interface{}, 0, len(letters))
	for _, c := range letters {
		records = append(records, models.PageJSONL{
			RecordType: models.RecordTypePage,
			URL:        "https://docs.example.com/" + c,
			Title:      c,
		})
	}
	writeJSONLRecords(t, jsonlPath, records)

	got := callListPages(t, s, map[string]any{
		"site_key":    "docs",
		"max_results": float64(2),
		"offset":      float64(2),
	})

	assert.EqualValues(t, 5, got["total"])
	assert.EqualValues(t, 2, got["offset"])
	assert.EqualValues(t, 2, got["returned"])

	pages, ok := got["pages"].([]any)
	require.True(t, ok)
	require.Len(t, pages, 2)
	assert.Equal(t, "https://docs.example.com/c", pages[0].(map[string]any)["url"])
	assert.Equal(t, "https://docs.example.com/d", pages[1].(map[string]any)["url"])
}

func TestHandleListPages_OffsetBeyondTotalReturnsEmpty(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/a"},
	})

	got := callListPages(t, s, map[string]any{
		"site_key": "docs",
		"offset":   float64(100),
	})

	assert.EqualValues(t, 1, got["total"])
	assert.EqualValues(t, 0, got["returned"])
	pages, ok := got["pages"].([]any)
	require.True(t, ok)
	assert.Empty(t, pages)
}

func TestHandleListPages_MissingSiteKey(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	result, err := s.handleListPages(context.Background(), req)
	require.NoError(t, err)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	assert.Contains(t, tc.Text, "site_key parameter is required")
}

func TestHandleListPages_UnknownSite(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"site_key": "ghost"}
	result, err := s.handleListPages(context.Background(), req)
	require.NoError(t, err)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	assert.Contains(t, tc.Text, "site 'ghost' not found")
	// Helpful: should list available sites alphabetically.
	assert.Contains(t, tc.Text, "docs")
}

func TestHandleListPages_NoCrawlYet(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	// JSONL file deliberately not written.

	got := callListPages(t, s, map[string]any{"site_key": "docs"})

	assert.EqualValues(t, 0, got["total"])
	assert.EqualValues(t, 0, got["returned"])
	pages, ok := got["pages"].([]any)
	require.True(t, ok)
	assert.Empty(t, pages)
	assert.Contains(t, got["message"], "No crawl output found")
}

func TestHandleDescribeServer_BasicShape(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	result, err := s.handleDescribeServer(context.Background(), req)
	require.NoError(t, err)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &got))

	server, ok := got["server"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, serverName, server["name"])
	assert.Equal(t, serverVersion, server["version"])

	sites, ok := got["sites"].([]any)
	require.True(t, ok)
	require.Len(t, sites, 1)
	assert.Equal(t, "docs", sites[0].(map[string]any)["key"])
	assert.Equal(t, "docs.example.com", sites[0].(map[string]any)["domain"])

	jobs, ok := got["recent_jobs"].([]any)
	require.True(t, ok)
	assert.Empty(t, jobs, "no jobs run yet")

	assert.EqualValues(t, 1, got["total_sites"])
	assert.EqualValues(t, 0, got["total_jobs"])
	assert.Equal(t, false, got["jobs_capped"])
	assert.NotEmpty(t, got["next_actions"])
}

func TestHandleDescribeServer_IncludesRecentJobsNewestFirst(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	// Seed several jobs into the in-memory JobManager. CreateJob uses
	// time.Now() at insertion, so insertion order == StartedAt order. We
	// expect the response to reverse that (newest first).
	for _, k := range []string{"alpha", "beta", "gamma"} {
		s.cfg.AppConfig.Sites[k] = &config.SiteConfig{AllowedDomain: k + ".example.com"}
		_, err := s.jobManager.CreateJob(k, false)
		require.NoError(t, err)
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	result, err := s.handleDescribeServer(context.Background(), req)
	require.NoError(t, err)
	tc := result.Content[0].(mcpgo.TextContent)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &got))

	jobs := got["recent_jobs"].([]any)
	require.Len(t, jobs, 3)
	// Newest first: gamma was created last.
	assert.Equal(t, "gamma", jobs[0].(map[string]any)["site_key"])
	assert.Equal(t, "beta", jobs[1].(map[string]any)["site_key"])
	assert.Equal(t, "alpha", jobs[2].(map[string]any)["site_key"])
}

func callCancelCrawl(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleCancelCrawl(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &got))
	return got
}

func TestHandleCancelCrawl_MissingJobID(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	result, err := s.handleCancelCrawl(context.Background(), req)
	require.NoError(t, err)
	tc := result.Content[0].(mcpgo.TextContent)
	assert.Contains(t, tc.Text, "job_id parameter is required")
}

func TestHandleCancelCrawl_UnknownJob(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"job_id": "nope"}
	result, err := s.handleCancelCrawl(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
	tc := result.Content[0].(mcpgo.TextContent)
	assert.Contains(t, tc.Text, "job 'nope' not found")
}

func TestHandleCancelCrawl_RunningJobCancelled(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	job, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)
	s.jobManager.UpdateStatus(job.ID, JobStatusRunning, "")

	got := callCancelCrawl(t, s, map[string]any{"job_id": job.ID})
	assert.Equal(t, true, got["cancelled"])
	assert.Equal(t, string(JobStatusCancelled), got["status"])
	assert.Equal(t, "docs", got["site_key"])

	// Verify state at the source: the job is now Cancelled in JobManager.
	stored := s.jobManager.GetJob(job.ID)
	require.NotNil(t, stored)
	assert.Equal(t, JobStatusCancelled, stored.Status)
}

func TestHandleCancelCrawl_TerminalJobNotCancellable(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	job, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)
	s.jobManager.UpdateStatus(job.ID, JobStatusCompleted, "")

	got := callCancelCrawl(t, s, map[string]any{"job_id": job.ID})
	assert.Equal(t, false, got["cancelled"])
	assert.Equal(t, string(JobStatusCompleted), got["status"])
	assert.Contains(t, got["message"], "terminal state")
}

func TestHandleListPages_ClampsMaxResults(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/a"},
	})

	// Negative max_results falls back to default (100).
	got := callListPages(t, s, map[string]any{
		"site_key":    "docs",
		"max_results": float64(-5),
	})
	assert.EqualValues(t, 100, got["max_results"])

	// max_results above 1000 is clamped to 1000.
	got = callListPages(t, s, map[string]any{
		"site_key":    "docs",
		"max_results": float64(50000),
	})
	assert.EqualValues(t, 1000, got["max_results"])
}

// --- get_freshness / diff_crawl tests ---

// attachIndex opens a fresh on-disk SQLite index in a temp dir and attaches it
// to s. Returns the index for direct seeding by tests.
func attachIndex(t *testing.T, s *Server) *index.Index {
	t.Helper()
	idx, err := index.Open(filepath.Join(t.TempDir(), "index.db"), 5, silentTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	s.idx = idx
	return idx
}

func callJSON(t *testing.T, fn func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error), args map[string]any) (*mcpgo.CallToolResult, map[string]any) {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = args
	result, err := fn(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok, "expected TextContent")
	if result.IsError {
		return result, nil
	}
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &got))
	return result, got
}

func TestHandleGetFreshness_NeverCrawled(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	attachIndex(t, s)
	_, got := callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "docs"})
	require.NotNil(t, got)
	assert.Equal(t, "docs", got["site_key"])
	assert.NotContains(t, got, "last_crawl", "expected no last_crawl for never-crawled site")
	assert.Contains(t, got["next_actions"], "No prior crawl recorded")
}

func TestHandleGetFreshness_AfterCrawl(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)
	started := time.Now().Add(-2 * time.Minute)
	ended := time.Now().Add(-time.Minute)
	require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
		SiteKey:        "docs",
		CrawlStartedAt: started,
		CrawlEndedAt:   ended,
		Mode:           index.ModeFull,
		Pages: []index.PageRecord{
			{URL: "https://docs.example.com/a", Title: "A", ContentHash: "h1"},
		},
	}))
	_, got := callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "docs"})
	require.NotNil(t, got)
	last, ok := got["last_crawl"].(map[string]any)
	require.True(t, ok, "last_crawl: %v", got["last_crawl"])
	assert.InDelta(t, float64(1), last["total_pages"], 0)
	assert.Equal(t, "full", last["mode"])
	age, ok := got["age_seconds"].(float64)
	require.True(t, ok)
	assert.GreaterOrEqual(t, age, 0.0)
	assert.Less(t, age, 600.0, "age should be on the order of seconds, not 10+ min")
	assert.Contains(t, got["next_actions"], "diff_crawl")
}

func TestHandleGetFreshness_UnknownSite(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	attachIndex(t, s)
	result, _ := callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "ghost"})
	require.True(t, result.IsError)
}

func TestHandleDiffCrawl_RequiresSiteKey(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	attachIndex(t, s)
	r, _ := callJSON(t, s.handleDiffCrawl, map[string]any{})
	assert.True(t, r.IsError)
}

// Without since, the latest crawl is compared with the one before it, which is
// what an agent wants right after an incremental crawl.
func TestHandleDiffCrawl_WithoutSinceUsesPreviousCrawl(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for i, hash := range []string{"h0", "h1", "h2"} {
		require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
			SiteKey:        "docs",
			CrawlStartedAt: base.Add(time.Duration(i) * time.Hour),
			CrawlEndedAt:   base.Add(time.Duration(i)*time.Hour + time.Minute),
			Mode:           index.ModeIncremental,
			Pages:          []index.PageRecord{{URL: "https://docs.example.com/a", Title: "A", ContentHash: hash}},
		}))
	}

	r, got := callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs"})
	require.False(t, r.IsError, "%v", r.Content)
	baseline, ok := got["baseline_crawl"].(map[string]any)
	require.True(t, ok, "expected a baseline crawl, got %v", got)
	assert.EqualValues(t, 2, baseline["crawl_id"])
	changed := got["changed"].([]any)
	require.Len(t, changed, 1)
	assert.Equal(t, "h1", changed[0].(map[string]any)["prior_hash"])
	assert.NotContains(t, got, "note")
}

func TestHandleDiffCrawl_WithoutSinceAfterOneCrawl(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)
	end := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
		SiteKey: "docs", CrawlStartedAt: end.Add(-time.Minute), CrawlEndedAt: end, Mode: index.ModeFull,
		Pages: []index.PageRecord{{URL: "https://docs.example.com/a", ContentHash: "h1"}},
	}))

	_, got := callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs"})
	require.NotNil(t, got)
	assert.NotContains(t, got, "baseline_crawl")
	assert.Contains(t, got["note"], "Only one crawl")
}

// since at or after the latest crawl's end picks that crawl as its own
// baseline; the note must say so rather than claim since is newer.
func TestHandleDiffCrawl_SinceAtLatestCrawlExplainsEmptyDiff(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)
	end := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for i, hash := range []string{"h0", "h1"} {
		require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
			SiteKey:        "docs",
			CrawlStartedAt: end.Add(time.Duration(i-1)*time.Hour - time.Minute),
			CrawlEndedAt:   end.Add(time.Duration(i-1) * time.Hour),
			Mode:           index.ModeFull,
			Pages:          []index.PageRecord{{URL: "https://docs.example.com/a", ContentHash: hash}},
		}))
	}

	_, got := callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": end.Format(time.RFC3339)})
	require.NotNil(t, got)
	assert.EqualValues(t, 0, got["total"])
	assert.Contains(t, got["note"], "omit since")
	assert.NotContains(t, got["note"], "newer than")
}

func TestHandleDiffCrawl_InvalidSince(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	attachIndex(t, s)
	r, _ := callJSON(t, s.handleDiffCrawl, map[string]any{
		"site_key": "docs",
		"since":    "not-a-timestamp",
	})
	assert.True(t, r.IsError)
}

func TestHandleDiffCrawl_IndexDisabled(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	// no attachIndex → s.idx is nil
	r, _ := callJSON(t, s.handleDiffCrawl, map[string]any{
		"site_key": "docs",
		"since":    time.Now().Format(time.RFC3339),
	})
	assert.True(t, r.IsError)
}

func TestHandleDiffCrawl_NoBaseline(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)
	end := time.Now()
	require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
		SiteKey:        "docs",
		CrawlStartedAt: end.Add(-time.Minute),
		CrawlEndedAt:   end,
		Mode:           index.ModeFull,
		Pages:          []index.PageRecord{{URL: "https://docs.example.com/a", ContentHash: "h1"}},
	}))
	// since predates the only crawl → no baseline
	_, got := callJSON(t, s.handleDiffCrawl, map[string]any{
		"site_key": "docs",
		"since":    end.Add(-time.Hour).Format(time.RFC3339),
	})
	require.NotNil(t, got)
	assert.EqualValues(t, 0, got["total"])
	assert.NotContains(t, got, "baseline_crawl")
	assert.Contains(t, got, "current_crawl")
	assert.Contains(t, got["note"], "No baseline crawl")
}

// The documented workflow: read last_crawl.crawl_ended_at from get_freshness, crawl
// again, then diff with since set to that value. Freshness reports whole
// seconds, so the crawl it describes must still be selected as the baseline.
func TestHandleDiffCrawl_SinceFromFreshnessSelectsThatCrawl(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)
	ended := time.Date(2026, 1, 1, 10, 0, 0, 300_000_000, time.UTC)
	record := func(end time.Time, hash string) {
		require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
			SiteKey:        "docs",
			CrawlStartedAt: end.Add(-time.Minute),
			CrawlEndedAt:   end,
			Mode:           index.ModeFull,
			Pages:          []index.PageRecord{{URL: "https://docs.example.com/a", Title: "A", ContentHash: hash}},
		}))
	}
	record(ended.Add(-time.Hour), "h0")
	record(ended, "h1")

	_, fresh := callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "docs"})
	require.NotNil(t, fresh)
	last, ok := fresh["last_crawl"].(map[string]any)
	require.True(t, ok, "last_crawl: %v", fresh["last_crawl"])
	since, ok := last["crawl_ended_at"].(string)
	require.True(t, ok)

	record(ended.Add(time.Hour), "h2")

	_, got := callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": since})
	require.NotNil(t, got)
	baseline, ok := got["baseline_crawl"].(map[string]any)
	require.True(t, ok, "expected a baseline crawl, got %v", got)
	assert.EqualValues(t, 2, baseline["crawl_id"], "baseline should be the crawl get_freshness reported")
	assert.EqualValues(t, 1, got["total"])
}

func TestHandleDiffCrawl_AddedRemovedChanged(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	// baseline: A(v1), B(v1), C(v1)
	require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
		SiteKey:        "docs",
		CrawlStartedAt: base,
		CrawlEndedAt:   base.Add(time.Minute),
		Mode:           index.ModeFull,
		Pages: []index.PageRecord{
			{URL: "https://docs.example.com/a", Title: "A", ContentHash: "ha1"},
			{URL: "https://docs.example.com/b", Title: "B", ContentHash: "hb1"},
			{URL: "https://docs.example.com/c", Title: "C", ContentHash: "hc1"},
		},
	}))
	// current: A(v2 changed), C(unchanged), D(added); B removed
	require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
		SiteKey:        "docs",
		CrawlStartedAt: base.Add(time.Hour),
		CrawlEndedAt:   base.Add(time.Hour + time.Minute),
		Mode:           index.ModeIncremental,
		Pages: []index.PageRecord{
			{URL: "https://docs.example.com/a", Title: "A2", ContentHash: "ha2"},
			{URL: "https://docs.example.com/c", Title: "C", ContentHash: "hc1"},
			{URL: "https://docs.example.com/d", Title: "D", ContentHash: "hd1"},
		},
	}))
	_, got := callJSON(t, s.handleDiffCrawl, map[string]any{
		"site_key": "docs",
		"since":    base.Add(2 * time.Minute).Format(time.RFC3339),
	})
	require.NotNil(t, got)
	assert.EqualValues(t, 1, got["unchanged_count"])
	added := got["added"].([]any)
	removed := got["removed"].([]any)
	changed := got["changed"].([]any)
	require.Len(t, added, 1)
	require.Len(t, removed, 1)
	require.Len(t, changed, 1)
	assert.Equal(t, "https://docs.example.com/d", added[0].(map[string]any)["url"])
	assert.Equal(t, "https://docs.example.com/b", removed[0].(map[string]any)["url"])
	chgEntry := changed[0].(map[string]any)
	assert.Equal(t, "https://docs.example.com/a", chgEntry["url"])
	assert.Equal(t, "ha2", chgEntry["content_hash"])
	assert.Equal(t, "ha1", chgEntry["prior_hash"])
}

// callReadPage invokes handleReadPage and parses the JSON response.
func callReadPage(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleReadPage(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok, "expected TextContent")
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &got))
	return got
}

// readPageError invokes handleReadPage and returns the raw text, for the error
// paths that do not emit JSON.
func readPageError(t *testing.T, s *Server, args map[string]any) string {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleReadPage(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError, "expected an error result")
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	return tc.Text
}

func seedCorpus(t *testing.T, jsonlPath string) {
	t.Helper()
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{
			RecordType: models.RecordTypePage, URL: "https://docs.example.com/a", Title: "A",
			Depth: 0, CrawledAt: "2026-05-23T10:00:00Z", Content: "# A\n\nBody of A.",
			ContentHash: "hash-a", Headings: []string{"A"},
		},
		models.PageJSONL{
			RecordType: models.RecordTypePage, URL: "https://docs.example.com/b", Title: "B",
			Depth: 1, CrawledAt: "2026-05-23T10:00:01Z", Content: "# B\n\nBody of B.",
			ContentHash: "hash-b",
		},
		models.CrawlMetaJSONL{RecordType: models.RecordTypeCrawlMeta, SiteKey: "docs", TotalPages: 2},
	})
}

func TestHandleReadPage_ReturnsStoredContent(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	seedCorpus(t, jsonlPath)

	got := callReadPage(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/a"})

	assert.Equal(t, "# A\n\nBody of A.", got["content"])
	assert.Equal(t, "A", got["title"])
	assert.Equal(t, "hash-a", got["content_hash"])
	assert.Equal(t, "stored_crawl", got["source"])
	assert.EqualValues(t, 0, got["depth"])
	assert.Equal(t, "2026-05-23T10:00:00Z", got["crawled_at"])
	assert.Equal(t, false, got["truncated"])
	assert.NotContains(t, got, "next_offset")
}

// The crawl_meta footer must never be mistaken for a page record.
func TestHandleReadPage_IgnoresCrawlMetaRecord(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	seedCorpus(t, jsonlPath)

	got := callReadPage(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/b"})
	assert.Equal(t, "# B\n\nBody of B.", got["content"])
}

func TestHandleReadPage_NormalizesURL(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	seedCorpus(t, jsonlPath)

	// A fragment the caller carried over from a docs link must still resolve.
	got := callReadPage(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/a#section"})
	assert.Equal(t, "https://docs.example.com/a", got["url"])
	assert.Equal(t, "# A\n\nBody of A.", got["content"])
}

func TestHandleReadPage_TruncatesAndResumesByOffset(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	body := strings.Repeat("x", 250)
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/big", Content: body},
	})

	first := callReadPage(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/big", "max_bytes": 100})
	assert.Equal(t, true, first["truncated"])
	assert.EqualValues(t, 250, first["total_length"])
	assert.EqualValues(t, 100, first["content_length"])
	assert.EqualValues(t, 100, first["next_offset"])

	var reassembled strings.Builder
	reassembled.WriteString(first["content"].(string))
	offset := first["next_offset"]
	for {
		next := callReadPage(t, s, map[string]any{
			"site_key": "docs", "url": "https://docs.example.com/big",
			"max_bytes": 100, "offset": offset,
		})
		reassembled.WriteString(next["content"].(string))
		if next["truncated"] != true {
			break
		}
		offset = next["next_offset"]
	}
	assert.Equal(t, body, reassembled.String(), "paging through next_offset must reassemble the page exactly")
}

// Truncation must not split a multi-byte rune, or the JSON response carries
// invalid UTF-8 that the client cannot decode.
func TestHandleReadPage_TruncationKeepsValidUTF8(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	body := strings.Repeat("é", 100) // 2 bytes per rune
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/utf8", Content: body},
	})

	// An odd max_bytes lands mid-rune and must be snapped back.
	got := callReadPage(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/utf8", "max_bytes": 51})
	content := got["content"].(string)
	assert.True(t, utf8.ValidString(content), "truncated content must be valid UTF-8")
	assert.EqualValues(t, 50, got["content_length"], "cut should snap back to the rune boundary")

	rest := callReadPage(t, s, map[string]any{
		"site_key": "docs", "url": "https://docs.example.com/utf8",
		"max_bytes": 1000, "offset": got["next_offset"],
	})
	assert.Equal(t, body, content+rest["content"].(string))
}

// A max_bytes smaller than the multi-byte rune at offset must still return a
// non-empty chunk with a strictly increasing next_offset, so a client that
// follows next_offset terminates instead of looping forever.
func TestHandleReadPage_SmallMaxBytesMakesProgress(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	body := strings.Repeat("\u2019", 20) // U+2019 RIGHT SINGLE QUOTATION MARK: 3 bytes per rune
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/quote", Content: body},
	})

	for _, maxBytes := range []int{1, 2} {
		args := map[string]any{
			"site_key": "docs", "url": "https://docs.example.com/quote", "max_bytes": maxBytes,
		}
		var reassembled strings.Builder
		prevOffset := 0
		truncated := true
		for i := 0; truncated && i < 100; i++ {
			got := callReadPage(t, s, args)
			content := got["content"].(string)
			assert.True(t, utf8.ValidString(content), "max_bytes=%d: chunk must be valid UTF-8", maxBytes)
			assert.NotEmpty(t, content, "max_bytes=%d: a truncated chunk must make progress", maxBytes)
			reassembled.WriteString(content)

			truncated = got["truncated"] == true
			if !truncated {
				break
			}
			next, ok := got["next_offset"]
			require.True(t, ok, "max_bytes=%d: truncated response must carry next_offset", maxBytes)
			assert.Greater(t, int(next.(float64)), prevOffset, "max_bytes=%d: next_offset must strictly advance", maxBytes)
			prevOffset = int(next.(float64))
			args["offset"] = next
		}
		assert.False(t, truncated, "max_bytes=%d: paging must terminate", maxBytes)
		assert.Equal(t, body, reassembled.String(), "max_bytes=%d: following next_offset must reassemble the page", maxBytes)
	}
}

// A search result's anchor reads just that section, paged within it.
func TestHandleReadPage_Anchor(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	body := "# Guide\nintro\n## Install\n" + strings.Repeat("step ", 30) + "\n### Linux\napt\n## Usage\nrun it\n"
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/g", Content: body},
	})
	install := "## Install\n" + strings.Repeat("step ", 30) + "\n### Linux\napt\n"
	installAt := strings.Index(body, "## Install")

	got := callReadPage(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/g", "anchor": "install"})
	assert.Equal(t, install, got["content"])
	assert.EqualValues(t, installAt, got["offset"])
	assert.EqualValues(t, installAt+len(install), got["section_end"])
	assert.Equal(t, false, got["truncated"])
	assert.EqualValues(t, len(body), got["total_length"])

	var rebuilt strings.Builder
	args := map[string]any{"site_key": "docs", "url": "https://docs.example.com/g", "anchor": "install", "max_bytes": 40}
	for range 20 {
		part := callReadPage(t, s, args)
		rebuilt.WriteString(part["content"].(string))
		next, ok := part["next_offset"]
		if !ok {
			break
		}
		args["offset"] = next
	}
	assert.Equal(t, install, rebuilt.String(), "following next_offset stays within the section")

	r, _ := callJSON(t, s.handleReadPage, map[string]any{"site_key": "docs", "url": "https://docs.example.com/g", "anchor": "nope"})
	msg := errText(t, r)
	assert.Contains(t, msg, `anchor "nope"`)
	assert.Contains(t, msg, "usage", "the error lists the page's anchors")
}

func TestHandleReadPage_UnknownURL(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	seedCorpus(t, jsonlPath)

	text := readPageError(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/missing"})
	assert.Contains(t, text, "not in the stored crawl")
	assert.Contains(t, text, "list_pages")
}

func TestHandleReadPage_NoCrawlYet(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")

	text := readPageError(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/a"})
	assert.Contains(t, text, "no crawl output found")
	assert.Contains(t, text, "crawl_site")
}

func TestHandleReadPage_MissingParams(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")

	assert.Contains(t, readPageError(t, s, map[string]any{"url": "https://docs.example.com/a"}), "site_key parameter is required")
	assert.Contains(t, readPageError(t, s, map[string]any{"site_key": "docs"}), "url parameter is required")
	assert.Contains(t, readPageError(t, s, map[string]any{"site_key": "ghost", "url": "https://x/"}), "site 'ghost' not found")
}

// JSONL disabled is a distinct condition from "never crawled": the crawl may
// have succeeded and written markdown only.
func TestHandleReadPage_JSONLDisabledIsReportedDistinctly(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	s.cfg.AppConfig.EnableJSONLOutput = new(false)

	text := readPageError(t, s, map[string]any{"site_key": "docs", "url": "https://docs.example.com/a"})
	assert.Contains(t, text, "JSONL output is disabled")
	assert.Contains(t, text, "enable_jsonl_output")

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"site_key": "docs"}
	result, err := s.handleListPages(context.Background(), req)
	require.NoError(t, err)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	assert.Contains(t, tc.Text, "JSONL output is disabled")
}

// The index-backed tools must name the opt-out rather than imply the site was
// never crawled, since a crawl with JSONL off is never indexed.
func TestIndexToolsExplainJSONLDisabled(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	s.cfg.AppConfig.Sites["docs"].EnableJSONLOutput = new(false)
	attachIndex(t, s)

	_, got := callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "docs"})
	assert.Contains(t, got["next_actions"], "enable_jsonl_output is false")

	_, got = callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": "2026-01-01T00:00:00Z"})
	assert.Contains(t, got["note"], "enable_jsonl_output is false")

	got, _ = callSearchDocs(t, s, map[string]any{"query": "anything", "site_key": "docs"})
	assert.Contains(t, got["next_actions"], "enable_jsonl_output is false")

	got, _ = callSearchDocs(t, s, map[string]any{"query": "anything"})
	assert.Contains(t, got["next_actions"], "docs", "a search across all sites names the sites it skipped")
	assert.Contains(t, got["next_actions"], "enable_jsonl_output is false")
}

// wait_seconds lets an agent block until a crawl finishes instead of polling.
func TestHandleGetJobStatus_WaitSeconds(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	job, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)
	s.jobManager.UpdateStatus(job.ID, JobStatusRunning, "")

	started := time.Now()
	_, got := callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID, "wait_seconds": 1})
	assert.Equal(t, "running", got["status"], "a job still running when the wait ends is reported as running")
	assert.GreaterOrEqual(t, time.Since(started), time.Second)

	time.AfterFunc(200*time.Millisecond, func() { s.jobManager.UpdateStatus(job.ID, JobStatusCompleted, "") })
	started = time.Now()
	_, got = callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID, "wait_seconds": 30})
	assert.Equal(t, "completed", got["status"])
	assert.Less(t, time.Since(started), 5*time.Second, "returns as soon as the job finishes")

	started = time.Now()
	_, got = callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID})
	assert.Equal(t, "completed", got["status"])
	assert.Less(t, time.Since(started), time.Second, "no wait by default")
}

// A huge argument must not come back whole in the error text.
func TestHandlers_ClipEchoedInput(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	seedCorpus(t, jsonlPath)
	idx, err := index.Open(filepath.Join(t.TempDir(), "index.db"), 5, silentTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	s.idx = idx
	s.cfg.AppConfig.HTTPClientSettings = config.HTTPClientConfig{Timeout: 5 * time.Second}
	huge := strings.Repeat("A", 1<<20)

	cases := []struct {
		name string
		fn   func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
		args map[string]any
	}{
		{"list_pages site_key", s.handleListPages, map[string]any{"site_key": huge}},
		{"get_freshness site_key", s.handleGetFreshness, map[string]any{"site_key": huge}},
		{"crawl_site site_key", s.handleCrawlSite, map[string]any{"site_key": huge}},
		{"get_job_status job_id", s.handleGetJobStatus, map[string]any{"job_id": huge}},
		{"cancel_crawl job_id", s.handleCancelCrawl, map[string]any{"job_id": huge}},
		{"diff_crawl since", s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": huge}},
		{"read_page url", s.handleReadPage, map[string]any{"site_key": "docs", "url": "https://docs.example.com/" + huge}},
		{"read_page anchor", s.handleReadPage, map[string]any{"site_key": "docs", "url": "https://docs.example.com/a", "anchor": huge}},
		{"get_page url", s.handleGetPage, map[string]any{"url": "https://" + huge + ".invalid/"}},
		{"get_page selector", s.handleGetPage, map[string]any{"url": "https://docs.example.com/", "content_selector": "[" + huge}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcpgo.CallToolRequest{}
			req.Params.Arguments = tc.args
			result, err := tc.fn(context.Background(), req)
			require.NoError(t, err)
			require.True(t, result.IsError)
			text := result.Content[0].(mcpgo.TextContent).Text
			assert.Less(t, len(text), 4096, "error text starts %q", text[:min(len(text), 200)])
			assert.Contains(t, text, "bytes")
		})
	}
}

func TestClipInput_KeepsBothEndsOnRuneBoundaries(t *testing.T) {
	assert.Equal(t, "short", clipInput("short"))

	v := "x" + strings.Repeat("é", 300) + ": no such host."
	got := clipInput(v)
	assert.True(t, utf8.ValidString(got))
	assert.Less(t, len(got), 260)
	assert.Contains(t, got, "[616 bytes]")
	assert.True(t, strings.HasPrefix(got, "xé"))
	assert.True(t, strings.HasSuffix(got, ": no such host."))
}
