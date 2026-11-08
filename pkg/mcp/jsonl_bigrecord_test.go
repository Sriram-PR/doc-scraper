package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

func TestReadersReadPastAJSONLRecordOver10MiB(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/big", Title: "Big",
			Content: "# Big\n\n" + strings.Repeat("walrus ", (11<<20)/7)},
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/after", Title: "After",
			Content: "# After\n\nPelican."},
		models.CrawlMetaJSONL{RecordType: models.RecordTypeCrawlMeta, SiteKey: "docs",
			CrawlEndedAt: "2026-11-08T10:00:00Z", TotalPages: 2},
	})

	_, got := callJSON(t, s.handleListPages, map[string]any{"site_key": "docs"})
	assert.EqualValues(t, 2, got["total"], "list_pages")

	result, got := callJSON(t, s.handleReadPage, map[string]any{"site_key": "docs", "url": "https://docs.example.com/after"})
	require.False(t, result.IsError, "%v", result.Content)
	assert.Contains(t, got["content"], "Pelican", "read_page")

	_, got = callJSON(t, s.handleListSites, map[string]any{})
	assert.Equal(t, "2026-11-08T10:00:00Z", got["sites"].([]any)[0].(map[string]any)["last_crawled"], "last_crawled")
}
