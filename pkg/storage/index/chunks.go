package index

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
)

// SearchResult is one ranked full-text hit. Rank is the raw BM25 score from
// SQLite (more negative = better match); Snippet has match terms wrapped in
// [ ] and elisions marked with an ellipsis. Anchor is the matched section's
// URL fragment; append it to URL as "#anchor" to deep-link the section.
type SearchResult struct {
	SiteKey     string  `json:"site_key"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	HeadingPath string  `json:"heading_path,omitempty"`
	Anchor      string  `json:"anchor,omitempty"`
	Snippet     string  `json:"snippet"`
	Rank        float64 `json:"rank"`
}

// ReplaceChunks swaps a page's chunks for freshly split ones in one
// transaction. contentHash records which page content the chunks came from,
// so unchanged pages can be skipped on the next crawl.
func (i *Index) ReplaceChunks(ctx context.Context, siteKey, url, title, contentHash string, chunks []chunk.Chunk) error {
	if siteKey == "" || url == "" {
		return fmt.Errorf("site_key and url are required")
	}
	i.writeMu.Lock()
	defer i.writeMu.Unlock()

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks WHERE site_key = ? AND url = ?`, siteKey, url); err != nil {
		return fmt.Errorf("delete old chunks: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO chunks (site_key, url, title, heading_path, anchor, seq, content_hash, text, identifiers)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, c := range chunks {
		identifiers := chunk.IdentifierTokens(c.Text)
		if _, err := stmt.ExecContext(ctx, siteKey, url, title, c.HeadingPath, c.Anchor, c.Seq, contentHash, c.Text, identifiers); err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ChunkedHashes returns url -> content_hash for every page of a site that has
// chunks, letting callers skip re-chunking unchanged pages.
func (i *Index) ChunkedHashes(ctx context.Context, siteKey string) (map[string]string, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT DISTINCT url, content_hash FROM chunks WHERE site_key = ?`, siteKey)
	if err != nil {
		return nil, fmt.Errorf("query chunked hashes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]string)
	for rows.Next() {
		var url, hash string
		if err := rows.Scan(&url, &hash); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out[url] = hash
	}
	return out, rows.Err()
}

// PruneChunks removes chunks for pages no longer in the site's corpus.
// currentURLs is the complete set of URLs the latest crawl produced.
func (i *Index) PruneChunks(ctx context.Context, siteKey string, currentURLs map[string]struct{}) error {
	existing, err := i.ChunkedHashes(ctx, siteKey)
	if err != nil {
		return err
	}
	var stale []string
	for url := range existing {
		if _, ok := currentURLs[url]; !ok {
			stale = append(stale, url)
		}
	}
	if len(stale) == 0 {
		return nil
	}

	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, url := range stale {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM chunks WHERE site_key = ? AND url = ?`, siteKey, url); err != nil {
			return fmt.Errorf("prune chunks: %w", err)
		}
	}
	return tx.Commit()
}

// SiteHasChunks reports whether any chunks exist for the site, used to decide
// whether an upgrade backfill is needed.
func (i *Index) SiteHasChunks(ctx context.Context, siteKey string) (bool, error) {
	var one int
	err := i.db.QueryRowContext(ctx,
		`SELECT 1 FROM chunks WHERE site_key = ? LIMIT 1`, siteKey).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query chunks: %w", err)
	}
	return true, nil
}

// SearchChunks runs a ranked full-text query. siteKey narrows to one site when
// non-empty. The query is tried verbatim first so phrase and prefix syntax
// work; if FTS5 rejects it (unbalanced quotes and similar), it is retried with
// every term quoted, so arbitrary agent input never surfaces a syntax error.
func (i *Index) SearchChunks(ctx context.Context, query, siteKey string, limit int) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 10
	}

	results, err := i.searchChunksRaw(ctx, query, siteKey, limit)
	if err != nil && looksLikeFTSSyntaxError(err) {
		return i.searchChunksRaw(ctx, quoteFTSTerms(query), siteKey, limit)
	}
	return results, err
}

func (i *Index) searchChunksRaw(ctx context.Context, match, siteKey string, limit int) ([]SearchResult, error) {
	// Weights: title 4, heading path 2, body 1, split identifiers 1 - a match
	// in the page title or section heading is a stronger signal than one in
	// running text.
	q := `SELECT c.site_key, c.url, c.title, c.heading_path, c.anchor,
	             snippet(chunks_fts, 2, '[', ']', '...', 12),
	             bm25(chunks_fts, 4.0, 2.0, 1.0, 1.0) AS rank
	      FROM chunks_fts
	      JOIN chunks c ON c.id = chunks_fts.rowid
	      WHERE chunks_fts MATCH ?`
	args := []any{match}
	if siteKey != "" {
		q += ` AND c.site_key = ?`
		args = append(args, siteKey)
	}
	q += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)

	rows, err := i.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.SiteKey, &r.URL, &r.Title, &r.HeadingPath, &r.Anchor, &r.Snippet, &r.Rank); err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func looksLikeFTSSyntaxError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "fts5: syntax error") ||
		strings.Contains(msg, "unterminated string") ||
		strings.Contains(msg, "no such column")
}

// quoteFTSTerms rebuilds the query as quoted bareword terms, neutralizing
// FTS5 operators and stray punctuation.
func quoteFTSTerms(query string) string {
	terms := strings.Fields(query)
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		t = strings.ReplaceAll(t, `"`, `""`)
		quoted = append(quoted, `"`+t+`"`)
	}
	return strings.Join(quoted, " ")
}
