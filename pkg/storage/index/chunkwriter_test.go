package index

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
)

// refReplaceChunks is the per-page write path ChunkWriter replaced: the
// reference its output must match row for row, ids included.
func refReplaceChunks(ctx context.Context, i *Index, siteKey, url, title, contentHash string, chunks []chunk.Chunk) error {
	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE site_key = ? AND url = ?`, siteKey, url); err != nil {
		return err
	}
	for _, c := range chunks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunks (site_key, url, title, heading_path, anchor, seq, content_hash, text, identifiers)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			siteKey, url, title, c.HeadingPath, c.Anchor, c.Seq, contentHash, c.Text, chunk.IdentifierTokens(c.Text)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type testPut struct{ site, url, hash, md string }

func testMarkdown(tag string, sections int) string {
	var b strings.Builder
	for s := range sections {
		fmt.Fprintf(&b, "## %s section %d\n\nThe %s_value parameter controls part %d. %s\n\n",
			tag, s, tag, s, strings.Repeat("filler words here ", 30))
	}
	return b.String()
}

func testPages(site, prefix, tag string, n int) []testPut {
	out := make([]testPut, n)
	for i := range out {
		out[i] = testPut{site, fmt.Sprintf("https://e/%s%d", prefix, i), fmt.Sprintf("%s%d", tag, i), testMarkdown(fmt.Sprintf("%s%d", tag, i), 1+i%4)}
	}
	return out
}

func dumpChunks(t *testing.T, idx *Index) []string {
	t.Helper()
	rows, err := idx.db.Query(`SELECT id, site_key, url, title, heading_path, anchor, seq, content_hash, text, identifiers FROM chunks ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, seq int64
		var site, url, title, hp, anchor, hash, text, ids string
		require.NoError(t, rows.Scan(&id, &site, &url, &title, &hp, &anchor, &seq, &hash, &text, &ids))
		out = append(out, fmt.Sprintf("%d|%s|%s|%s|%s|%s|%d|%s|%s|%s", id, site, url, title, hp, anchor, seq, hash, text, ids))
	}
	require.NoError(t, rows.Err())
	_, err = idx.db.Exec(`INSERT INTO chunks_fts(chunks_fts) VALUES('integrity-check')`)
	require.NoError(t, err, "FTS5 integrity-check")
	return out
}

func TestChunkWriterMatchesPerPageReplace(t *testing.T) {
	ctx := context.Background()
	fresh := testPages("s", "a", "v1-", 10)
	changed := testPages("s", "a", "v2-", 10)
	repeated := []testPut{fresh[0], fresh[1], changed[0], fresh[2], {"s", fresh[0].url, "v3", testMarkdown("v3", 2)}, fresh[3]}
	tiny := func(w *ChunkWriter) {
		w.maxGroupPages, w.maxGroupBytes, w.maxStmtRows, w.maxStmtBytes, w.maxTxAge = 2, 1500, 3, 2500, 0
	}
	cases := []struct {
		name   string
		seed   []testPut
		puts   []testPut
		limits func(*ChunkWriter)
	}{
		{"fresh pages", nil, fresh, nil},
		{"pages that already have chunks", fresh, changed, nil},
		{"some pages already indexed", fresh[:4], append(append([]testPut{}, changed[:2]...), fresh[5:]...), nil},
		{"a URL put more than once", nil, repeated, nil},
		{"a page with no chunks", fresh[:3], []testPut{{"s", fresh[1].url, "empty", ""}, fresh[4]}, nil},
		{"another site untouched", append(testPages("o", "a", "o-", 5), fresh[:3]...), changed, nil},
		{"tiny limits, fresh", nil, fresh, tiny},
		{"tiny limits, replace and repeat", fresh[:6], append(append([]testPut{}, changed...), repeated...), tiny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, got := newTestIndex(t, 5), newTestIndex(t, 5)
			for _, p := range tc.seed {
				for _, idx := range []*Index{ref, got} {
					require.NoError(t, refReplaceChunks(ctx, idx, p.site, p.url, "T "+p.url, p.hash, chunk.Split(p.md)))
				}
			}
			for _, p := range tc.puts {
				require.NoError(t, refReplaceChunks(ctx, ref, p.site, p.url, "T "+p.url, p.hash, chunk.Split(p.md)))
			}
			w := got.NewChunkWriter(ctx, "s")
			if tc.limits != nil {
				tc.limits(w)
			}
			for _, p := range tc.puts {
				require.NoError(t, w.Put(p.url, "T "+p.url, p.hash, chunk.Split(p.md)))
			}
			require.NoError(t, w.Close())
			assert.Equal(t, dumpChunks(t, ref), dumpChunks(t, got))
		})
	}
}

