package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

// oldLastCrawledTime is the scan list_sites and describe_server used before
// lastCrawlEnded read the file's tail.
func oldLastCrawledTime(jsonlPath string) time.Time {
	file, err := os.Open(jsonlPath)
	if err != nil {
		return time.Time{}
	}
	defer file.Close()

	var lastEnded time.Time
	scanner := crawler.NewJSONLScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, models.RecordTypeCrawlMeta) {
			continue
		}
		var meta models.CrawlMetaJSONL
		if err := json.Unmarshal([]byte(line), &meta); err != nil {
			continue
		}
		if meta.RecordType != models.RecordTypeCrawlMeta {
			continue
		}
		if t, err := time.Parse(time.RFC3339, meta.CrawlEndedAt); err == nil {
			lastEnded = t
		}
	}
	return lastEnded
}

func metaLine(ended string) string {
	return `{"record_type":"crawl_meta","site_key":"s","crawl_ended_at":"` + ended + `"}`
}

func TestLastCrawlEnded_MatchesTheOldScan(t *testing.T) {
	big, err := json.Marshal(models.PageJSONL{
		RecordType: models.RecordTypePage, URL: "https://d.example/big",
		Content: strings.Repeat("x", 3*lastCrawlTailBytes),
	})
	require.NoError(t, err)

	dir := t.TempDir()
	for name, content := range map[string]string{
		"finalized":            pageLine(t, "https://d.example/a", "A") + "\n" + metaLine("2026-05-24T11:30:00+02:00") + "\n",
		"no trailing newline":  pageLine(t, "https://d.example/a", "A") + "\n" + metaLine("2026-05-24T11:30:00Z"),
		"trailing blank lines": metaLine("2026-05-24T11:30:00Z") + "\n\n  \n",
		"meta only":            metaLine("2026-05-24T11:30:00Z") + "\n",
		"resume in progress":   metaLine("2026-05-23T10:00:00Z") + "\n" + pageLine(t, "https://d.example/a", "A") + "\n",
		"two crawls, resume in progress": metaLine("2026-05-23T10:00:00Z") + "\n" + metaLine("2026-05-24T10:00:00Z") + "\n" +
			pageLine(t, "https://d.example/a", "A") + "\n",
		"last meta has bad time": metaLine("2026-05-23T10:00:00Z") + "\n" + metaLine("not a time") + "\n",
		"last meta is truncated": metaLine("2026-05-23T10:00:00Z") + "\n" + `{"record_type":"crawl_meta","crawl_ended_at":`,
		"page mentions crawl_meta last": metaLine("2026-05-23T10:00:00Z") + "\n" +
			pageLine(t, "https://d.example/crawl_meta", "crawl_meta") + "\n",
		"last line longer than the tail": metaLine("2026-05-23T10:00:00Z") + "\n" + string(big) + "\n",
		"no meta":                        pageLine(t, "https://d.example/a", "A") + "\n",
		"empty":                          "",
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".jsonl")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		want := oldLastCrawledTime(path)
		got := lastCrawlEnded(path)
		assert.True(t, want.Equal(got), "%s: got %v, want %v", name, got, want)
	}

	got := lastCrawlEnded(filepath.Join(dir, "finalized.jsonl"))
	assert.Equal(t, "2026-05-24T09:30:00Z", got.UTC().Format(time.RFC3339))
	assert.True(t, lastCrawlEnded(filepath.Join(dir, "missing.jsonl")).IsZero())
}
