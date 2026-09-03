// Package index is the SQLite-backed crawl history store. It records one row
// per crawl (with mode + timestamps) and one row per page-in-crawl (with the
// markdown SHA-256), bounded by a per-site retention count. get_freshness and
// diff_crawl read from here. The same database file is the v3.0 storage spine
// that FTS5 and sqlite-vec virtual tables will sit alongside later.
package index

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Mode is the crawl mode label persisted with each crawl record.
type Mode string

const (
	ModeFull        Mode = "full"
	ModeIncremental Mode = "incremental"
	ModeResume      Mode = "resume"
)

// DefaultRetention is the per-site crawl-history depth when no config value is provided.
const DefaultRetention = 10

// Index owns the SQLite handle. Writes are serialized with writeMu so the
// many-tiny-tx pattern (one tx per crawl finish) stays simple under parallel
// --all-sites crawls; SQLite's own busy_timeout backs that up.
type Index struct {
	db        *sql.DB
	log       *slog.Logger
	writeMu   sync.Mutex
	retention int
	path      string
}

// PageRecord is one page row passed to RecordCrawl.
type PageRecord struct {
	URL         string
	Title       string
	ContentHash string
	Depth       int
}

// CrawlRecord is the per-crawl bundle passed to RecordCrawl.
type CrawlRecord struct {
	SiteKey        string
	CrawlStartedAt time.Time
	CrawlEndedAt   time.Time
	Mode           Mode
	Pages          []PageRecord
}

// LatestCrawl is the row returned by GetLatestCrawl.
type LatestCrawl struct {
	ID             int64
	SiteKey        string
	CrawlStartedAt time.Time
	CrawlEndedAt   time.Time
	TotalPages     int
	Mode           Mode
}

// DiffEntry is one row in DiffResult.Entries. Kind is "added", "removed", or "changed".
// For "added" and "changed", ContentHash is the current hash; for "changed", PriorHash
// is the baseline hash. For "removed", ContentHash is the baseline hash and
// PriorHash is empty.
type DiffEntry struct {
	Kind        string
	URL         string
	Title       string
	ContentHash string
	PriorHash   string
	Depth       int
}

// DiffResult bundles the diff response. BaselineCrawl is nil if no crawl ended
// at or before the requested since; CurrentCrawl is nil if the site has never
// been crawled.
type DiffResult struct {
	BaselineCrawl  *LatestCrawl
	CurrentCrawl   *LatestCrawl
	Entries        []DiffEntry
	Total          int
	UnchangedCount int
}

// Open opens or creates the SQLite database at path, applies the schema, and
// returns an *Index. retention<=0 means use DefaultRetention.
func Open(path string, retention int, log *slog.Logger) (*Index, error) {
	if path == "" {
		return nil, fmt.Errorf("index path is required")
	}
	// modernc/sqlite opens lazily on the first PRAGMA, so a missing parent dir
	// yields an opaque "unable to open database file"; create it first.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create index dir %q: %w", dir, err)
		}
	}
	if retention <= 0 {
		retention = DefaultRetention
	}
	// Per-connection pragmas go in the DSN so every connection database/sql
	// pools gets them; a plain db.Exec would configure only whichever single
	// connection happened to run it, leaving the rest without foreign_keys
	// and silently breaking ON DELETE CASCADE.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := enableWAL(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	idx := &Index{
		db:        db,
		log:       log.With("component", "index", "path", path),
		retention: retention,
		path:      path,
	}
	if err := migrate(ctx, db, idx.log); err != nil {
		_ = db.Close()
		return nil, err
	}
	idx.log.Info("crawl-history index opened", "retention", retention, "schema_version", latestVersion())
	return idx, nil
}

// enableWAL switches the database to WAL journaling, retrying on SQLITE_BUSY.
// The journal-mode pragma is documented to bypass the busy handler entirely,
// so when two connections race to convert a fresh database the loser fails
// instantly despite busy_timeout; WAL is a persistent property of the file,
// so once anyone wins, later attempts are no-ops.
func enableWAL(ctx context.Context, db *sql.DB) error {
	const pragma = "PRAGMA journal_mode = WAL"
	for {
		_, err := db.ExecContext(ctx, pragma)
		if err == nil {
			return nil
		}
		msg := err.Error()
		if !strings.Contains(msg, "SQLITE_BUSY") && !strings.Contains(msg, "database is locked") {
			return fmt.Errorf("pragma %q: %w", pragma, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pragma %q: %w (retry budget exhausted)", pragma, err)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Close releases the database handle. Safe to call on a nil receiver.
func (i *Index) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}

// OpenAt is the canonical constructor for callers that already know stateDir:
// it places the database at <stateDir>/index.db. An empty stateDir is logged
// and returns (nil, nil) so callers can degrade gracefully (no history capture,
// no error path bloat).
func OpenAt(stateDir string, retention int, log *slog.Logger) (*Index, error) {
	if stateDir == "" {
		log.Warn("state_dir is empty; crawl-history index disabled")
		return nil, nil
	}
	return Open(filepath.Join(stateDir, "index.db"), retention, log)
}
