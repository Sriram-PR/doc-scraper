package mcp

import (
	"encoding/json"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type schemaInner struct {
	ID int64 `json:"id"`
}

type schemaSample struct {
	Name  string        `json:"name"`
	Count int           `json:"count"`
	Note  string        `json:"note,omitempty"`
	Inner *schemaInner  `json:"inner,omitempty"`
	List  []schemaInner `json:"list"`
}

type schemaCycle struct {
	Next *schemaCycle `json:"next"`
}

func TestOutputSchemaJSON_OpenAtEveryLevel(t *testing.T) {
	raw := outputSchemaJSON[schemaSample]()
	assert.NotContains(t, string(raw), `"additionalProperties":false`)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.Equal(t, "object", doc["type"])
	assert.ElementsMatch(t, []any{"name", "count", "list"}, doc["required"])
	props := doc["properties"].(map[string]any)
	inner := props["inner"].(map[string]any)
	assert.Contains(t, inner["properties"], "id")
	assert.Equal(t, []any{"id"}, inner["required"])
}

func TestOutputSchemaJSON_PanicsWhenTypeCannotBeReflected(t *testing.T) {
	assert.Panics(t, func() { outputSchemaJSON[schemaCycle]() })
}

func TestStructuredResult_TextIsCompactJSONOfStructuredContent(t *testing.T) {
	r := structuredResult(schemaSample{Name: "a", List: []schemaInner{}})
	requireConforms(t, outputSchemaJSON[schemaSample](), r)
	text := r.Content[0].(mcpgo.TextContent).Text
	assert.JSONEq(t, `{"name":"a","count":0,"list":[]}`, text)
	assert.NotContains(t, text, "\n", "text block must be compact JSON")
}
