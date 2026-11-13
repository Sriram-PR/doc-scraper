package mcp

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var contentLineRE = regexp.MustCompile(`^bytes (\d+)-(\d+) of (\d+)(?: \(truncated; next_offset: (\d+)\))?$`)

// parsePageText reads a page tool's text result into named fields (offset,
// content_length, truncated, next_offset, ...) so tests can assert on them.
func parsePageText(t *testing.T, result *mcpgo.CallToolResult) map[string]any {
	t.Helper()
	require.NotNil(t, result)
	require.False(t, result.IsError, "%v", result.Content)
	require.Nil(t, result.StructuredContent, "page tools return text only")
	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok, "expected TextContent")

	head, content, found := strings.Cut(tc.Text, "\n\n")
	require.True(t, found, "header must end with an empty line")
	got := map[string]any{"content": content}
	num := func(s string) float64 {
		n, err := strconv.Atoi(s)
		require.NoError(t, err, s)
		return float64(n)
	}
	for _, line := range strings.Split(head, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		require.True(t, ok, "header line %q", line)
		switch key {
		case "content":
			m := contentLineRE.FindStringSubmatch(value)
			require.NotNil(t, m, "content line %q", value)
			start, end := num(m[1]), num(m[2])
			got["offset"] = start
			got["content_length"] = end - start
			got["total_length"] = num(m[3])
			got["truncated"] = m[4] != ""
			if m[4] != "" {
				assert.Equal(t, m[2], m[4], "next_offset is the end of the returned range")
				got["next_offset"] = num(m[4])
			}
		case "section_end", "depth", "fetch_time_ms":
			got[key] = num(value)
		case "headings":
			parts := strings.Split(value, " | ")
			hs := make([]any, len(parts))
			for i, h := range parts {
				hs[i] = h
			}
			got[key] = hs
		default:
			got[key] = value
		}
	}
	require.Contains(t, got, "offset", "header must carry a content line")
	assert.Equal(t, len(content), int(got["content_length"].(float64)), "content line must match the content")
	return got
}

func TestPageTextResult_HeaderThenContent(t *testing.T) {
	r := pageTextResult([]headerLine{
		{"url", "https://docs.example.com/a"},
		{"title", "A"},
		contentLine(0, 5, 5, false),
	}, "hello")
	require.Nil(t, r.StructuredContent)
	require.Len(t, r.Content, 1)
	assert.Equal(t, "url: https://docs.example.com/a\ntitle: A\ncontent: bytes 0-5 of 5\n\nhello",
		r.Content[0].(mcpgo.TextContent).Text)
}

func TestContentLine_NextOffsetOnlyWhenTruncated(t *testing.T) {
	assert.Equal(t, headerLine{"content", "bytes 10-20 of 50 (truncated; next_offset: 20)"}, contentLine(10, 20, 50, true))
	assert.Equal(t, headerLine{"content", "bytes 40-50 of 50"}, contentLine(40, 50, 50, false))
}

func TestPageTextResult_FlattensNewlinesInValues(t *testing.T) {
	r := pageTextResult([]headerLine{{"title", "Two\r\nLines\nhere\r"}, contentLine(0, 2, 2, false)}, "ab")
	got := parsePageText(t, r)
	assert.Equal(t, "Two Lines here ", got["title"])
	assert.Equal(t, "ab", got["content"])
}

func TestPageTextResult_ContentKeptVerbatim(t *testing.T) {
	md := "# H\n\nkey: value\n\n```\ncode\n```"
	got := parsePageText(t, pageTextResult([]headerLine{contentLine(0, len(md), len(md), false)}, md))
	assert.Equal(t, md, got["content"])
	assert.Equal(t, false, got["truncated"])
	assert.NotContains(t, got, "next_offset")
}

// callPage is callJSON for the page tools, whose results are text.
func callPage(t *testing.T, fn func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error), args map[string]any) (*mcpgo.CallToolResult, map[string]any) {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = args
	result, err := fn(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	if result.IsError {
		return result, nil
	}
	return result, parsePageText(t, result)
}

func TestPageTools_DeclareNoOutputSchema(t *testing.T) {
	s := newStdioTestServer(t)
	for _, name := range []string{"get_page", "read_page"} {
		st := s.mcpServer.GetTool(name)
		require.NotNil(t, st, name)
		assert.Empty(t, st.Tool.RawOutputSchema, "%s must not declare an outputSchema", name)
		maxBytes := st.Tool.InputSchema.Properties["max_bytes"].(map[string]any)
		assert.Contains(t, maxBytes["description"], "default: 32768, max: 1048576", name)
	}
}

func TestOtherTools_KeepOutputSchema(t *testing.T) {
	s := newStdioTestServer(t)
	for _, name := range []string{"list_sites", "crawl_site", "get_job_status", "cancel_crawl", "draft_site",
		"save_site", "describe_server", "list_pages", "search_docs", "get_freshness", "diff_crawl"} {
		st := s.mcpServer.GetTool(name)
		require.NotNil(t, st, name)
		assert.NotEmpty(t, st.Tool.RawOutputSchema, "%s keeps its outputSchema", name)
	}
}

func TestPageTextResult_FlattensUnicodeLineBreaksInValues(t *testing.T) {
	r := pageTextResult([]headerLine{{"title", "a b c\u0085d"}, contentLine(0, 2, 2, false)}, "ab")
	text := r.Content[0].(mcpgo.TextContent).Text
	assert.True(t, strings.HasPrefix(text, "title: a b c d\n"), text)
}
