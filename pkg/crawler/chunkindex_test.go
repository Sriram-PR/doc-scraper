package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
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

	indexed, err := idx.ChunkedPages(ctx, "docs")
	require.NoError(t, err)
	assert.Equal(t, map[string]index.ChunkedPage{
		"https://d.example/config": {ContentHash: "c2", Title: "Config"},
		"https://d.example/auth":   {ContentHash: "a1", Title: "Auth"},
	}, indexed)
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

func TestIndexChunksFromJSONLDeletedFileClearsTheSite(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	idx, err := index.Open(filepath.Join(dir, "index.db"), 5, log)
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()
	ctx := context.Background()
	jsonlPath := filepath.Join(dir, "pages.jsonl")
	writeChunkTestJSONL(t, jsonlPath, []models.PageJSONL{
		{URL: "https://d.example/install", Title: "Install", ContentHash: "i1",
			Content: "# Install\n\nDownload the walrus binary. " + strings.Repeat("filler ", 40)},
	})
	require.NoError(t, IndexChunksFromJSONL(ctx, idx, "docs", jsonlPath, log))
	require.NoError(t, idx.ReplaceChunks(ctx, "other", "https://o.example/a", "A", "a1", chunk.Split("# A\n\nAnother walrus. "+strings.Repeat("filler ", 40))))

	require.NoError(t, os.Remove(jsonlPath))
	require.NoError(t, IndexChunksFromJSONL(ctx, idx, "docs", jsonlPath, log))

	results, err := idx.SearchChunks(ctx, "walrus", "docs", 5)
	require.NoError(t, err)
	assert.Empty(t, results, "chunks of a site whose JSONL was deleted are still searchable")
	results, err = idx.SearchChunks(ctx, "walrus", "other", 5)
	require.NoError(t, err)
	assert.Len(t, results, 1, "another site's chunks must stay")
}

func TestIndexChunksFromJSONLLogsProgress(t *testing.T) {
	old := indexProgressInterval
	indexProgressInterval = 0
	t.Cleanup(func() { indexProgressInterval = old })

	dir := t.TempDir()
	idx, err := index.Open(filepath.Join(dir, "index.db"), 5, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()
	ctx := context.Background()
	jsonlPath := filepath.Join(dir, "pages.jsonl")
	filler := strings.Repeat("filler ", 40)
	pages := []models.PageJSONL{
		{URL: "https://d.example/a", Title: "A", ContentHash: "a1", Content: "# A\n\nalpha. " + filler},
		{URL: "https://d.example/b", Title: "B", ContentHash: "b1", Content: "# B\n\nbravo. " + filler},
		{URL: "https://d.example/c", Title: "C", ContentHash: "c1", Content: "# C\n\ncharlie. " + filler},
	}
	run := func() string {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).With("site_key", "docs")
		require.NoError(t, IndexChunksFromJSONL(ctx, idx, "docs", jsonlPath, log))
		return buf.String()
	}

	writeChunkTestJSONL(t, jsonlPath, pages)
	out := run()
	assert.NotContains(t, out, " site=", "the caller's logger names the site")
	assert.Equal(t, 4, strings.Count(out, `level=INFO msg="Indexing pages for search" site_key=docs`), "a start line and a progress line per page:\n%s", out)
	assert.Contains(t, out, `level=INFO msg="Indexed pages for search" site_key=docs pages_indexed=3 pages_unchanged=0 pages_removed=0`)

	writeChunkTestJSONL(t, jsonlPath, pages[:2])
	out = run()
	assert.NotContains(t, out, `msg="Indexing pages for search"`, "nothing to index, no start line")
	assert.Contains(t, out, `level=INFO msg="Indexed pages for search" site_key=docs pages_indexed=0 pages_unchanged=2 pages_removed=1`)

	out = run()
	assert.NotContains(t, out, "level=INFO")
	assert.Contains(t, out, `level=DEBUG msg="Indexed pages for search" site_key=docs pages_indexed=0 pages_unchanged=2 pages_removed=0`)
}

