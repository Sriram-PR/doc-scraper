package crawler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

// writeTestJSONL writes the given records as one JSON object per line to path.
// Each value must JSON-encode (we use models.PageJSONL / models.CrawlMetaJSONL).
func writeTestJSONL(t *testing.T, path string, records []interface{}) {
	t.Helper()
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

func newTestOutputManager(t *testing.T, dir string, jsonlName, siteKey, domain string) *OutputManager {
	t.Helper()
	return &OutputManager{
		log:           silentLogger(),
		siteCfg:       &config.SiteConfig{AllowedDomain: domain},
		siteKey:       siteKey,
		siteOutputDir: dir,
		jsonlFilePath: filepath.Join(dir, jsonlName),
	}
}

func TestWriteLLMsTxtFiles_HappyPath(t *testing.T) {
	dir := t.TempDir()
	om := newTestOutputManager(t, dir, "pages.jsonl", "example_docs", "docs.example.com")

	writeTestJSONL(t, om.jsonlFilePath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/a", Title: "Page A", Content: "Body A"},
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/b", Title: "Page B", Content: "Body B"},
		models.CrawlMetaJSONL{RecordType: models.RecordTypeCrawlMeta, SiteKey: "example_docs", TotalPages: 2},
	})

	om.writeLLMsTxtFiles()

	indexBytes, err := os.ReadFile(filepath.Join(dir, llmsTxtFilename))
	require.NoError(t, err)
	index := string(indexBytes)

	assert.Contains(t, index, "# example_docs")
	assert.Contains(t, index, "> Crawled documentation from docs.example.com. 2 pages.")
	assert.Contains(t, index, "## Pages")
	assert.Contains(t, index, "- [Page A](https://docs.example.com/a)")
	assert.Contains(t, index, "- [Page B](https://docs.example.com/b)")
	// crawl_meta record must not appear as a page entry.
	assert.NotContains(t, index, "crawl_meta")

	fullBytes, err := os.ReadFile(filepath.Join(dir, llmsFullTxtFilename))
	require.NoError(t, err)
	full := string(fullBytes)

	assert.Contains(t, full, "# example_docs")
	assert.Contains(t, full, "> Full crawled content from docs.example.com.")
	assert.Contains(t, full, "# Page A")
	assert.Contains(t, full, "URL: https://docs.example.com/a")
	assert.Contains(t, full, "Body A")
	assert.Contains(t, full, "# Page B")
	assert.Contains(t, full, "Body B")
	// Section separators between pages.
	assert.GreaterOrEqual(t, strings.Count(full, "\n---\n"), 2)
}

func TestWriteLLMsTxtFiles_EmptyJSONL(t *testing.T) {
	dir := t.TempDir()
	om := newTestOutputManager(t, dir, "pages.jsonl", "empty_site", "empty.example.com")
	writeTestJSONL(t, om.jsonlFilePath, nil)

	om.writeLLMsTxtFiles()

	index, err := os.ReadFile(filepath.Join(dir, llmsTxtFilename))
	require.NoError(t, err)
	assert.Contains(t, string(index), "0 pages")
	assert.Contains(t, string(index), "## Pages")
	assert.Contains(t, string(index), "_No pages were successfully crawled._")

	_, err = os.Stat(filepath.Join(dir, llmsFullTxtFilename))
	assert.NoError(t, err, "llms-full.txt should still be created with just the header")
}

func TestWriteLLMsTxtFiles_NoJSONLPathIsNoop(t *testing.T) {
	dir := t.TempDir()
	om := &OutputManager{
		log:           silentLogger(),
		siteCfg:       &config.SiteConfig{AllowedDomain: "x"},
		siteKey:       "x",
		siteOutputDir: dir,
		// jsonlFilePath intentionally empty
	}

	om.writeLLMsTxtFiles()

	for _, name := range []string{llmsTxtFilename, llmsFullTxtFilename} {
		_, err := os.Stat(filepath.Join(dir, name))
		assert.True(t, os.IsNotExist(err), "%s should not be created when JSONL is disabled", name)
	}
}

func TestWriteLLMsTxtFiles_MissingJSONLIsNoop(t *testing.T) {
	dir := t.TempDir()
	om := newTestOutputManager(t, dir, "pages.jsonl", "x", "x.example.com")
	// JSONL file deliberately not created on disk.

	om.writeLLMsTxtFiles()

	for _, name := range []string{llmsTxtFilename, llmsFullTxtFilename} {
		_, err := os.Stat(filepath.Join(dir, name))
		assert.True(t, os.IsNotExist(err))
	}
}

func TestWriteLLMsTxtFiles_TitleFallsBackToURL(t *testing.T) {
	dir := t.TempDir()
	om := newTestOutputManager(t, dir, "pages.jsonl", "site", "example.com")
	writeTestJSONL(t, om.jsonlFilePath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://example.com/untitled", Title: "", Content: "Body"},
	})

	om.writeLLMsTxtFiles()

	index, err := os.ReadFile(filepath.Join(dir, llmsTxtFilename))
	require.NoError(t, err)
	assert.Contains(t, string(index), "- [https://example.com/untitled](https://example.com/untitled)")
}

func TestWriteLLMsTxtFiles_EscapesBracketsInTitle(t *testing.T) {
	dir := t.TempDir()
	om := newTestOutputManager(t, dir, "pages.jsonl", "site", "example.com")
	writeTestJSONL(t, om.jsonlFilePath, []interface{}{
		models.PageJSONL{
			RecordType: models.RecordTypePage,
			URL:        "https://example.com/x",
			Title:      "Article [Draft]",
			Content:    "Body",
		},
	})

	om.writeLLMsTxtFiles()

	index, err := os.ReadFile(filepath.Join(dir, llmsTxtFilename))
	require.NoError(t, err)
	// Brackets must be escaped so the markdown link parser doesn't choke.
	assert.Contains(t, string(index), `- [Article \[Draft\]](https://example.com/x)`)
}

func TestWriteLLMsTxtFiles_StripsNewlinesInTitle(t *testing.T) {
	dir := t.TempDir()
	om := newTestOutputManager(t, dir, "pages.jsonl", "site", "example.com")
	writeTestJSONL(t, om.jsonlFilePath, []interface{}{
		models.PageJSONL{
			RecordType: models.RecordTypePage,
			URL:        "https://example.com/x",
			Title:      "Line one\nLine two",
			Content:    "Body",
		},
	})

	om.writeLLMsTxtFiles()

	index, err := os.ReadFile(filepath.Join(dir, llmsTxtFilename))
	require.NoError(t, err)
	assert.Contains(t, string(index), "- [Line one Line two](https://example.com/x)")
	assert.NotContains(t, string(index), "Line one\nLine two")

	full, err := os.ReadFile(filepath.Join(dir, llmsFullTxtFilename))
	require.NoError(t, err)
	assert.Contains(t, string(full), "# Line one Line two")
}
