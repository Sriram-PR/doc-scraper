package index

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RecordCrawl writes one crawls row and len(cr.Pages) page_history rows in a
// single transaction, then prunes per-site history to i.retention crawls.
// Safe for concurrent callers (writes serialized via writeMu).
func (i *Index) RecordCrawl(ctx context.Context, cr CrawlRecord) error {
	if cr.SiteKey == "" {
		return fmt.Errorf("site_key is required")
	}
	if cr.Mode == "" {
		return fmt.Errorf("mode is required")
	}
	i.writeMu.Lock()
	defer i.writeMu.Unlock()

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO crawls(site_key, crawl_started_at, crawl_ended_at, total_pages, mode)
		 VALUES (?, ?, ?, ?, ?)`,
		cr.SiteKey,
		cr.CrawlStartedAt.UTC().Format(time.RFC3339Nano),
		cr.CrawlEndedAt.UTC().Format(time.RFC3339Nano),
		len(cr.Pages),
		string(cr.Mode),
	)
	if err != nil {
		return fmt.Errorf("insert crawl: %w", err)
	}
	crawlID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}

	if len(cr.Pages) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO page_history(crawl_id, url, title, content_hash, depth)
			 VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare page insert: %w", err)
		}
		for _, p := range cr.Pages {
			if _, err := stmt.ExecContext(ctx, crawlID, p.URL, p.Title, p.ContentHash, p.Depth); err != nil {
				_ = stmt.Close()
				return fmt.Errorf("insert page %q: %w", p.URL, err)
			}
		}
		if err := stmt.Close(); err != nil {
			return fmt.Errorf("close page stmt: %w", err)
		}
	}

	if err := pruneSite(ctx, tx, cr.SiteKey, i.retention); err != nil {
		return fmt.Errorf("prune: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	i.log.Info("recorded crawl",
		"site_key", cr.SiteKey,
		"crawl_id", crawlID,
		"mode", cr.Mode,
		"pages", len(cr.Pages),
	)
	return nil
}

// pruneSite deletes all but the most recent `keep` crawls for site_key. The FK
// cascade drops the page_history rows. Runs inside the caller's tx.
func pruneSite(ctx context.Context, tx *sql.Tx, siteKey string, keep int) error {
	if keep <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM crawls WHERE id IN (
			SELECT id FROM crawls
			WHERE site_key = ?
			ORDER BY id DESC
			LIMIT -1 OFFSET ?
		)`,
		siteKey, keep,
	)
	return err
}