func openChunkTestIndex(t *testing.T) (*index.Index, string) {
	t.Helper()
	dir := t.TempDir()
	idx, err := index.Open(filepath.Join(dir, "index.db"), 5, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	return idx, dir
}

// indexAndCount runs IndexChunksFromJSONL and returns the pages_indexed and
// pages_unchanged counts from its end line.
func indexAndCount(t *testing.T, idx *index.Index, jsonlPath string) (indexed, unchanged int) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	require.NoError(t, IndexChunksFromJSONL(context.Background(), idx, "docs", jsonlPath, log))
	_, err := fmt.Sscanf(buf.String()[strings.Index(buf.String(), "pages_indexed="):], "pages_indexed=%d pages_unchanged=%d", &indexed, &unchanged)
	require.NoError(t, err, buf.String())
	return indexed, unchanged
}

func TestIndexChunksFromJSONLFollowsTitleChanges(t *testing.T) {
	idx, dir := openChunkTestIndex(t)
	ctx := context.Background()
	jsonlPath := filepath.Join(dir, "pages.jsonl")
	page := models.PageJSONL{URL: "https://d.example/p1", Title: "P1", ContentHash: "h1",
		Content: "# Intro\n\nBody text. " + strings.Repeat("filler ", 40)}
	writeChunkTestJSONL(t, jsonlPath, []models.PageJSONL{page})

	indexed, _ := indexAndCount(t, idx, jsonlPath)
	assert.Equal(t, 1, indexed)
	format, err := idx.ChunkFormat(ctx, "docs")
	require.NoError(t, err)
	assert.Equal(t, chunk.FormatVersion, format, "the first full index stamps the format")

	indexed, unchanged := indexAndCount(t, idx, jsonlPath)
	assert.Equal(t, 0, indexed)
	assert.Equal(t, 1, unchanged, "same content and title is skipped")

	page.Title = "ZetaTitle"
	writeChunkTestJSONL(t, jsonlPath, []models.PageJSONL{page})
	indexed, unchanged = indexAndCount(t, idx, jsonlPath)
	assert.Equal(t, 1, indexed, "a title-only change is re-indexed")
	assert.Equal(t, 0, unchanged)
	results, err := idx.SearchChunks(ctx, "ZetaTitle", "docs", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "ZetaTitle", results[0].Title)
}

func TestIndexChunksFromJSONLRechunksAnOldFormat(t *testing.T) {
	idx, dir := openChunkTestIndex(t)
	ctx := context.Background()
	jsonlPath := filepath.Join(dir, "pages.jsonl")
	filler := strings.Repeat("filler ", 40)
	writeChunkTestJSONL(t, jsonlPath, []models.PageJSONL{
		{URL: "https://d.example/a", Title: "A", ContentHash: "a1", Content: "# A\n\nalpha. " + filler},
		{URL: "https://d.example/b", Title: "B", ContentHash: "b1", Content: "# B\n\nbravo. " + filler},
	})
	indexAndCount(t, idx, jsonlPath)
	require.NoError(t, idx.SetChunkFormat(ctx, "docs", chunk.FormatVersion-1))

	indexed, unchanged := indexAndCount(t, idx, jsonlPath)
	assert.Equal(t, 2, indexed, "every page is re-chunked once")
	assert.Equal(t, 0, unchanged)
	format, err := idx.ChunkFormat(ctx, "docs")
	require.NoError(t, err)
	assert.Equal(t, chunk.FormatVersion, format)

	indexed, unchanged = indexAndCount(t, idx, jsonlPath)
	assert.Equal(t, 0, indexed)
	assert.Equal(t, 2, unchanged)
}

func TestIndexChunksFromJSONLFailureKeepsTheOldFormat(t *testing.T) {
	idx, dir := openChunkTestIndex(t)
	ctx := context.Background()
	require.NoError(t, idx.SetChunkFormat(ctx, "docs", chunk.FormatVersion-1))

	// Opening a directory works; reading it fails, so the scan stops with an error.
	err := IndexChunksFromJSONL(ctx, idx, "docs", dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Error(t, err)

	format, err := idx.ChunkFormat(ctx, "docs")
	require.NoError(t, err)
	assert.Equal(t, chunk.FormatVersion-1, format)
}
