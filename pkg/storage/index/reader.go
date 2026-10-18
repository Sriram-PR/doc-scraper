package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

const crawlColumns = `id, site_key, crawl_started_at, crawl_ended_at, total_pages, mode`

// GetLatestCrawl returns the most recent crawl for siteKey, or (nil, nil) if
// none exists.
func (i *Index) GetLatestCrawl(ctx context.Context, siteKey string) (*LatestCrawl, error) {
	row := i.db.QueryRowContext(ctx,
		`SELECT `+crawlColumns+` FROM crawls WHERE site_key = ? ORDER BY id DESC LIMIT 1`,
		siteKey,
	)
	lc, err := scanCrawl(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan latest crawl: %w", err)
	}
	return lc, nil
}

func scanCrawl(row interface{ Scan(...any) error }) (*LatestCrawl, error) {
	var lc LatestCrawl
	var startedAt, endedAt, mode string
	if err := row.Scan(&lc.ID, &lc.SiteKey, &startedAt, &endedAt, &lc.TotalPages, &mode); err != nil {
		return nil, err
	}
	t, err := parseRFC3339(startedAt)
	if err != nil {
		return nil, fmt.Errorf("parse crawl_started_at: %w", err)
	}
	lc.CrawlStartedAt = t
	t, err = parseRFC3339(endedAt)
	if err != nil {
		return nil, fmt.Errorf("parse crawl_ended_at: %w", err)
	}
	lc.CrawlEndedAt = t
	lc.Mode = Mode(mode)
	return &lc, nil
}

// DiffSince compares the current crawl against the most recent crawl whose
// crawl_ended_at is <= since, and returns added/removed/changed pages plus the
// count of unchanged ones. If no baseline exists (since is older than the
// earliest crawl, or the site has only ever been crawled once after since),
// BaselineCrawl is nil and Entries is empty. Pagination is applied to a stable
// ordering: Kind alphabetical (added, changed, removed) then URL alphabetical.
func (i *Index) DiffSince(ctx context.Context, siteKey string, since time.Time, maxResults, offset int) (*DiffResult, error) {
	return i.diff(ctx, siteKey, maxResults, offset, func(*LatestCrawl) (*LatestCrawl, error) {
		return i.crawlAtOrBefore(ctx, siteKey, since)
	})
}

// DiffPrevious is DiffSince with the crawl recorded just before the current
// one as the baseline; BaselineCrawl is nil when there is only one crawl.
func (i *Index) DiffPrevious(ctx context.Context, siteKey string, maxResults, offset int) (*DiffResult, error) {
	return i.diff(ctx, siteKey, maxResults, offset, func(current *LatestCrawl) (*LatestCrawl, error) {
		return i.crawlBefore(ctx, siteKey, current)
	})
}

func (i *Index) diff(ctx context.Context, siteKey string, maxResults, offset int, baselineFor func(*LatestCrawl) (*LatestCrawl, error)) (*DiffResult, error) {
	if maxResults <= 0 {
		maxResults = 100
	}
	if maxResults > 1000 {
		maxResults = 1000
	}
	if offset < 0 {
		offset = 0
	}

	current, err := i.GetLatestCrawl(ctx, siteKey)
	if err != nil {
		return nil, err
	}
	res := &DiffResult{CurrentCrawl: current}
	if current == nil {
		return res, nil
	}

	baseline, err := baselineFor(current)
	if err != nil {
		return nil, err
	}
	res.BaselineCrawl = baseline
	if baseline == nil || baseline.ID == current.ID {
		return res, nil
	}

	basePages, err := i.pagesFor(ctx, baseline.ID)
	if err != nil {
		return nil, fmt.Errorf("load baseline pages: %w", err)
	}
	currPages, err := i.pagesFor(ctx, current.ID)
	if err != nil {
		return nil, fmt.Errorf("load current pages: %w", err)
	}

	entries, unchanged := diffPageSets(basePages, currPages)
	res.UnchangedCount = unchanged
	res.Total = len(entries)

	sort.Slice(entries, func(a, b int) bool {
		if entries[a].Kind != entries[b].Kind {
			return entries[a].Kind < entries[b].Kind
		}
		return entries[a].URL < entries[b].URL
	})

	if offset >= len(entries) {
		res.Entries = []DiffEntry{}
		return res, nil
	}
	end := min(offset+maxResults, len(entries))
	res.Entries = entries[offset:end]
	return res, nil
}

