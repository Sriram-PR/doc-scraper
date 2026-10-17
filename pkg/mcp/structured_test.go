package mcp

import (
	"bytes"
	"encoding/json"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
