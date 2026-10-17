package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
)

// SearchResult is one ranked full-text hit. Rank is the raw BM25 score from
// SQLite (more negative = better match) for the query stage that found it, so
// it compares only between results with the same Match; Snippet has match
// terms wrapped in [ ] and elisions marked with an ellipsis. Anchor is the
// matched section's URL fragment; append it to URL as "#anchor" to deep-link
// the section.
type SearchResult struct {
	SiteKey     string  `json:"site_key"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	HeadingPath string  `json:"heading_path,omitempty"`
	Anchor      string  `json:"anchor,omitempty"`
	Snippet     string  `json:"snippet"`
	Rank        float64 `json:"rank"`
	Match       string  `json:"match"`
}

// Match values: MatchAll results contain every query term other than
// stopwords; MatchPartial results come from the synonym or OR fallback and may
// lack some of the terms as typed.
const (
	MatchAll     = "all"
	MatchPartial = "partial"
)

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

// DeleteSiteChunks removes every chunk for the site, for a site whose corpus is no
// longer indexed.
func (i *Index) DeleteSiteChunks(ctx context.Context, siteKey string) error {
	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	if _, err := i.db.ExecContext(ctx, `DELETE FROM chunks WHERE site_key = ?`, siteKey); err != nil {
		return fmt.Errorf("delete site chunks: %w", err)
	}
	return nil
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

// ErrNoSearchableTerms is returned for queries containing no letters or digits.
var ErrNoSearchableTerms = errors.New("query has no searchable terms")

// SearchChunks runs a ranked full-text query. siteKey narrows to one site when
// non-empty. The query is tried verbatim first so phrase and prefix syntax
// work; if FTS5 rejects it (unbalanced quotes and similar), it is retried with
// every term quoted, so arbitrary agent input never surfaces a syntax error.
// When that returns fewer than limit results, relaxed rewrites fill the rest
// after them. The slice order is the ranking; Rank is comparable only within
// one Match level.
func (i *Index) SearchChunks(ctx context.Context, query, siteKey string, limit int) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	query = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, query))
	if !strings.ContainsFunc(query, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return nil, ErrNoSearchableTerms
	}
	if limit <= 0 {
		limit = 10
	}

	return searchStaged(ctx, query, siteKey, limit, i.searchChunksRaw)
}

type rawSearch func(ctx context.Context, match, siteKey string, limit int, exclude []int64) ([]SearchResult, []int64, error)

func searchStaged(ctx context.Context, query, siteKey string, limit int, raw rawSearch) ([]SearchResult, error) {
	results, ids, err := raw(ctx, query, siteKey, limit, nil)
	if err != nil && looksLikeFTSSyntaxError(err) {
		results, ids, err = raw(ctx, quoteFTSTerms(query), siteKey, limit, nil)
	}
	if err != nil {
		return nil, err
	}
	setMatch(results, MatchAll)
	// FTS5 ANDs plain terms, so a natural-language query ("how do I group
	// commands") often matches little or nothing even when its meaningful
	// words are all over the corpus, and a strict match on a table-of-contents
	// page can be the only hit. Fill progressively: AND of the content words
	// first (stopwords stripped, since on a docs corpus "how do I" is rare
	// enough that BM25 would rank literal question phrasings over the real
	// answer), then OR as the last resort.
	for _, rw := range relaxedFTSQueries(query) {
		if len(results) >= limit {
			break
		}
		more, moreIDs, err := raw(ctx, rw.match, siteKey, limit-len(results), ids)
		if err != nil {
			return nil, err
		}
		if rw.partial {
			setMatch(more, MatchPartial)
		} else {
			setMatch(more, MatchAll)
		}
		results = append(results, more...)
		ids = append(ids, moreIDs...)
	}
	return results, nil
}

func setMatch(results []SearchResult, match string) {
	for i := range results {
		results[i].Match = match
	}
}

var ftsStopwords = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(
		"a an and are as at be but by can could do does for from get has have how i in is it its me my of on or " +
			"our should that the their there these this to use using want was we what when where which who why will with you your") {
		ftsStopwords[w] = struct{}{}
	}
}

type ftsRewrite struct {
	match string
	// partial marks a synonym or OR rewrite, whose matches may lack some query terms.
	partial bool
}

// ftsColumnFilter matches FTS5 column filters ("title:x", "{title text}: x",
// "-text:x") on the chunks_fts columns; other colons ("std::env") are text.
var ftsColumnFilter = regexp.MustCompile(`(?i)(^|[\s{-])(title|heading_path|text|identifiers)\s*}?\s*:`)

// relaxedFTSQueries returns fallback query rewrites for a plain query, in
// decreasing precision: content-word AND, synonym AND, then OR (with synonyms).
// A single word gets a rewrite only when it has synonyms. Queries using
// explicit FTS5 syntax (quotes, prefixes, grouping, operators, column filters)
// are taken as intentional and never rewritten.
func relaxedFTSQueries(query string) []ftsRewrite {
	if strings.ContainsAny(query, `"*()`) || ftsColumnFilter.MatchString(query) {
		return nil
	}
	terms := strings.Fields(query)
	var content []string
	for _, t := range terms {
		switch t {
		case "OR", "AND", "NOT", "NEAR":
			return nil
		}
		if !isStopword(t) {
			content = append(content, t)
		}
	}
	// Expand over all terms so synonym phrases containing a stopword ("log in")
	// match; stopwords left over on their own are then dropped.
	groups := slices.DeleteFunc(expandTerms(terms), func(g termGroup) bool { return len(g.alts) == 0 && isStopword(g.text) })
	expanded := slices.ContainsFunc(groups, func(g termGroup) bool { return len(g.alts) > 0 })
	if len(terms) < 2 && !expanded {
		return nil
	}

	var out []ftsRewrite
	if n := len(content); n > 0 && n < len(terms) {
		out = append(out, ftsRewrite{match: joinQuoted(content, " ")})
	}
	if expanded {
		and := make([]string, 0, len(groups))
		for _, g := range groups {
			if len(g.alts) == 0 {
				and = append(and, quoteFTS(g.text))
				continue
			}
			and = append(and, "("+joinQuoted(append([]string{g.text}, g.alts...), " OR ")+")")
		}
		// FTS5 only ANDs bare phrases implicitly, not parenthesized groups.
		out = append(out, ftsRewrite{match: strings.Join(and, " AND "), partial: true})
	}
	// One content word: the OR stage would repeat the synonym AND stage.
	if len(content) >= 2 {
		var or []string
		for _, g := range groups {
			for _, w := range strings.Fields(g.text) {
				if !isStopword(w) {
					or = append(or, w)
				}
			}
			or = append(or, g.alts...)
		}
		out = append(out, ftsRewrite{match: joinQuoted(or, " OR "), partial: true})
	}
	if len(content) == 0 {
		out = append(out, ftsRewrite{match: joinQuoted(terms, " OR "), partial: true})
	}
	return out
}

