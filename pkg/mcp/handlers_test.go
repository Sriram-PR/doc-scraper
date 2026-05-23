package mcp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
)

// silentTestLogger returns a logrus.Entry that discards output (tests should
// not produce stderr noise).
func silentTestLogger() *logrus.Entry {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return logrus.NewEntry(l)
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
			Logger:    silentTestLogger().Logger,
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
	assert.Equal(t, float64(3), got["total"], "crawl_meta record must not be counted")
	assert.Equal(t, float64(0), got["offset"])
	assert.Equal(t, float64(3), got["returned"])

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
	assert.Equal(t, float64(9), first["content_length"], "content_length should be len(\"content A\")")
	assert.NotContains(t, first, "content")
}

func TestHandleListPages_Pagination(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	var records []interface{}
	for _, c := range []string{"a", "b", "c", "d", "e"} {
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

	assert.Equal(t, float64(5), got["total"])
	assert.Equal(t, float64(2), got["offset"])
	assert.Equal(t, float64(2), got["returned"])

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

	assert.Equal(t, float64(1), got["total"])
	assert.Equal(t, float64(0), got["returned"])
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

	assert.Equal(t, float64(0), got["total"])
	assert.Equal(t, float64(0), got["returned"])
	pages, ok := got["pages"].([]any)
	require.True(t, ok)
	assert.Empty(t, pages)
	assert.Contains(t, got["message"], "No crawl output found")
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
	assert.Equal(t, float64(100), got["max_results"])

	// max_results above 1000 is clamped to 1000.
	got = callListPages(t, s, map[string]any{
		"site_key":    "docs",
		"max_results": float64(50000),
	})
	assert.Equal(t, float64(1000), got["max_results"])
}
