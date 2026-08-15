package crawler

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

// silentLogger returns a *slog.Logger that discards all output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestFinalizeJSONL_DedupesAndSortsByURL(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "pages.jsonl")
	f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	require.NoError(t, err)

	om := &OutputManager{
		log:            silentLogger(),
		siteCfg:        &config.SiteConfig{AllowedDomain: "example.com"},
		resolved:       &config.ResolvedSiteConfig{},
		siteOutputDir:  tmpDir,
		jsonlFile:      f,
		jsonlFilePath:  jsonlPath,
		crawlStartTime: time.Now(),
	}
	// Stream out of URL order, with a duplicate URL: the last write must win.
	om.recordJSONL(models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/zeta", Title: "Z"}, silentLogger())
	om.recordJSONL(models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/alpha", Title: "A-old"}, silentLogger())
	om.recordJSONL(models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/mu", Title: "M"}, silentLogger())
	om.recordJSONL(models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/alpha", Title: "A-new"}, silentLogger())

	om.closeJSONLFile()
	om.finalizeJSONL()

	lines := readJSONLLines(t, jsonlPath)
	require.Len(t, lines, 4, "3 unique page records (alpha deduped) plus one crawl_meta")

	var pages []models.PageJSONL
	for _, l := range lines {
		var p models.PageJSONL
		require.NoError(t, json.Unmarshal([]byte(l), &p))
		if p.RecordType == models.RecordTypePage {
			pages = append(pages, p)
		}
	}
	require.Len(t, pages, 3)
	assert.Equal(t, []string{
		"https://example.com/alpha",
		"https://example.com/mu",
		"https://example.com/zeta",
	}, []string{pages[0].URL, pages[1].URL, pages[2].URL}, "page records must be written in URL order")
	assert.Equal(t, "A-new", pages[0].Title, "last write for a duplicate URL must win")
}

func TestRecordJSONL_StreamsToDisk(t *testing.T) {
	// Records must go straight to disk as they are produced, not accumulate in
	// memory, regardless of fresh vs resume mode.
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "pages.jsonl")
	f, err := os.Create(jsonlPath)
	require.NoError(t, err)

	om := &OutputManager{
		log:           silentLogger(),
		jsonlFile:     f,
		jsonlFilePath: jsonlPath,
	}
	om.recordJSONL(models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/a", Title: "A"}, silentLogger())
	om.recordJSONL(models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/b", Title: "B"}, silentLogger())
	require.NoError(t, f.Close())

	got := readJSONLURLs(t, jsonlPath)
	assert.Len(t, got, 2, "both records should have been streamed to disk")
}

func TestClose_WritesSortedPagesAndCrawlMeta(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "pages.jsonl")
	f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	require.NoError(t, err)

	om := &OutputManager{
		log:            silentLogger(),
		siteKey:        "example",
		siteCfg:        &config.SiteConfig{AllowedDomain: "example.com"},
		resolved:       &config.ResolvedSiteConfig{},
		siteOutputDir:  tmpDir,
		jsonlFile:      f,
		jsonlFilePath:  jsonlPath,
		crawlStartTime: time.Now(),
	}
	// Streamed out of URL order; Close must dedupe/sort and set total_pages.
	om.recordJSONL(models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/b"}, silentLogger())
	om.recordJSONL(models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/a"}, silentLogger())

	require.NoError(t, om.Close())

	lines := readJSONLLines(t, jsonlPath)
	require.Len(t, lines, 3, "two page records plus one crawl_meta record")

	// First two lines are page records, sorted by URL.
	for i, wantURL := range []string{"https://example.com/a", "https://example.com/b"} {
		var page models.PageJSONL
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &page))
		assert.Equal(t, models.RecordTypePage, page.RecordType)
		assert.Equal(t, wantURL, page.URL)
	}

	// Final line is the crawl_meta summary.
	var meta models.CrawlMetaJSONL
	require.NoError(t, json.Unmarshal([]byte(lines[2]), &meta))
	assert.Equal(t, models.RecordTypeCrawlMeta, meta.RecordType)
	assert.Equal(t, "example", meta.SiteKey)
	assert.Equal(t, "example.com", meta.AllowedDomain)
	assert.Equal(t, 2, meta.TotalPages)
	assert.NotEmpty(t, meta.CrawlStartedAt)
	assert.NotEmpty(t, meta.CrawlEndedAt)
}

// readJSONLLines returns the non-empty lines of a JSONL file in file order.
func readJSONLLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	require.NoError(t, scanner.Err())
	return lines
}

// readJSONLURLs returns the URL field of each JSONL line.
func readJSONLURLs(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var urls []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var page struct {
			URL string `json:"url"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &page))
		urls = append(urls, page.URL)
	}
	require.NoError(t, scanner.Err())
	return urls
}
