package crawler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

// A page can be up to max_page_size_bytes (50 MiB by default), so its JSONL
// record can be longer than any fixed line cap; every reader must read past it.
func TestJSONLReadersReadPastARecordOver10MiB(t *testing.T) {
	dir := t.TempDir()
	om := newTestOutputManager(t, dir, "pages.jsonl", "docs", "docs.example.com")
	big := "# Big\n\n" + strings.Repeat("walrus ", (11<<20)/7)
	writeTestJSONL(t, om.jsonlFilePath, []interface{}{
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/big", Title: "Big", Content: big, ContentHash: "b1"},
		models.PageJSONL{RecordType: models.RecordTypePage, URL: "https://docs.example.com/after", Title: "After", ContentHash: "a1",
			Content: "# After\n\nThe pelican page comes after the big one. " + strings.Repeat("filler ", 40)},
		models.CrawlMetaJSONL{RecordType: models.RecordTypeCrawlMeta, SiteKey: "docs", TotalPages: 2},
	})
	info, err := os.Stat(om.jsonlFilePath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(10<<20))

	n, err := countPriorPageRecords(om.jsonlFilePath)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "countPriorPageRecords")

	recs, err := readJSONLPagesForIndex(om.jsonlFilePath)
	require.NoError(t, err)
	assert.Len(t, recs, 2, "readJSONLPagesForIndex")

	om.writeLLMsTxtFiles()
	llms, err := os.ReadFile(filepath.Join(dir, llmsTxtFilename))
	require.NoError(t, err)
	assert.Contains(t, string(llms), "https://docs.example.com/after", "llms.txt")

	idx, err := index.Open(filepath.Join(dir, "index.db"), 5, silentLogger())
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()
	require.NoError(t, IndexChunksFromJSONL(context.Background(), idx, "docs", om.jsonlFilePath, silentLogger()))
	results, err := idx.SearchChunks(context.Background(), "pelican", "docs", 5)
	require.NoError(t, err)
	assert.Len(t, results, 1, "chunk index")
}

func TestJSONLScannerLineEndings(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"empty", "", nil},
		{"trailing newline", "a\nb\n", []string{"a", "b"}},
		{"no trailing newline", "a\nb", []string{"a", "b"}},
		{"crlf", "a\r\nb\r\n", []string{"a", "b"}},
		{"blank lines kept", "a\n\nb\n", []string{"a", "", "b"}},
		{"line longer than the buffer", strings.Repeat("x", 3*jsonlReadBuf+5) + "\nend", []string{strings.Repeat("x", 3*jsonlReadBuf+5), "end"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := NewJSONLScanner(strings.NewReader(tc.in))
			var got []string
			for sc.Scan() {
				got = append(got, sc.Text())
			}
			require.NoError(t, sc.Err())
			assert.Equal(t, tc.want, got)
		})
	}
}