func TestChunkWriterAbortKeepsCommittedPages(t *testing.T) {
	ctx := context.Background()
	idx := newTestIndex(t, 5)
	pages := testPages("s", "a", "v", 3)
	w := idx.NewChunkWriter(ctx, "s")
	w.maxGroupPages, w.maxTxAge = 1, 0
	require.NoError(t, w.Put(pages[0].url, "T", pages[0].hash, chunk.Split(pages[0].md)))
	w.maxTxAge = time.Hour
	require.NoError(t, w.Put(pages[1].url, "T", pages[1].hash, chunk.Split(pages[1].md)))
	require.NoError(t, w.Put(pages[2].url, "T", pages[2].hash, chunk.Split(pages[2].md)))

	w.Abort()
	w.Abort()

	hashes, err := idx.ChunkedHashes(ctx, "s")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{pages[0].url: pages[0].hash}, hashes)
	require.True(t, idx.writeMu.TryLock(), "Abort releases the write lock")
	idx.writeMu.Unlock()
}

func TestChunkWriterPutAfterClose(t *testing.T) {
	idx := newTestIndex(t, 5)
	w := idx.NewChunkWriter(context.Background(), "s")
	require.NoError(t, w.Close())
	require.Error(t, w.Put("https://e/a", "T", "h", chunk.Split(testMarkdown("a", 1))))
	w.Abort()
}

func TestChunkWriterRequiresSiteAndURL(t *testing.T) {
	idx := newTestIndex(t, 5)
	require.Error(t, idx.NewChunkWriter(context.Background(), "").Put("https://e/a", "T", "h", nil))
	require.Error(t, idx.NewChunkWriter(context.Background(), "s").Put("", "T", "h", nil))
}

func TestChunkWriterCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	idx := newTestIndex(t, 5)
	pages := testPages("s", "a", "v", 2)
	w := idx.NewChunkWriter(ctx, "s")
	w.maxGroupPages, w.maxTxAge = 1, 0
	require.NoError(t, w.Put(pages[0].url, "T", pages[0].hash, chunk.Split(pages[0].md)))
	cancel()

	require.ErrorIs(t, w.Put(pages[1].url, "T", pages[1].hash, chunk.Split(pages[1].md)), context.Canceled)
	require.ErrorIs(t, w.Close(), context.Canceled, "later calls return the same error")

	hashes, err := idx.ChunkedHashes(context.Background(), "s")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{pages[0].url: pages[0].hash}, hashes)
	require.True(t, idx.writeMu.TryLock())
	idx.writeMu.Unlock()
}

func TestChunkWriterReleasesTheLockBetweenTransactions(t *testing.T) {
	idx := newTestIndex(t, 5)
	pages := testPages("s", "a", "v", 3)
	w := idx.NewChunkWriter(context.Background(), "s")
	w.maxGroupPages, w.maxTxAge = 1, 0
	for _, p := range pages {
		require.NoError(t, w.Put(p.url, "T", p.hash, chunk.Split(p.md)))
		require.True(t, idx.writeMu.TryLock(), "a committed flush leaves writeMu free for other writers")
		idx.writeMu.Unlock()
	}
	require.NoError(t, w.Close())
}
