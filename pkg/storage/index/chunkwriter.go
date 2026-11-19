package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
)

var (
	errChunkWriterClosed  = errors.New("chunk writer is closed")
	errChunkWriterAborted = errors.New("chunk writer was aborted")
)

// ChunkWriter replaces many pages' chunks for one site with few statements, since
// FTS5 writes a segment per statement. It holds the index's write lock for about
// 1 s at a time, so don't write to the index from its goroutine before Close.
type ChunkWriter struct {
	idx     *Index
	ctx     context.Context
	siteKey string

	maxGroupPages int
	maxGroupBytes int
	maxStmtRows   int
	maxStmtBytes  int
	maxTxAge      time.Duration

	group      []pendingPage
	groupBytes int
	rows       []chunkRow
	rowBytes   int
	seen       map[string]struct{}

	tx      *sql.Tx
	txStart time.Time
	err     error
	closed  bool
}

type pendingPage struct {
	url  string
	rows []chunkRow
}

type chunkRow struct {
	url, title, headingPath, anchor string
	seq                             int
	contentHash, text, identifiers  string
}

// NewChunkWriter returns a writer for siteKey; call Close to commit, or Abort.
func (i *Index) NewChunkWriter(ctx context.Context, siteKey string) *ChunkWriter {
	return &ChunkWriter{
		idx: i, ctx: ctx, siteKey: siteKey,
		maxGroupPages: 64, maxGroupBytes: 8 << 20,
		maxStmtRows: 3000, maxStmtBytes: 8 << 20,
		maxTxAge: time.Second,
		seen:     map[string]struct{}{},
	}
}

// Put replaces the page's chunks. Pages already committed stay committed if a
// later call fails.
func (w *ChunkWriter) Put(url, title, contentHash string, chunks []chunk.Chunk) error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return errChunkWriterClosed
	}
	if w.siteKey == "" || url == "" {
		return fmt.Errorf("site_key and url are required")
	}
	if err := w.ctx.Err(); err != nil {
		return w.fail(err)
	}
	if _, again := w.seen[url]; again {
		// The repeat's existence check must see the earlier copy's rows.
		if err := w.flushGroup(); err != nil {
			return w.fail(err)
		}
		if err := w.writeRows(); err != nil {
			return w.fail(err)
		}
	}
	w.seen[url] = struct{}{}

	p := pendingPage{url: url, rows: make([]chunkRow, 0, len(chunks))}
	for _, c := range chunks {
		p.rows = append(p.rows, chunkRow{
			url: url, title: title, headingPath: c.HeadingPath, anchor: c.Anchor, seq: c.Seq,
			contentHash: contentHash, text: c.Text, identifiers: chunk.IdentifierTokens(c.Text),
		})
		w.groupBytes += len(c.Text)
	}
	w.group = append(w.group, p)
	if len(w.group) >= w.maxGroupPages || w.groupBytes >= w.maxGroupBytes {
		return w.fail(w.flushGroup())
	}
	return nil
}

// Close writes everything pending and commits.
func (w *ChunkWriter) Close() error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.flushGroup(); err != nil {
		return w.fail(err)
	}
	if err := w.writeRows(); err != nil {
		return w.fail(err)
	}
	return w.fail(w.commit())
}

// Abort rolls back the open transaction and drops pending pages. Safe to call
// after Close or a failed call, and more than once.
func (w *ChunkWriter) Abort() {
	if !w.closed && w.err == nil {
		w.err = errChunkWriterAborted
	}
	w.rollback()
	w.group, w.groupBytes, w.rows, w.rowBytes = nil, 0, nil, 0
	w.closed = true
}

func (w *ChunkWriter) fail(err error) error {
	if err == nil {
		return nil
	}
	w.err = err
	w.Abort()
	return err
}

func (w *ChunkWriter) begin() error {
	if w.tx != nil {
		return nil
	}
	w.idx.writeMu.Lock()
	tx, err := w.idx.db.BeginTx(w.ctx, nil)
	if err != nil {
		w.idx.writeMu.Unlock()
		return fmt.Errorf("begin: %w", err)
	}
	w.tx, w.txStart = tx, time.Now()
	return nil
}

func (w *ChunkWriter) commit() error {
	if w.tx == nil {
		return nil
	}
	err := w.tx.Commit()
	w.tx = nil
	w.idx.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (w *ChunkWriter) rollback() {
	if w.tx == nil {
		return
	}
	_ = w.tx.Rollback()
	w.tx = nil
	w.idx.writeMu.Unlock()
}

// flushGroup deletes the old chunks of the group's pages that have any, then
// moves the group's rows to the row buffer.
func (w *ChunkWriter) flushGroup() error {
	if len(w.group) == 0 {
		return nil
	}
	if err := w.begin(); err != nil {
		return err
	}
	urls := make([]string, len(w.group))
	for i, p := range w.group {
		urls[i] = p.url
	}
	existing, err := w.indexedURLs(urls)
	if err != nil {
		return err
	}
	// A DELETE is a statement too, and flushes FTS5 even when it deletes nothing.
	if len(existing) > 0 {
		list, err := json.Marshal(existing)
		if err != nil {
			return err
		}
		if _, err := w.tx.ExecContext(w.ctx,
			`DELETE FROM chunks WHERE site_key = ? AND url IN (SELECT value FROM json_each(?))`,
			w.siteKey, string(list)); err != nil {
			return fmt.Errorf("delete old chunks: %w", err)
		}
	}
	for _, p := range w.group {
		for _, r := range p.rows {
			w.rows = append(w.rows, r)
			w.rowBytes += len(r.text)
			if len(w.rows) >= w.maxStmtRows || w.rowBytes >= w.maxStmtBytes {
				if err := w.writeRows(); err != nil {
					return err
				}
			}
		}
	}
	w.group, w.groupBytes = w.group[:0], 0
	if time.Since(w.txStart) >= w.maxTxAge {
		if err := w.writeRows(); err != nil {
			return err
		}
		return w.commit()
	}
	return nil
}

func (w *ChunkWriter) indexedURLs(urls []string) ([]string, error) {
	list, err := json.Marshal(urls)
	if err != nil {
		return nil, err
	}
	rows, err := w.tx.QueryContext(w.ctx,
		`SELECT DISTINCT url FROM chunks WHERE site_key = ? AND url IN (SELECT value FROM json_each(?))`,
		w.siteKey, string(list))
	if err != nil {
		return nil, fmt.Errorf("check indexed pages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

const chunkRowPlaceholders = "(?, ?, ?, ?, ?, ?, ?, ?, ?)"

func (w *ChunkWriter) writeRows() error {
	if len(w.rows) == 0 {
		return nil
	}
	if err := w.begin(); err != nil {
		return err
	}
	var q strings.Builder
	q.WriteString(`INSERT INTO chunks (site_key, url, title, heading_path, anchor, seq, content_hash, text, identifiers) VALUES `)
	args := make([]any, 0, 9*len(w.rows))
	for i, r := range w.rows {
		if i > 0 {
			q.WriteString(", ")
		}
		q.WriteString(chunkRowPlaceholders)
		args = append(args, w.siteKey, r.url, r.title, r.headingPath, r.anchor, r.seq, r.contentHash, r.text, r.identifiers)
	}
	if _, err := w.tx.ExecContext(w.ctx, q.String(), args...); err != nil {
		return fmt.Errorf("insert chunks: %w", err)
	}
	w.rows, w.rowBytes = w.rows[:0], 0
	return nil
}
