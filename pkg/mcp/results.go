package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// Each tool's result struct is both its structuredContent and, through
// outputSchemaFor, its declared outputSchema, so the two cannot drift.

// outputSchemaJSON reflects T into an open JSON schema. Reflection closes every
// object (additionalProperties:false); that is dropped so adding a field later
// does not break clients validating against a cached tools/list.
func outputSchemaJSON[T any]() json.RawMessage {
	var tool mcp.Tool
	mcp.WithOutputSchema[T]()(&tool)
	if tool.OutputSchema.Type == "" {
		panic(fmt.Sprintf("mcp: cannot generate output schema for %T", *new(T)))
	}
	b, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		panic(fmt.Sprintf("mcp: marshal output schema for %T: %v", *new(T), err))
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		panic(fmt.Sprintf("mcp: decode output schema for %T: %v", *new(T), err))
	}
	stripClosedObjects(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("mcp: re-marshal output schema for %T: %v", *new(T), err))
	}
	return out
}

func stripClosedObjects(v any) {
	switch x := v.(type) {
	case map[string]any:
		if ap, ok := x["additionalProperties"].(bool); ok && !ap {
			delete(x, "additionalProperties")
		}
		for _, e := range x {
			stripClosedObjects(e)
		}
	case []any:
		for _, e := range x {
			stripClosedObjects(e)
		}
	}
}

func outputSchemaFor[T any]() mcp.ToolOption {
	return mcp.WithRawOutputSchema(outputSchemaJSON[T]())
}

func structuredResult(v any) *mcp.CallToolResult {
	return mcp.NewToolResultStructuredOnly(v)
}

type siteSummary struct {
	Key            string `json:"key"`
	Domain         string `json:"domain"`
	PathPrefix     string `json:"path_prefix"`
	StartURLsCount int    `json:"start_urls_count"`
	MaxDepth       int    `json:"max_depth"`
	LastCrawled    string `json:"last_crawled,omitempty"`
	Status         string `json:"status,omitempty" jsonschema_description:"\"running\" while a crawl is active; absent otherwise"`
}

type listSitesResult struct {
	Sites      []siteSummary `json:"sites"`
	ConfigPath string        `json:"config_path"`
	TotalSites int           `json:"total_sites"`
}