// crawlBefore returns the crawl recorded just before current, or (nil, nil)
// if current is the only one. Ordered by id, as GetLatestCrawl is.
func (i *Index) crawlBefore(ctx context.Context, siteKey string, current *LatestCrawl) (*LatestCrawl, error) {
	row := i.db.QueryRowContext(ctx,
		`SELECT `+crawlColumns+` FROM crawls WHERE site_key = ? AND id < ? ORDER BY id DESC LIMIT 1`,
		siteKey, current.ID,
	)
	lc, err := scanCrawl(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan previous crawl: %w", err)
	}
	return lc, nil
}

// crawlAtOrBefore returns the most recent crawl whose crawl_ended_at <= cutoff,
// or (nil, nil) if none exists. Compared as parsed times because RFC3339Nano
// text does not sort chronologically, and at whole seconds because that is
// the precision get_freshness reports.
func (i *Index) crawlAtOrBefore(ctx context.Context, siteKey string, cutoff time.Time) (*LatestCrawl, error) {
	rows, err := i.db.QueryContext(ctx, `SELECT `+crawlColumns+` FROM crawls WHERE site_key = ?`, siteKey)
	if err != nil {
		return nil, fmt.Errorf("query crawls: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var best *LatestCrawl
	for rows.Next() {
		lc, err := scanCrawl(rows)
		if err != nil {
			return nil, fmt.Errorf("scan baseline crawl: %w", err)
		}
		if lc.CrawlEndedAt.Truncate(time.Second).After(cutoff) {
			continue
		}
		if best == nil || lc.CrawlEndedAt.After(best.CrawlEndedAt) ||
			(lc.CrawlEndedAt.Equal(best.CrawlEndedAt) && lc.ID > best.ID) {
			best = lc
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crawls: %w", err)
	}
	return best, nil
}

// pagesFor returns the page_history rows for crawlID keyed by URL.
func (i *Index) pagesFor(ctx context.Context, crawlID int64) (map[string]PageRecord, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT url, title, content_hash, depth FROM page_history WHERE crawl_id = ?`,
		crawlID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]PageRecord, 256)
	for rows.Next() {
		var p PageRecord
		if err := rows.Scan(&p.URL, &p.Title, &p.ContentHash, &p.Depth); err != nil {
			return nil, err
		}
		out[p.URL] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// diffPageSets returns the (added, removed, changed) verdict + unchanged count
// from comparing two URL-keyed maps. Pure function for ease of testing.
func diffPageSets(baseline, current map[string]PageRecord) ([]DiffEntry, int) {
	entries := make([]DiffEntry, 0)
	unchanged := 0
	for url, c := range current {
		b, existed := baseline[url]
		switch {
		case !existed:
			entries = append(entries, DiffEntry{
				Kind:        "added",
				URL:         url,
				Title:       c.Title,
				ContentHash: c.ContentHash,
				Depth:       c.Depth,
			})
		case b.ContentHash != c.ContentHash:
			entries = append(entries, DiffEntry{
				Kind:        "changed",
				URL:         url,
				Title:       c.Title,
				ContentHash: c.ContentHash,
				PriorHash:   b.ContentHash,
				Depth:       c.Depth,
			})
		default:
			unchanged++
		}
	}
	for url, b := range baseline {
		if _, stillThere := current[url]; stillThere {
			continue
		}
		entries = append(entries, DiffEntry{
			Kind:        "removed",
			URL:         url,
			Title:       b.Title,
			ContentHash: b.ContentHash,
			Depth:       b.Depth,
		})
	}
	return entries, unchanged
}

// parseRFC3339 accepts both RFC3339 and RFC3339Nano (which is what we write).
func parseRFC3339(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
