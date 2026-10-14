package index

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestIndex(t *testing.T, retention int) *Index {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(path, retention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err, "Open")
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

func mustRecord(t *testing.T, idx *Index, cr CrawlRecord) {
	t.Helper()
	require.NoError(t, idx.RecordCrawl(context.Background(), cr), "RecordCrawl")
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	idx1, err := Open(path, 5, log)
	require.NoError(t, err, "first Open")
	require.NoError(t, idx1.Close(), "first Close")
	idx2, err := Open(path, 5, log)
	require.NoError(t, err, "second Open (existing schema)")
	require.NoError(t, idx2.Close(), "second Close")
}

func TestGetLatestCrawlNoRows(t *testing.T) {
	idx := newTestIndex(t, 5)
	lc, err := idx.GetLatestCrawl(context.Background(), "ghost")
	require.NoError(t, err, "GetLatestCrawl")
	require.Nil(t, lc)
}

func TestRecordAndGetLatest(t *testing.T) {
	idx := newTestIndex(t, 5)
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	mustRecord(t, idx, CrawlRecord{
		SiteKey:        "rust",
		CrawlStartedAt: start,
		CrawlEndedAt:   end,
		Mode:           ModeFull,
		Pages: []PageRecord{
			{URL: "https://a/", Title: "A", ContentHash: "h1", Depth: 0},
			{URL: "https://b/", Title: "B", ContentHash: "h2", Depth: 1},
		},
	})
	lc, err := idx.GetLatestCrawl(context.Background(), "rust")
	require.NoError(t, err, "GetLatestCrawl")
	require.NotNil(t, lc, "expected a row")
	require.Equal(t, 2, lc.TotalPages)
	require.Equal(t, ModeFull, lc.Mode)
	require.True(t, lc.CrawlStartedAt.Equal(start), "started round-trip: got %v", lc.CrawlStartedAt)
	require.True(t, lc.CrawlEndedAt.Equal(end), "ended round-trip: got %v", lc.CrawlEndedAt)
}

func TestDiffSinceNoPriorCrawl(t *testing.T) {
	idx := newTestIndex(t, 5)
	start := time.Now().Add(-time.Hour)
	end := start.Add(time.Minute)
	mustRecord(t, idx, CrawlRecord{
		SiteKey:        "rust",
		CrawlStartedAt: start,
		CrawlEndedAt:   end,
		Mode:           ModeFull,
		Pages:          []PageRecord{{URL: "https://a/", Title: "A", ContentHash: "h1"}},
	})
	// since predates the only crawl → no baseline
	res, err := idx.DiffSince(context.Background(), "rust", start.Add(-time.Hour), 100, 0)
	require.NoError(t, err, "DiffSince")
	require.Nil(t, res.BaselineCrawl)
	require.NotNil(t, res.CurrentCrawl)
	require.Empty(t, res.Entries)
}

func TestDiffSinceAddedChangedRemoved(t *testing.T) {
	idx := newTestIndex(t, 5)
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	// crawl #1
	mustRecord(t, idx, CrawlRecord{
		SiteKey:        "rust",
		CrawlStartedAt: t0,
		CrawlEndedAt:   t0.Add(time.Minute),
		Mode:           ModeFull,
		Pages: []PageRecord{
			{URL: "https://a/", Title: "A", ContentHash: "ha-v1"},
			{URL: "https://b/", Title: "B", ContentHash: "hb-v1"},
			{URL: "https://c/", Title: "C", ContentHash: "hc-v1"},
		},
	})
	// crawl #2: change A, remove B, add D, keep C
	mustRecord(t, idx, CrawlRecord{
		SiteKey:        "rust",
		CrawlStartedAt: t0.Add(time.Hour),
		CrawlEndedAt:   t0.Add(time.Hour + time.Minute),
		Mode:           ModeIncremental,
		Pages: []PageRecord{
			{URL: "https://a/", Title: "A2", ContentHash: "ha-v2"},
			{URL: "https://c/", Title: "C", ContentHash: "hc-v1"},
			{URL: "https://d/", Title: "D", ContentHash: "hd-v1"},
		},
	})

	res, err := idx.DiffSince(context.Background(), "rust", t0.Add(2*time.Minute), 100, 0)
	require.NoError(t, err, "DiffSince")
	require.NotNil(t, res.BaselineCrawl)
	require.NotNil(t, res.CurrentCrawl)
	require.Equal(t, 1, res.UnchangedCount, "expected 1 unchanged (C)")
	require.Equal(t, 3, res.Total)
	require.Len(t, res.Entries, 3)
	// Sort is kind asc then URL asc → added, changed, removed
	want := []DiffEntry{
		{Kind: "added", URL: "https://d/", Title: "D", ContentHash: "hd-v1"},
		{Kind: "changed", URL: "https://a/", Title: "A2", ContentHash: "ha-v2", PriorHash: "ha-v1"},
		{Kind: "removed", URL: "https://b/", Title: "B", ContentHash: "hb-v1"},
	}
	for i, w := range want {
		got := res.Entries[i]
		assert.Equal(t, w.Kind, got.Kind, "entry %d", i)
		assert.Equal(t, w.URL, got.URL, "entry %d", i)
		assert.Equal(t, w.ContentHash, got.ContentHash, "entry %d", i)
		assert.Equal(t, w.PriorHash, got.PriorHash, "entry %d", i)
	}
}

func TestDiffSinceBaselineEqualsCurrent(t *testing.T) {
	idx := newTestIndex(t, 5)
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	mustRecord(t, idx, CrawlRecord{
		SiteKey:        "rust",
		CrawlStartedAt: t0,
		CrawlEndedAt:   t0.Add(time.Minute),
		Mode:           ModeFull,
		Pages:          []PageRecord{{URL: "https://a/", Title: "A", ContentHash: "h1"}},
	})
	// since is after the crawl ended → baseline picks up the same crawl as current → no diff
	res, err := idx.DiffSince(context.Background(), "rust", t0.Add(time.Hour), 100, 0)
	require.NoError(t, err, "DiffSince")
	require.NotNil(t, res.BaselineCrawl)
	require.NotNil(t, res.CurrentCrawl)
	require.Equal(t, res.CurrentCrawl.ID, res.BaselineCrawl.ID, "expected same crawl id")
	require.Empty(t, res.Entries)
	require.Zero(t, res.UnchangedCount)
}

func TestRetentionPruning(t *testing.T) {
	idx := newTestIndex(t, 2) // keep 2 most-recent crawls per site
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for i := range 5 {
		mustRecord(t, idx, CrawlRecord{
			SiteKey:        "rust",
			CrawlStartedAt: t0.Add(time.Duration(i) * time.Hour),
			CrawlEndedAt:   t0.Add(time.Duration(i)*time.Hour + time.Minute),
			Mode:           ModeFull,
			Pages:          []PageRecord{{URL: "https://a/", Title: "A", ContentHash: "h"}},
		})
	}
	var count int
	row := idx.db.QueryRow(`SELECT COUNT(*) FROM crawls WHERE site_key = ?`, "rust")
	require.NoError(t, row.Scan(&count), "count")
	require.Equal(t, 2, count, "expected retention to leave 2 rows")
	// page_history rows should be FK-cascaded
	var pages int
	row = idx.db.QueryRow(`SELECT COUNT(*) FROM page_history`)
	require.NoError(t, row.Scan(&pages), "count pages")
	require.Equal(t, 2, pages, "expected 2 page rows (one per surviving crawl)")
}

func TestMultiSiteIsolation(t *testing.T) {
	idx := newTestIndex(t, 5)
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	mustRecord(t, idx, CrawlRecord{
		SiteKey: "siteA", CrawlStartedAt: t0, CrawlEndedAt: t0.Add(time.Minute),
		Mode: ModeFull, Pages: []PageRecord{{URL: "https://a/", ContentHash: "ha"}},
	})
	mustRecord(t, idx, CrawlRecord{
		SiteKey: "siteB", CrawlStartedAt: t0, CrawlEndedAt: t0.Add(time.Minute),
		Mode: ModeFull, Pages: []PageRecord{{URL: "https://b/", ContentHash: "hb"}},
	})
	a, _ := idx.GetLatestCrawl(context.Background(), "siteA")
	b, _ := idx.GetLatestCrawl(context.Background(), "siteB")
	require.NotNil(t, a)
	require.NotNil(t, b)
	require.NotEqual(t, a.ID, b.ID, "expected distinct crawls per site")
}

func TestRecordCrawlValidation(t *testing.T) {
	idx := newTestIndex(t, 5)
	ctx := context.Background()
	require.ErrorContains(t, idx.RecordCrawl(ctx, CrawlRecord{Mode: ModeFull}), "site_key is required")
	require.ErrorContains(t, idx.RecordCrawl(ctx, CrawlRecord{SiteKey: "x"}), "mode is required")

	var crawls int
	require.NoError(t, idx.db.QueryRow(`SELECT COUNT(*) FROM crawls`).Scan(&crawls))
	assert.Zero(t, crawls, "a rejected record must not be written")
}

func TestNoOrphanedPageHistoryAcrossPooledConnections(t *testing.T) {
	idx := newTestIndex(t, 2)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 18)
	for site := range 3 {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			for i := range 6 {
				cr := CrawlRecord{
					SiteKey:        key,
					CrawlStartedAt: time.Now().Add(time.Duration(i) * time.Second),
					CrawlEndedAt:   time.Now().Add(time.Duration(i)*time.Second + time.Millisecond),
					Mode:           ModeFull,
					Pages: []PageRecord{
						{URL: "https://" + key + "/a", Title: "A", ContentHash: "h", Depth: 1},
						{URL: "https://" + key + "/b", Title: "B", ContentHash: "h", Depth: 1},
					},
				}
				errs <- idx.RecordCrawl(ctx, cr)
			}
		}("site" + string(rune('0'+site)))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	// Retention pruning deletes crawls; ON DELETE CASCADE must have removed
	// their page rows on every pooled connection, not just the one that
	// happened to run a foreign_keys pragma.
	var orphans int
	require.NoError(t, idx.db.QueryRow(
		`SELECT COUNT(*) FROM page_history WHERE crawl_id NOT IN (SELECT id FROM crawls)`).Scan(&orphans))
	require.Zero(t, orphans, "page_history rows must never outlive their crawl")
}

