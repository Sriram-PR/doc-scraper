package crawler

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/models"
)

// silentLogger returns a logrus.Entry that discards all output.
func silentLogger() *logrus.Entry {
	lg := logrus.New()
	lg.SetOutput(io.Discard)
	return logrus.NewEntry(lg)
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

func TestFlushBufferedChunks_SortsByURLThenIndex(t *testing.T) {
	tmpDir := t.TempDir()
	chunksPath := filepath.Join(tmpDir, "chunks.jsonl")
	f, err := os.Create(chunksPath)
	require.NoError(t, err)

	// Insertion order shuffled across both axes.
	om := &OutputManager{
		log:            silentLogger(),
		chunksFile:     f,
		chunksFilePath: chunksPath,
		bufferOutput:   true,
		collectedChunks: []models.ChunkJSONL{
			{URL: "https://example.com/beta", ChunkIndex: 1, Content: "b1"},
			{URL: "https://example.com/alpha", ChunkIndex: 2, Content: "a2"},
			{URL: "https://example.com/beta", ChunkIndex: 0, Content: "b0"},
			{URL: "https://example.com/alpha", ChunkIndex: 0, Content: "a0"},
			{URL: "https://example.com/alpha", ChunkIndex: 1, Content: "a1"},
		},
	}

	om.flushBufferedChunks()
	require.NoError(t, f.Close())
	assert.Nil(t, om.collectedChunks)

	got := readChunkOrder(t, chunksPath)
	assert.Equal(t, []string{
		"https://example.com/alpha#0",
		"https://example.com/alpha#1",
		"https://example.com/alpha#2",
		"https://example.com/beta#0",
		"https://example.com/beta#1",
	}, got, "chunks must be ordered by (URL, ChunkIndex)")
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

// readChunkOrder returns "url#index" strings, one per chunk line, in file order.
func readChunkOrder(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var chunk struct {
			URL        string `json:"url"`
			ChunkIndex int    `json:"chunk_index"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &chunk))
		out = append(out, chunk.URL+"#"+itoa(chunk.ChunkIndex))
	}
	require.NoError(t, scanner.Err())
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
