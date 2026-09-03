package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func newTestServerWithIndex(t *testing.T, siteKey string) *Server {
	t.Helper()
	s, _ := newTestServer(t, siteKey, "docs.example.com")
	idx, err := index.Open(filepath.Join(t.TempDir(), "index.db"), 5, silentTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	s.idx = idx
	return s
}

func callSearchDocs(t *testing.T, s *Server, args map[string]any) (map[string]any, *mcpgo.CallToolResult) {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleSearchDocs(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok, "expected TextContent")
	if result.IsError {
		return nil, result
	}
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &got))
	return got, result
}

func TestHandleSearchDocs_HappyPath(t *testing.T) {
	s := newTestServerWithIndex(t, "docs")
	md := "# Retries\n\nRetry limits are configured with max_retries. " + strings.Repeat("filler ", 40)
	require.NoError(t, s.idx.ReplaceChunks(context.Background(),
		"docs", "https://docs.example.com/retries", "Retries", "h1", chunk.Split(md)))

	got, _ := callSearchDocs(t, s, map[string]any{"query": "retry limits"})
	assert.EqualValues(t, 1, got["count"])
	results := got["results"].([]any)
	first := results[0].(map[string]any)
	assert.Equal(t, "https://docs.example.com/retries", first["url"])
	assert.Contains(t, first["snippet"], "[")
	assert.Contains(t, got["next_actions"], "read_page")
}

func TestHandleSearchDocs_Validation(t *testing.T) {
	s := newTestServerWithIndex(t, "docs")

	_, res := callSearchDocs(t, s, map[string]any{})
	assert.True(t, res.IsError, "missing query must error")

	_, res = callSearchDocs(t, s, map[string]any{"query": "x", "site_key": "unknown"})
	assert.True(t, res.IsError, "unknown site_key must error")
}

func TestHandleSearchDocs_IndexDisabled(t *testing.T) {
	s, _ := newTestServer(t, "docs", "docs.example.com")
	_, res := callSearchDocs(t, s, map[string]any{"query": "anything"})
	require.True(t, res.IsError)
	tc := res.Content[0].(mcpgo.TextContent)
	assert.Contains(t, tc.Text, "index is disabled")
}

func TestHandleSearchDocs_EmptyResultHints(t *testing.T) {
	s := newTestServerWithIndex(t, "docs")

	got, _ := callSearchDocs(t, s, map[string]any{"query": "nothingmatches", "site_key": "docs"})
	assert.EqualValues(t, 0, got["count"])
	assert.Contains(t, got["next_actions"], "crawl_site", "unindexed site should point at crawl_site")

	md := "# Page\n\nsome indexed content here. " + strings.Repeat("filler ", 40)
	require.NoError(t, s.idx.ReplaceChunks(context.Background(),
		"docs", "https://docs.example.com/p", "Page", "h1", chunk.Split(md)))

	got, _ = callSearchDocs(t, s, map[string]any{"query": "nothingmatches", "site_key": "docs"})
	assert.EqualValues(t, 0, got["count"])
	assert.Contains(t, got["next_actions"], "Broaden", "indexed site should suggest broadening")
}

func TestHandleSearchDocs_MalformedQueryDoesNotError(t *testing.T) {
	s := newTestServerWithIndex(t, "docs")
	got, res := callSearchDocs(t, s, map[string]any{"query": `"unbalanced`})
	require.False(t, res.IsError)
	assert.EqualValues(t, 0, got["count"])
}