// The cutoff has one-second resolution, matching the whole-second
// last_crawl_ended_at that get_freshness reports, and the latest crawl is chosen
// by time. Comparing the stored RFC3339Nano text fails both: it drops trailing
// zeros, so "10:00:01.1Z" sorts after "10:00:01.15Z".
func TestCrawlAtOrBeforeComparesTimes(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	record := func(idx *Index, ended time.Time) {
		mustRecord(t, idx, CrawlRecord{SiteKey: "rust", CrawlStartedAt: ended.Add(-time.Minute), CrawlEndedAt: ended, Mode: ModeFull})
	}

	t.Run("FreshnessTimestampSelectsItsOwnCrawl", func(t *testing.T) {
		idx := newTestIndex(t, 5)
		ended := t0.Add(300 * time.Millisecond)
		record(idx, ended)

		got, err := idx.crawlAtOrBefore(ctx, "rust", ended.Truncate(time.Second))
		require.NoError(t, err)
		require.NotNil(t, got, "since=last_crawl_ended_at must select that crawl")
		assert.True(t, got.CrawlEndedAt.Equal(ended))
	})

	t.Run("CrawlEndingInALaterSecondIsExcluded", func(t *testing.T) {
		idx := newTestIndex(t, 5)
		record(idx, t0.Add(1200*time.Millisecond))

		got, err := idx.crawlAtOrBefore(ctx, "rust", t0)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("LatestBeforeCutoffWins", func(t *testing.T) {
		idx := newTestIndex(t, 5)
		// The later crawl gets the lower id, so only time ordering can pick it.
		later := t0.Add(1150 * time.Millisecond)
		record(idx, later)
		record(idx, t0.Add(1100*time.Millisecond))

		got, err := idx.crawlAtOrBefore(ctx, "rust", t0.Add(time.Minute))
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.True(t, got.CrawlEndedAt.Equal(later), "picked the crawl ending %v, want %v", got.CrawlEndedAt, later)
	})

	t.Run("TiedEndTimesPickTheNewestRecord", func(t *testing.T) {
		idx := newTestIndex(t, 5)
		ended := t0.Add(500 * time.Millisecond)
		record(idx, ended)
		record(idx, ended)

		got, err := idx.crawlAtOrBefore(ctx, "rust", t0.Add(time.Minute))
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, int64(2), got.ID)
	})
}
