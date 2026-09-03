package crawler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func writeChunkTestJSONL(t *testing.T, path string, pages []models.PageJSONL) {
	t.Helper()
	var b strings.Builder
	for _, p := range pages {
		p.RecordType = models.RecordTypePage
		line, err := json.Marshal(p)
		require.NoError(t, err)
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteString(`{"record_type":"crawl_meta","total_pages":` + "0}\n")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
}

func TestIndexChunksFromJSONL(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	idx, err := index.Open(filepath.Join(dir, "index.db"), 5, log)
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()

	ctx := context.Background()
	jsonlPath := filepath.Join(dir, "pages.jsonl")
	filler := strings.Repeat("filler ", 40)

	writeChunkTestJSONL(t, jsonlPath, []models.PageJSONL{
		{URL: "https://d.example/config", Title: "Config", ContentHash: "c1",
			Content: "# Config\n\nRetry limits live under max_retries. " + filler},
		{URL: "https://d.example/install", Title: "Install", ContentHash: "i1",
			Content: "# Install\n\nDownload the walrus binary. " + filler},
	})

	require.NoError(t, IndexChunksFromJSONL(ctx, idx, "docs", jsonlPath, log))

	results, err := idx.SearchChunks(ctx, "walrus", "docs", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "https://d.example/install", results[0].URL)

	// Second pass: config page changed, install page removed, a new page added.
	writeChunkTestJSONL(t, jsonlPath, []models.PageJSONL{
		{URL: "https://d.example/config", Title: "Config", ContentHash: "c2",
			Content: "# Config\n\nQuokka settings replaced everything. " + filler},
		{URL: "https://d.example/auth", Title: "Auth", ContentHash: "a1",
			Content: "# Auth\n\nPelican tokens authenticate requests. " + filler},
	})
	require.NoError(t, IndexChunksFromJSONL(ctx, idx, "docs", jsonlPath, log))

	for query, wantURL := range map[string]string{
		"quokka":  "https://d.example/config",
		"pelican": "https://d.example/auth",
	} {
		results, err = idx.SearchChunks(ctx, query, "docs", 5)
		require.NoError(t, err)
		require.Len(t, results, 1, "query %q", query)
		assert.Equal(t, wantURL, results[0].URL)
	}
	for _, gone := range []string{"walrus", "retry"} {
		results, err = idx.SearchChunks(ctx, gone, "docs", 5)
		require.NoError(t, err)
		assert.Empty(t, results, "stale term %q must be gone", gone)
	}

	hashes, err := idx.ChunkedHashes(ctx, "docs")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"https://d.example/config": "c2",
		"https://d.example/auth":   "a1",
	}, hashes)
}

func TestIndexChunksFromJSONLNilIndexAndMissingFile(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NoError(t, IndexChunksFromJSONL(context.Background(), nil, "docs", "/nope/pages.jsonl", log))

	dir := t.TempDir()
	idx, err := index.Open(filepath.Join(dir, "index.db"), 5, log)
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()
	assert.NoError(t, IndexChunksFromJSONL(context.Background(), idx, "docs", filepath.Join(dir, "missing.jsonl"), log))
}
