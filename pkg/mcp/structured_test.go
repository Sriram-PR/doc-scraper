package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/version"
)

// registeredOutputSchema reads the schema from a fully registered server, so
// tests check handlers against what clients are actually told.
func registeredOutputSchema(t *testing.T, tool string) json.RawMessage {
	t.Helper()
	st := newStdioTestServer(t).mcpServer.GetTool(tool)
	require.NotNil(t, st, "tool %s is not registered", tool)
	require.NotEmpty(t, st.Tool.RawOutputSchema, "tool %s declares no outputSchema", tool)
	return st.Tool.RawOutputSchema
}

func requireConforms(t *testing.T, schema json.RawMessage, result *mcpgo.CallToolResult) map[string]any {
	t.Helper()
	require.False(t, result.IsError, "unexpected error result: %v", result.Content)
	require.NotNil(t, result.StructuredContent)
	structured, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok, "expected TextContent")
	require.JSONEq(t, string(structured), tc.Text, "text block must carry the structured result")

	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	require.NoError(t, err)
	require.NoError(t, c.AddResource("output.json", doc))
	sch, err := c.Compile("output.json")
	require.NoError(t, err)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(structured))
	require.NoError(t, err)
	require.NoError(t, sch.Validate(inst), "result does not match outputSchema: %s", structured)

	var got map[string]any
	require.NoError(t, json.Unmarshal(structured, &got))
	return got
}

func requireToolResultConforms(t *testing.T, tool string, result *mcpgo.CallToolResult) map[string]any {
	t.Helper()
	return requireConforms(t, registeredOutputSchema(t, tool), result)
}

func TestHandleListSites_StructuredOutput(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	result, _ := callJSON(t, s.handleListSites, nil)
	got := requireToolResultConforms(t, "list_sites", result)

	sites := got["sites"].([]any)
	require.Len(t, sites, 1)
	site := sites[0].(map[string]any)
	assert.Equal(t, "docs", site["key"])
	assert.Equal(t, "docs.example.com", site["domain"])
	assert.EqualValues(t, 0, site["max_depth"], "zero values must not be dropped")
	assert.EqualValues(t, 0, site["start_urls_count"])
	assert.NotContains(t, site, "last_crawled")
	assert.NotContains(t, site, "status")
	assert.EqualValues(t, 1, got["total_sites"])
	assert.Contains(t, got, "config_path")
}

func TestHandleGetPage_StructuredOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>T</title></head><body><p>BODY</p></body></html>`)
	}))
	t.Cleanup(srv.Close)
	s, _ := newTestServer(t, "docs", "docs.example.com")
	s.cfg.AppConfig.HTTPClientSettings = config.HTTPClientConfig{Timeout: 10 * time.Second, AllowPrivateNetworks: true}

	result, _ := callJSON(t, s.handleGetPage, map[string]any{"url": srv.URL + "/p"})
	got := requireToolResultConforms(t, "get_page", result)
	assert.Equal(t, "T", got["title"])
	assert.Contains(t, got["content"], "BODY")
	assert.Equal(t, srv.URL+"/p", got["url"])
	assert.Contains(t, got, "fetch_time_ms")
}

func TestHandleGetPage_DefaultUserAgentCarriesVersion(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><body><p>BODY</p></body></html>`)
	}))
	t.Cleanup(srv.Close)
	s, _ := newTestServer(t, "docs", "docs.example.com")
	s.cfg.AppConfig.HTTPClientSettings = config.HTTPClientConfig{Timeout: 10 * time.Second, AllowPrivateNetworks: true}
	s.cfg.AppConfig.DefaultUserAgent = ""

	callJSON(t, s.handleGetPage, map[string]any{"url": srv.URL + "/p"})
	assert.Equal(t, "github.com/Sriram-PR/doc-scraper/"+version.Version, gotUA)
}

