package mcp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

// At UTC+11 the local and UTC timestamps for one instant fall on different dates.
var aedt = time.FixedZone("AEDT", 11*3600)

func TestTimestampsAreUTC(t *testing.T) {
	s, jsonlPath := newTestServer(t, "docs", "docs.example.com")
	attachIndex(t, s)
	writeJSONLRecords(t, jsonlPath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/a", Title: "A",
			CrawledAt: "2026-10-19T00:52:28+11:00", Content: "# A"},
		models.CrawlMetaJSONL{RecordType: models.RecordTypeCrawlMeta, SiteKey: "docs",
			CrawlStartedAt: "2026-10-19T00:50:00+11:00", CrawlEndedAt: "2026-10-19T00:52:28+11:00", TotalPages: 1},
	})
	job, err := s.jobManager.CreateJob("docs", false)
	require.NoError(t, err)
	assert.Equal(t, time.UTC, job.StartedAt.Location())
	job.StartedAt = time.Date(2026, 10, 19, 0, 50, 0, 0, aedt)

	_, got := callJSON(t, s.handleListPages, map[string]any{"site_key": "docs"})
	assert.Equal(t, "2026-10-18T13:52:28Z", got["pages"].([]any)[0].(map[string]any)["crawled_at"])

	_, got = callJSON(t, s.handleReadPage, map[string]any{"site_key": "docs", "url": "https://docs.example.com/a"})
	assert.Equal(t, "2026-10-18T13:52:28Z", got["crawled_at"])

	_, got = callJSON(t, s.handleListSites, map[string]any{})
	assert.Equal(t, "2026-10-18T13:52:28Z", got["sites"].([]any)[0].(map[string]any)["last_crawled"])

	_, got = callJSON(t, s.handleDescribeServer, map[string]any{})
	assert.Equal(t, "2026-10-18T13:52:28Z", got["sites"].([]any)[0].(map[string]any)["last_crawled"])
	assert.Equal(t, "2026-10-18T13:50:00Z", got["recent_jobs"].([]any)[0].(map[string]any)["started_at"])

	_, got = callJSON(t, s.handleGetJobStatus, map[string]any{"job_id": job.ID})
	assert.Equal(t, "2026-10-18T13:50:00Z", got["started_at"])

	_, got = callJSON(t, s.handleDiffCrawl, map[string]any{"site_key": "docs", "since": "2026-10-19T00:52:28+11:00"})
	assert.Equal(t, "2026-10-18T13:52:28Z", got["since"])
}

func TestSummarizeJob_CompletedAtIsUTC(t *testing.T) {
	sum := summarizeJob(&Job{ID: "j", SiteKey: "docs", Status: JobStatusCompleted,
		StartedAt: time.Date(2026, 10, 19, 0, 50, 0, 0, aedt), CompletedAt: time.Date(2026, 10, 19, 0, 52, 28, 0, aedt)})
	assert.Equal(t, "2026-10-18T13:50:00Z", sum.StartedAt)
	assert.Equal(t, "2026-10-18T13:52:28Z", sum.CompletedAt)
}
