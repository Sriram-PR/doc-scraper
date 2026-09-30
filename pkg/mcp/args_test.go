package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func errText(t *testing.T, r *mcpgo.CallToolResult) string {
	t.Helper()
	require.True(t, r.IsError, "expected an error result")
	return r.Content[0].(mcpgo.TextContent).Text
}

func TestToolArgumentTypesAreEnforced(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	attachIndex(t, s)
	since := time.Now().Format(time.RFC3339)

	cases := []struct {
		name string
		fn   func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
		args map[string]any
		want string
	}{
		{"crawl incremental string", s.handleCrawlSite, map[string]any{"site_key": "docs", "incremental": "yes"}, `"incremental" must be boolean`},
		{"crawl incremental number", s.handleCrawlSite, map[string]any{"site_key": "docs", "incremental": 1.0}, `"incremental" must be boolean`},
		{"crawl site_key number", s.handleCrawlSite, map[string]any{"site_key": 5.0}, `"site_key" must be string`},
		{"list max_results string", s.handleListPages, map[string]any{"site_key": "docs", "max_results": "3"}, `"max_results" must be integer`},
		{"list max_results fraction", s.handleListPages, map[string]any{"site_key": "docs", "max_results": 2.5}, `"max_results" must be integer`},
		{"list offset bool", s.handleListPages, map[string]any{"site_key": "docs", "offset": true}, `"offset" must be integer`},
		{"read max_bytes string", s.handleReadPage, map[string]any{"site_key": "docs", "url": "u", "max_bytes": "9"}, `"max_bytes" must be integer`},
		{"search limit string", s.handleSearchDocs, map[string]any{"query": "x", "limit": "5"}, `"limit" must be integer`},
		{"search site_key bool", s.handleSearchDocs, map[string]any{"query": "x", "site_key": true}, `"site_key" must be string`},
		{"diff since number", s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": 5.0}, `"since" must be string`},
		{"diff offset string", s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": since, "offset": "1"}, `"offset" must be integer`},
		{"get_page selector number", s.handleGetPage, map[string]any{"url": "http://x", "content_selector": 1.0}, `"content_selector" must be string`},
		{"job_id number", s.handleGetJobStatus, map[string]any{"job_id": 1.0}, `"job_id" must be string`},
		{"cancel job_id bool", s.handleCancelCrawl, map[string]any{"job_id": false}, `"job_id" must be string`},
		{"freshness site_key", s.handleGetFreshness, map[string]any{"site_key": []any{}}, `"site_key" must be string`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := callJSON(t, tc.fn, tc.args)
			assert.Contains(t, errText(t, r), tc.want)
		})
	}
	assert.Empty(t, s.jobManager.ListJobs(), "a rejected crawl_site call must not start a job")
}

func TestToolArgumentTypes_AcceptsValidAndNull(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	got := callListPages(t, s, map[string]any{"site_key": "docs", "max_results": 5.0, "offset": nil})
	assert.EqualValues(t, 5, got["max_results"])
}

func TestHandleDiffCrawl_EchoesEffectiveValues(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	attachIndex(t, s)
	_, got := callJSON(t, s.handleDiffCrawl, map[string]any{
		"site_key": "docs", "since": time.Now().Format(time.RFC3339),
		"max_results": -1.0, "offset": -4.0,
	})
	assert.EqualValues(t, 100, got["max_results"])
	assert.EqualValues(t, 0, got["offset"])

	_, got = callJSON(t, s.handleDiffCrawl, map[string]any{
		"site_key": "docs", "since": time.Now().Format(time.RFC3339), "max_results": 5000.0,
	})
	assert.EqualValues(t, 1000, got["max_results"])
}

func TestGetPage_InvalidSelectorReportedBeforeFetch(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	r, _ := callJSON(t, s.handleGetPage, map[string]any{
		"url": "http://127.0.0.1:1/unreachable", "content_selector": "[[[",
	})
	msg := errText(t, r)
	assert.Contains(t, msg, "invalid CSS selector")
	assert.NotContains(t, msg, "failed to fetch")
}

func TestDescribeServer_NextActionsCoversAllTools(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	_, got := callJSON(t, s.handleDescribeServer, map[string]any{})
	for _, tool := range []string{"search_docs", "list_sites", "list_pages", "read_page", "crawl_site", "get_job_status", "cancel_crawl", "get_page", "get_freshness", "diff_crawl"} {
		assert.Contains(t, got["next_actions"], tool)
	}
}

func assertNoNulls(t *testing.T, name string, v any) {
	t.Helper()
	switch x := v.(type) {
	case nil:
		t.Errorf("%s is null", name)
	case map[string]any:
		for k, e := range x {
			assertNoNulls(t, name+"."+k, e)
		}
	case []any:
		for i, e := range x {
			assertNoNulls(t, name+"."+string(rune('0'+i%10)), e)
		}
	}
}

func TestResponsesNeverContainNull(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	idx := attachIndex(t, s)
	seedCorpus(t, jsonlPath)
	end := time.Now()
	require.NoError(t, idx.RecordCrawl(context.Background(), index.CrawlRecord{
		SiteKey: "docs", CrawlStartedAt: end.Add(-time.Minute), CrawlEndedAt: end, Mode: index.ModeFull,
	}))
	since := end.Format(time.RFC3339)

	calls := map[string]func() (*mcpgo.CallToolResult, map[string]any){
		"describe": func() (*mcpgo.CallToolResult, map[string]any) { return callJSON(t, s.handleDescribeServer, nil) },
		"sites":    func() (*mcpgo.CallToolResult, map[string]any) { return callJSON(t, s.handleListSites, nil) },
		"list": func() (*mcpgo.CallToolResult, map[string]any) {
			return callJSON(t, s.handleListPages, map[string]any{"site_key": "docs", "offset": 99.0})
		},
		"read b": func() (*mcpgo.CallToolResult, map[string]any) {
			return callJSON(t, s.handleReadPage, map[string]any{"site_key": "docs", "url": "https://docs.example.com/b"})
		},
		"diff": func() (*mcpgo.CallToolResult, map[string]any) {
			return callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": since})
		},
		"freshness": func() (*mcpgo.CallToolResult, map[string]any) {
			return callJSON(t, s.handleGetFreshness, map[string]any{"site_key": "docs"})
		},
		"search": func() (*mcpgo.CallToolResult, map[string]any) {
			return callJSON(t, s.handleSearchDocs, map[string]any{"query": "zzz"})
		},
	}
	for name, call := range calls {
		r, got := call()
		require.False(t, r.IsError, name)
		raw, _ := json.Marshal(got)
		assert.NotContains(t, string(raw), "null", name)
		assertNoNulls(t, name, got)
	}
}