func isStopword(w string) bool {
	_, ok := ftsStopwords[strings.ToLower(w)]
	return ok
}

func quoteFTS(s string) string { return `"` + s + `"` }

func joinQuoted(words []string, sep string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quoteFTS(w)
	}
	return strings.Join(quoted, sep)
}

// searchChunksRaw runs one MATCH, skipping chunk ids in exclude, and returns
// the rows with their chunk ids.
func (i *Index) searchChunksRaw(ctx context.Context, match, siteKey string, limit int, exclude []int64) ([]SearchResult, []int64, error) {
	// Weights: title 4, heading path 2, body 1, split identifiers 1 - a match
	// in the page title or section heading is a stronger signal than one in
	// running text.
	q := `SELECT c.id, c.site_key, c.url, c.title, c.heading_path, c.anchor,
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
	if len(exclude) > 0 {
		ids, err := json.Marshal(exclude)
		if err != nil {
			return nil, nil, err
		}
		// One JSON parameter instead of one per id: a large -limit must not
		// hit SQLite's bound-parameter cap.
		q += ` AND c.id NOT IN (SELECT value FROM json_each(?))`
		args = append(args, string(ids))
	}
	q += ` ORDER BY rank, c.url, c.anchor, c.id LIMIT ?`
	args = append(args, limit)

	rows, err := i.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []SearchResult{}
	var ids []int64
	for rows.Next() {
		var r SearchResult
		var id int64
		if err := rows.Scan(&id, &r.SiteKey, &r.URL, &r.Title, &r.HeadingPath, &r.Anchor, &r.Snippet, &r.Rank); err != nil {
			return nil, nil, fmt.Errorf("scan result: %w", err)
		}
		out = append(out, r)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return out, ids, nil
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
