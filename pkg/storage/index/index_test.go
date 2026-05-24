package index

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func newTestIndex(t *testing.T, retention int) *Index {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(path, retention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

func mustRecord(t *testing.T, idx *Index, cr CrawlRecord) {
	t.Helper()
	if err := idx.RecordCrawl(context.Background(), cr); err != nil {
		t.Fatalf("RecordCrawl: %v", err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	idx1, err := Open(path, 5, log)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := idx1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	idx2, err := Open(path, 5, log)
	if err != nil {
		t.Fatalf("second Open (existing schema): %v", err)
	}
	if err := idx2.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestGetLatestCrawlNoRows(t *testing.T) {
	idx := newTestIndex(t, 5)
	lc, err := idx.GetLatestCrawl(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("GetLatestCrawl: %v", err)
	}
	if lc != nil {
		t.Fatalf("expected nil, got %+v", lc)
	}
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
	if err != nil {
		t.Fatalf("GetLatestCrawl: %v", err)
	}
	if lc == nil {
		t.Fatalf("expected a row")
	}
	if lc.TotalPages != 2 || lc.Mode != ModeFull {
		t.Fatalf("unexpected: %+v", lc)
	}
	if !lc.CrawlStartedAt.Equal(start) || !lc.CrawlEndedAt.Equal(end) {
		t.Fatalf("timestamps round-trip wrong: started=%v ended=%v", lc.CrawlStartedAt, lc.CrawlEndedAt)
	}
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
	if err != nil {
		t.Fatalf("DiffSince: %v", err)
	}
	if res.BaselineCrawl != nil {
		t.Fatalf("expected nil baseline, got %+v", res.BaselineCrawl)
	}
	if res.CurrentCrawl == nil {
		t.Fatalf("expected current crawl")
	}
	if len(res.Entries) != 0 {
		t.Fatalf("expected empty entries, got %d", len(res.Entries))
	}
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
	if err != nil {
		t.Fatalf("DiffSince: %v", err)
	}
	if res.BaselineCrawl == nil || res.CurrentCrawl == nil {
		t.Fatalf("expected both crawls present")
	}
	if res.UnchangedCount != 1 {
		t.Fatalf("expected 1 unchanged (C), got %d", res.UnchangedCount)
	}
	if res.Total != 3 || len(res.Entries) != 3 {
		t.Fatalf("expected 3 entries, got total=%d len=%d", res.Total, len(res.Entries))
	}
	// Sort is kind asc then URL asc → added, changed, removed
	want := []DiffEntry{
		{Kind: "added", URL: "https://d/", Title: "D", ContentHash: "hd-v1"},
		{Kind: "changed", URL: "https://a/", Title: "A2", ContentHash: "ha-v2", PriorHash: "ha-v1"},
		{Kind: "removed", URL: "https://b/", Title: "B", ContentHash: "hb-v1"},
	}
	for i, w := range want {
		got := res.Entries[i]
		if got.Kind != w.Kind || got.URL != w.URL || got.ContentHash != w.ContentHash || got.PriorHash != w.PriorHash {
			t.Errorf("entry %d: got %+v want %+v", i, got, w)
		}
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
	if err != nil {
		t.Fatalf("DiffSince: %v", err)
	}
	if res.BaselineCrawl == nil || res.CurrentCrawl == nil {
		t.Fatalf("expected both crawls present")
	}
	if res.BaselineCrawl.ID != res.CurrentCrawl.ID {
		t.Fatalf("expected same crawl id")
	}
	if len(res.Entries) != 0 || res.UnchangedCount != 0 {
		t.Fatalf("expected empty diff, got entries=%d unchanged=%d", len(res.Entries), res.UnchangedCount)
	}
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
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected retention to leave 2 rows, got %d", count)
	}
	// page_history rows should be FK-cascaded
	var pages int
	row = idx.db.QueryRow(`SELECT COUNT(*) FROM page_history`)
	if err := row.Scan(&pages); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if pages != 2 {
		t.Fatalf("expected 2 page rows (one per surviving crawl), got %d", pages)
	}
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
	if a == nil || b == nil || a.ID == b.ID {
		t.Fatalf("expected distinct crawls per site, got a=%+v b=%+v", a, b)
	}
}

func TestRecordCrawlValidation(t *testing.T) {
	idx := newTestIndex(t, 5)
	if err := idx.RecordCrawl(context.Background(), CrawlRecord{}); err == nil {
		t.Fatalf("expected error on empty site_key")
	}
	if err := idx.RecordCrawl(context.Background(), CrawlRecord{SiteKey: "x"}); err == nil {
		t.Fatalf("expected error on empty mode")
	}
}
