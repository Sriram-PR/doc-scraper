package mcp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"log/slog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
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
		EnableJSONLOutput:   true,
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
	return s, filepath.Join(outDir, allowedDomain, "pages.jsonl")
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
