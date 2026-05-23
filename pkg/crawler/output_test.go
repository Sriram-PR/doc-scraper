package crawler

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"log/slog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
)

// silentLogger returns a *slog.Logger that discards all output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestFlushBufferedJSONL_SortsByURL(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "pages.jsonl")
	f, err := os.Create(jsonlPath)
	require.NoError(t, err)

	om := &OutputManager{
		log:           silentLogger(),
		jsonlFile:     f,
		jsonlFilePath: jsonlPath,
		bufferOutput:  true,
		collectedPageJSONL: []models.PageJSONL{
			{URL: "https://example.com/zeta", Title: "Z"},
			{URL: "https://example.com/alpha", Title: "A"},
			{URL: "https://example.com/mu", Title: "M"},
		},
	}

	om.flushBufferedJSONL()
	require.NoError(t, f.Close())

	// Records should be empty post-flush (consumed).
	assert.Nil(t, om.collectedPageJSONL)

	got := readJSONLURLs(t, jsonlPath)
	assert.Equal(t, []string{
		"https://example.com/alpha",
		"https://example.com/mu",
		"https://example.com/zeta",
	}, got, "JSONL records must be written in URL order regardless of insertion order")
}

func TestRecordJSONL_StreamsInResumeMode(t *testing.T) {
	// When bufferOutput=false (resume mode), records must go straight to disk
	// and NOT accumulate in collectedPageJSONL.
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "pages.jsonl")
	f, err := os.Create(jsonlPath)
	require.NoError(t, err)

	om := &OutputManager{
		log:           silentLogger(),
		jsonlFile:     f,
		jsonlFilePath: jsonlPath,
		bufferOutput:  false,
	}
	om.recordJSONL(models.PageJSONL{URL: "https://example.com/a", Title: "A"}, silentLogger())
	om.recordJSONL(models.PageJSONL{URL: "https://example.com/b", Title: "B"}, silentLogger())
	require.NoError(t, f.Close())

	assert.Empty(t, om.collectedPageJSONL, "resume-mode records must not be buffered")
	got := readJSONLURLs(t, jsonlPath)
	assert.Len(t, got, 2, "both records should have been streamed to disk")
}

func TestClose_AppendsCrawlMetaRecordAsFinalLine(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "pages.jsonl")
	f, err := os.Create(jsonlPath)
	require.NoError(t, err)

	om := &OutputManager{
		log:            silentLogger(),
		siteKey:        "example",
		siteCfg:        &config.SiteConfig{AllowedDomain: "example.com"},
		resolved:       &config.ResolvedSiteConfig{},
		siteOutputDir:  tmpDir,
		jsonlFile:      f,
		jsonlFilePath:  jsonlPath,
		bufferOutput:   true,
		crawlStartTime: time.Now(),
		collectedPageJSONL: []models.PageJSONL{
			{RecordType: models.RecordTypePage, URL: "https://example.com/b"},
			{RecordType: models.RecordTypePage, URL: "https://example.com/a"},
		},
	}
	om.pagesRecorded.Store(2)

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

