package main

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCorpus_SearchesFrozenJSONL(t *testing.T) {
	ctx := context.Background()
	c, err := loadCorpus(ctx, "testdata", "mini")
	require.NoError(t, err)

	assert.Equal(t, map[string]struct{}{"https://mini.example/install": {}, "https://mini.example/retries": {}}, c.pages)
	assert.Equal(t, 2, c.fp.Pages)
	assert.Positive(t, c.fp.Chunks)
	assert.Len(t, c.fp.SHA256, 64)

	hits, err := c.search(ctx, "exponentially")
	require.NoError(t, err)
	r := scoreQuery(hits, []target{{URL: "https://mini.example/retries"}})
	assert.Equal(t, 1, r.PageRank)

	tmp := c.tmp
	require.NoError(t, c.close())
	_, err = os.Stat(tmp)
	assert.True(t, os.IsNotExist(err), "temp dir should be removed, stat err = %v", err)
}

func TestLoadCorpus_MissingFile(t *testing.T) {
	_, err := loadCorpus(context.Background(), t.TempDir(), "absent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "corpus absent")
	assert.Contains(t, err.Error(), "-site absent")
}

func TestLoadCorpus_NoPages(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/empty", 0o755))
	require.NoError(t, os.WriteFile(dir+"/empty/pages.jsonl", []byte(`{"record_type":"crawl_meta"}`+"\n"), 0o644))
	_, err := loadCorpus(context.Background(), dir, "empty")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no page records")
}

func TestLoadCorpus_CountsOnlyRecordsTheIndexerReads(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/spaced", 0o755))
	line := `{"record_type": "page", "url": "https://x.example/a", "content": "# A\n\nText."}` + "\n"
	require.NoError(t, os.WriteFile(dir+"/spaced/pages.jsonl", []byte(line), 0o644))
	_, err := loadCorpus(context.Background(), dir, "spaced")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no page records")
}
