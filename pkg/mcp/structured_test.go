package mcp

import (
	"bytes"
	"context"
	"encoding/json"
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

	_, miss := callSearchDocs(t, s, map[string]any{"query": "zebra"})
	got = requireToolResultConforms(t, "search_docs", miss)
	assert.EqualValues(t, 0, got["count"])
	assert.Equal(t, []any{}, got["results"])
	assert.NotContains(t, got, "site_key")
}