func TestHandleGetPage_ConvertsTablesAndTaskLists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>T</title></head><body>`+
			`<table><thead><tr><th>Name</th></tr></thead><tbody><tr><td>alpha</td></tr></tbody></table>`+
			`<ul><li><input type="checkbox" checked> done</li></ul></body></html>`)
	}))
	t.Cleanup(srv.Close)
	s, _ := newTestServer(t, "docs", "docs.example.com")
	s.cfg.AppConfig.HTTPClientSettings = config.HTTPClientConfig{Timeout: 10 * time.Second, AllowPrivateNetworks: true}

	result, _ := callJSON(t, s.handleGetPage, map[string]any{"url": srv.URL + "/p"})
	got := requireToolResultConforms(t, "get_page", result)
	assert.Contains(t, got["content"], "| alpha")
	assert.Contains(t, got["content"], "- [x]  done")
	assert.NotContains(t, got["content"], "<!--")
}

// A large page must not come back whole: get_page pages its Markdown like
// read_page, and following next_offset rebuilds the full content.
func TestHandleGetPage_PaginatesLargePages(t *testing.T) {
	var body strings.Builder
	body.WriteString("<html><head><title>Big</title></head><body>")
	for i := range 8000 {
		fmt.Fprintf(&body, "<p>paragraph %d héllo</p>", i)
	}
	body.WriteString("</body></html>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body.String())
	}))
	t.Cleanup(srv.Close)
	s, _ := newTestServer(t, "docs", "docs.example.com")
	s.cfg.AppConfig.HTTPClientSettings = config.HTTPClientConfig{Timeout: 10 * time.Second, AllowPrivateNetworks: true}

	result, first := callJSON(t, s.handleGetPage, map[string]any{"url": srv.URL + "/big"})
	requireToolResultConforms(t, "get_page", result)
	n := len(first["content"].(string))
	assert.True(t, n <= 102400 && n > 102390, "default page is about 100 KiB, got %d bytes", n)
	assert.Equal(t, true, first["truncated"])
	total := int(first["total_length"].(float64))
	require.Greater(t, total, 102400)

	var rebuilt strings.Builder
	args := map[string]any{"url": srv.URL + "/big", "max_bytes": 50000}
	for range 20 {
		_, got := callJSON(t, s.handleGetPage, args)
		require.NotNil(t, got)
		rebuilt.WriteString(got["content"].(string))
		next, ok := got["next_offset"]
		if !ok {
			assert.Equal(t, false, got["truncated"])
			break
		}
		args["offset"] = next
	}
	assert.Equal(t, total, rebuilt.Len())
	assert.True(t, strings.HasPrefix(rebuilt.String(), "paragraph 0 héllo"))
	assert.True(t, strings.HasSuffix(rebuilt.String(), "paragraph 7999 héllo"))
}

// get_page converts HTML; anything else must come back as a clear error rather
// than as binary or escaped text dressed up as Markdown.
func TestHandleGetPage_ContentType(t *testing.T) {
	for _, tc := range []struct {
		contentType string
		body        string
		ok          bool
	}{
		{"application/xhtml+xml", `<html><head><title>T</title></head><body><p>BODY</p></body></html>`, true},
		{"", `<!DOCTYPE html><html><head><title>T</title></head><body><p>BODY</p></body></html>`, true},
		{"application/pdf", "%PDF-1.7 BODY", false},
		{"application/json", `{"BODY": 1}`, false},
		{"", "%PDF-1.7 BODY", false},
	} {
		t.Run(tc.contentType+"/"+tc.body[:4], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = nil
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			s, _ := newTestServer(t, "docs", "docs.example.com")
			s.cfg.AppConfig.HTTPClientSettings = config.HTTPClientConfig{Timeout: 10 * time.Second, AllowPrivateNetworks: true}

			result, got := callJSON(t, s.handleGetPage, map[string]any{"url": srv.URL + "/p"})
			if tc.ok {
				require.False(t, result.IsError, "%v", result.Content)
				assert.Contains(t, got["content"], "BODY")
				return
			}
			assert.Contains(t, errText(t, result), "not an HTML page")
		})
	}
}

func TestHandleCancelCrawl_StructuredOutput(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	job, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)

	first, _ := callJSON(t, s.handleCancelCrawl, map[string]any{"job_id": job.ID})
	got := requireToolResultConforms(t, "cancel_crawl", first)
	assert.Equal(t, true, got["cancelled"])

	again, _ := callJSON(t, s.handleCancelCrawl, map[string]any{"job_id": job.ID})
	got = requireToolResultConforms(t, "cancel_crawl", again)
	assert.Equal(t, false, got["cancelled"], "cancelled:false must not be dropped")
	assert.Equal(t, string(JobStatusCancelled), got["status"])
	assert.Contains(t, got["message"], "terminal state")
}

func TestHandleSearchDocs_StructuredOutput(t *testing.T) {
	s := newTestServerWithIndex(t, "docs")
	md := "# Retries\n\nRetry limits are configured with max_retries. " + strings.Repeat("filler ", 40)
	require.NoError(t, s.idx.ReplaceChunks(context.Background(),
		"docs", "https://docs.example.com/retries", "Retries", "h1", chunk.Split(md)))

	_, hit := callSearchDocs(t, s, map[string]any{"query": "retry limits", "site_key": "docs"})
	got := requireToolResultConforms(t, "search_docs", hit)
	assert.EqualValues(t, 1, got["count"])
	assert.Equal(t, "docs", got["site_key"])
	results, ok := got["results"].([]any)
	require.True(t, ok)
	require.Len(t, results, 1)
	assert.Equal(t, "all", results[0].(map[string]any)["match"])

	_, miss := callSearchDocs(t, s, map[string]any{"query": "zebra"})
	got = requireToolResultConforms(t, "search_docs", miss)
	assert.EqualValues(t, 0, got["count"])
	assert.Equal(t, []any{}, got["results"])
	assert.NotContains(t, got, "site_key")
}

func TestHandleListPages_StructuredOutput(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")

	empty, _ := callJSON(t, s.handleListPages, map[string]any{"site_key": "docs"})
	got := requireToolResultConforms(t, "list_pages", empty)
	assert.Contains(t, got["message"], "Run crawl_site first")
	assert.Equal(t, []any{}, got["pages"])
	assert.EqualValues(t, 0, got["total"])

	seedCorpus(t, jsonlPath)
	full, _ := callJSON(t, s.handleListPages, map[string]any{"site_key": "docs"})
	got = requireToolResultConforms(t, "list_pages", full)
	assert.NotContains(t, got, "message")
	assert.NotEmpty(t, got["pages"])
	assert.EqualValues(t, 0, got["offset"], "zero values must not be dropped")
}

func TestHandleReadPage_StructuredOutput(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	seedCorpus(t, jsonlPath)

	whole, _ := callJSON(t, s.handleReadPage, map[string]any{"site_key": "docs", "url": "https://docs.example.com/a"})
	got := requireToolResultConforms(t, "read_page", whole)
	assert.Equal(t, false, got["truncated"], "truncated:false must not be dropped")
	assert.NotContains(t, got, "next_offset")
	assert.EqualValues(t, 0, got["offset"])
	assert.Equal(t, "stored_crawl", got["source"])
	assert.NotNil(t, got["headings"])

	part, _ := callJSON(t, s.handleReadPage, map[string]any{"site_key": "docs", "url": "https://docs.example.com/a", "max_bytes": 1.0})
	got = requireToolResultConforms(t, "read_page", part)
	assert.Equal(t, true, got["truncated"])
	assert.Contains(t, got, "next_offset")
}

func TestHandleGetJobStatus_StructuredOutput(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	job, err := s.jobManager.CreateJob("docs", true)
	require.NoError(t, err)

	pending, _ := callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID})
	got := requireToolResultConforms(t, "get_job_status", pending)
	assert.EqualValues(t, 0, got["pages_unchanged"], "zero values must not be dropped")
	assert.Equal(t, true, got["incremental"])
	assert.NotContains(t, got, "completed_at")
	assert.NotContains(t, got, "duration_seconds")

	s.jobManager.UpdateStatus(job.ID, JobStatusFailed, "boom")
	done, _ := callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID})
	got = requireToolResultConforms(t, "get_job_status", done)
	assert.Contains(t, got, "completed_at")
	assert.Contains(t, got, "duration_seconds")
	assert.Equal(t, "boom", got["error_message"])
}

func TestHandleDescribeServer_StructuredOutput(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	_, err := s.jobManager.CreateJob("docs", true)
	require.NoError(t, err)

	result, _ := callJSON(t, s.handleDescribeServer, nil)
	got := requireToolResultConforms(t, "describe_server", result)
	jobs := got["recent_jobs"].([]any)
	require.Len(t, jobs, 1)
	j := jobs[0].(map[string]any)
	assert.EqualValues(t, 0, j["pages_queued"], "recent_jobs now carry the full job summary")
	assert.Equal(t, true, j["incremental"])
	assert.Equal(t, false, got["jobs_capped"])
}

func TestHandleCrawlSite_StructuredOutput(t *testing.T) {
	// Temp state/output dirs and a local docs server: the started job really runs.
	s, _, _, _ := newCrawlJobServer(t, "docs")

	started, _ := callJSON(t, s.handleCrawlSite, map[string]any{"site_key": "docs"})
	got := requireToolResultConforms(t, "crawl_site", started)
	assert.Equal(t, "started", got["status"])
	assert.Equal(t, false, got["incremental"])
	jobID := got["job_id"].(string)
	require.Eventually(t, func() bool {
		j := s.jobManager.GetJob(jobID)
		return j != nil && isTerminalStatus(j.Status)
	}, 10*time.Second, 20*time.Millisecond)

	_, err := s.jobManager.CreateJob("docs", true)
	require.NoError(t, err)
	running, _ := callJSON(t, s.handleCrawlSite, map[string]any{"site_key": "docs"})
	got = requireToolResultConforms(t, "crawl_site", running)
	assert.Equal(t, "already_running", got["status"])
	assert.Equal(t, true, got["incremental"], "already_running reports the running job's mode")
}

func TestHandleGetFreshness_StructuredOutput(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)

	never, _ := callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "docs"})
	got := requireToolResultConforms(t, "get_freshness", never)
	assert.NotContains(t, got, "last_crawl")
	assert.NotContains(t, got, "age_seconds")
	assert.Equal(t, false, got["output_exists"], "false must not be dropped")

	ended := time.Now().Add(-time.Minute)
	require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
		SiteKey: "docs", CrawlStartedAt: ended.Add(-time.Minute), CrawlEndedAt: ended, Mode: index.ModeFull,
		Pages: []index.PageRecord{{URL: "https://docs.example.com/a", Title: "A", ContentHash: "h1"}},
	}))
	_, err := s.jobManager.CreateJob("docs", true)
	require.NoError(t, err)
	// A pending job counts as running for IsRunning, so running_job is reported.

	crawled, _ := callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "docs"})
	got = requireToolResultConforms(t, "get_freshness", crawled)
	last := got["last_crawl"].(map[string]any)
	endedAt, err := time.Parse(time.RFC3339, last["crawl_ended_at"].(string))
	require.NoError(t, err)
	assert.Equal(t, ended.Unix(), endedAt.Unix())
	assert.EqualValues(t, 1, last["total_pages"])
	assert.Equal(t, "full", last["mode"])
	assert.Contains(t, got, "age_seconds")
	for _, old := range []string{"last_crawl_id", "last_crawl_started_at", "last_crawl_ended_at", "last_crawl_total_pages", "last_crawl_mode"} {
		assert.NotContains(t, got, old)
	}
	running := got["running_job"].(map[string]any)
	assert.Equal(t, "docs", running["site_key"], "running_job is now a full job summary")
	assert.Contains(t, running, "status")
	assert.Contains(t, got["next_actions"], "diff_crawl without since")
}

func TestHandleDiffCrawl_StructuredOutput(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)

	none, _ := callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": time.Now().Format(time.RFC3339)})
	got := requireToolResultConforms(t, "diff_crawl", none)
	assert.NotContains(t, got, "current_crawl")
	assert.Contains(t, got["note"], "No crawl has been recorded")
	assert.Equal(t, []any{}, got["added"])

	record := func(end time.Time, hash string) {
		require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
			SiteKey: "docs", CrawlStartedAt: end.Add(-time.Minute), CrawlEndedAt: end, Mode: index.ModeFull,
			Pages: []index.PageRecord{{URL: "https://docs.example.com/a", Title: "A", ContentHash: hash}},
		}))
	}
	base := time.Now().Add(-time.Hour)
	record(base, "h0")
	record(base.Add(30*time.Minute), "h1")

	diff, _ := callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": base.Format(time.RFC3339)})
	got = requireToolResultConforms(t, "diff_crawl", diff)
	assert.Contains(t, got, "baseline_crawl")
	assert.Contains(t, got, "current_crawl")
	assert.NotContains(t, got, "note")
	changed := got["changed"].([]any)
	require.Len(t, changed, 1)
	assert.Equal(t, "h0", changed[0].(map[string]any)["prior_hash"])
	assert.EqualValues(t, 0, got["unchanged_count"])
}

func hasClosedObject(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if ap, ok := x["additionalProperties"].(bool); ok && !ap {
			return true
		}
		for _, e := range x {
			if hasClosedObject(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if hasClosedObject(e) {
				return true
			}
		}
	}
	return false
}

// Every tool must reach clients with an open, object-root outputSchema.
func TestToolsList_EveryToolDeclaresAnOpenOutputSchema(t *testing.T) {
	s := newStdioTestServer(t)
	res := handleRaw(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	tools := res["tools"].([]any)
	require.Len(t, tools, 12)
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"]
		schema, ok := tool["outputSchema"].(map[string]any)
		require.True(t, ok, "%s has no outputSchema", name)
		assert.Equal(t, "object", schema["type"], "%s outputSchema root", name)
		assert.False(t, hasClosedObject(schema), "%s outputSchema closes an object", name)
		assert.NotEmpty(t, schema["required"], "%s outputSchema has no required fields", name)
	}
}

// The handlers reject fractions for counts and offsets, so the input schema
// must say integer or clients will send 2.5 and get an error.
func TestToolsList_CountsAndOffsetsAreIntegers(t *testing.T) {
	s := newStdioTestServer(t)
	res := handleRaw(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	seen := 0
	for _, raw := range res["tools"].([]any) {
		tool := raw.(map[string]any)
		props, _ := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		for name, p := range props {
			switch name {
			case "limit", "max_results", "max_bytes", "offset":
				seen++
				assert.Equal(t, "integer", p.(map[string]any)["type"], "%s.%s", tool["name"], name)
			}
		}
	}
	assert.Equal(t, 9, seen)
}

func TestToolErrors_CarryNoStructuredContent(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	result, _ := callJSON(t, s.handleReadPage, map[string]any{"site_key": "ghost", "url": "https://x/"})
	require.True(t, result.IsError)
	assert.Nil(t, result.StructuredContent)
	assert.Contains(t, result.Content[0].(mcpgo.TextContent).Text, "site 'ghost' not found")
}

func TestSiteEntries_ReportLastCrawledAndRunning(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	writeJSONLRecords(t, jsonlPath, []any{models.CrawlMetaJSONL{
		RecordType: models.RecordTypeCrawlMeta, SiteKey: "docs", CrawlEndedAt: "2026-05-23T10:00:00Z",
	}})
	_, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)

	listed, _ := callJSON(t, s.handleListSites, nil)
	described, _ := callJSON(t, s.handleDescribeServer, nil)
	for tool, result := range map[string]*mcpgo.CallToolResult{"list_sites": listed, "describe_server": described} {
		got := requireToolResultConforms(t, tool, result)
		site := got["sites"].([]any)[0].(map[string]any)
		assert.Equal(t, "2026-05-23T10:00:00Z", site["last_crawled"], tool)
		assert.Equal(t, "running", site["status"], tool)
	}
}
