package index

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// legacySchema is the DDL as it shipped before the migration runner existed:
// the tables plus an empty schema_version. Databases in the wild look like this.
const legacySchema = `
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS crawls (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    site_key         TEXT    NOT NULL,
    crawl_started_at TEXT    NOT NULL,
    crawl_ended_at   TEXT    NOT NULL,
    total_pages      INTEGER NOT NULL,
    mode             TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS crawls_site_ended ON crawls(site_key, crawl_ended_at DESC);
CREATE TABLE IF NOT EXISTS page_history (
    crawl_id     INTEGER NOT NULL,
    url          TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    content_hash TEXT    NOT NULL,
    depth        INTEGER NOT NULL,
    PRIMARY KEY (crawl_id, url),
    FOREIGN KEY (crawl_id) REFERENCES crawls(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS page_history_url ON page_history(url);
`

// writeRawDB applies ddl to a fresh database file without going through Open,
// so a pre-migration database can be reconstructed exactly.
func writeRawDB(t *testing.T, path, ddl string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("apply raw ddl: %v", err)
	}
}

func TestMigrationsAreWellFormed(t *testing.T) {
	if len(migrations) == 0 {
		t.Fatal("no migrations declared")
	}
	seen := map[string]bool{}
	for i, m := range migrations {
		if m.version != i+1 {
			t.Errorf("migration %d has version %d; versions must be contiguous from 1", i, m.version)
		}
		if m.name == "" {
			t.Errorf("migration %d has no name", m.version)
		}
		if _, err := migrationFS.ReadFile(m.file); err != nil {
			t.Errorf("migration %d declares %q which is not embedded: %v", m.version, m.file, err)
		}
		seen[m.file] = true
	}

	// Every embedded file must be declared, or a migration silently never runs.
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	for _, e := range entries {
		p := "migrations/" + e.Name()
		if !seen[p] {
			t.Errorf("%s is embedded but not declared in the migrations slice", p)
		}
	}
}

func TestMigrateFreshDatabaseStampsLatest(t *testing.T) {
	idx := newTestIndex(t, 5)

	got, err := idx.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != latestVersion() {
		t.Errorf("fresh database at version %d, want %d", got, latestVersion())
	}

	for _, table := range []string{"crawls", "page_history", "schema_version"} {
		var n int
		q := `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`
		if err := idx.db.QueryRow(q, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %q missing after migration (n=%d, err=%v)", table, n, err)
		}
	}
}

// The case that matters: a database written before migrations existed must be
// adopted and stamped, never rebuilt, and must keep every row.
func TestMigrateAdoptsLegacyDatabaseWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	writeRawDB(t, path, legacySchema)

	// Seed history the way a pre-migration build would have.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	res, err := db.Exec(`INSERT INTO crawls(site_key,crawl_started_at,crawl_ended_at,total_pages,mode)
	                     VALUES('docs','2026-05-01T10:00:00Z','2026-05-01T10:05:00Z',2,'full')`)
	if err != nil {
		t.Fatalf("seed crawl: %v", err)
	}
	id, _ := res.LastInsertId()
	for _, p := range []struct{ url, hash string }{{"https://d/a", "h1"}, {"https://d/b", "h2"}} {
		if _, err := db.Exec(`INSERT INTO page_history(crawl_id,url,title,content_hash,depth) VALUES(?,?,?,?,0)`,
			id, p.url, "T", p.hash); err != nil {
			t.Fatalf("seed page: %v", err)
		}
	}
	var before int
	_ = db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&before)
	if before != 0 {
		t.Fatalf("legacy fixture should have no version row, got %d", before)
	}
	db.Close()

	idx, err := Open(path, 5, discardLog())
	if err != nil {
		t.Fatalf("Open on legacy database: %v", err)
	}
	defer idx.Close()

	v, err := idx.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != latestVersion() {
		t.Errorf("legacy database stamped %d, want %d", v, latestVersion())
	}

	latest, err := idx.GetLatestCrawl(context.Background(), "docs")
	if err != nil {
		t.Fatalf("GetLatestCrawl: %v", err)
	}
	if latest == nil {
		t.Fatal("pre-existing crawl history was lost by the migration")
	}
	if latest.TotalPages != 2 || latest.Mode != ModeFull {
		t.Errorf("history altered: total_pages=%d mode=%q, want 2/full", latest.TotalPages, latest.Mode)
	}

	var pages int
	if err := idx.db.QueryRow(`SELECT count(*) FROM page_history`).Scan(&pages); err != nil {
		t.Fatalf("count page_history: %v", err)
	}
	if pages != 2 {
		t.Errorf("page_history has %d rows, want 2", pages)
	}
}

// Reopening must not re-apply a migration or add a duplicate version row.
func TestMigrateIsIdempotentAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	for i := range 3 {
		idx, err := Open(path, 5, discardLog())
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		var rows int
		if err := idx.db.QueryRow(`SELECT count(*) FROM schema_version`).Scan(&rows); err != nil {
			t.Fatalf("count schema_version: %v", err)
		}
		if rows != len(migrations) {
			t.Errorf("after Open #%d schema_version has %d rows, want %d", i+1, rows, len(migrations))
		}
		idx.Close()
	}
}

// An index written by a newer build must be refused, not written to with a
// schema this binary does not understand.
func TestMigrateRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(path, 5, discardLog())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	future := latestVersion() + 7
	if _, err := idx.db.Exec(`INSERT INTO schema_version(version) VALUES(?)`, future); err != nil {
		t.Fatalf("stamp future version: %v", err)
	}
	idx.Close()

	_, err = Open(path, 5, discardLog())
	if err == nil {
		t.Fatal("expected Open to refuse a database from a newer build")
	}
	for _, want := range []string{fmt.Sprint(future), "upgrade doc-scraper"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// A migration that fails partway must leave nothing behind: not the objects its
// earlier statements created, and not a version stamp.
func TestMigrateFailedStepRollsBackEntirely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer db.Close()

	steps := append(append([]migration{}, migrations...), migration{
		version: latestVersion() + 1,
		name:    "deliberately broken",
		file:    "test://broken",
		// The first statement succeeds; the second is not valid SQL, so the
		// step fails after having already created a table.
		sql: `CREATE TABLE half_applied (a INTEGER); THIS IS NOT VALID SQL;`,
	})

	err = migrateSteps(context.Background(), db, discardLog(), steps)
	if err == nil {
		t.Fatal("expected the broken migration to fail")
	}
	if !strings.Contains(err.Error(), "deliberately broken") {
		t.Errorf("error should name the failing migration, got: %v", err)
	}

	var halfApplied int
	q := `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='half_applied'`
	if err := db.QueryRow(q).Scan(&halfApplied); err != nil {
		t.Fatalf("probe half_applied: %v", err)
	}
	if halfApplied != 0 {
		t.Error("a table created by the failed migration survived; the step was not atomic")
	}

	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 0 {
		t.Errorf("version stamped %d despite the migration failing, want 0", version)
	}

	// The whole batch rolled back, so the good steps must still be pending.
	var crawls int
	q = `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='crawls'`
	if err := db.QueryRow(q).Scan(&crawls); err != nil {
		t.Fatalf("probe crawls: %v", err)
	}
	if crawls != 0 {
		t.Error("migration 1 persisted even though a later step in the same batch failed")
	}
}

// After a failed migration the write lock must be released, or every later
// opener would block until busy_timeout.
func TestMigrateReleasesLockAfterFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	broken := append(append([]migration{}, migrations...), migration{
		version: latestVersion() + 1, name: "broken", file: "test://broken",
		sql: `THIS IS NOT VALID SQL;`,
	})
	if err := migrateSteps(context.Background(), db, discardLog(), broken); err == nil {
		t.Fatal("expected failure")
	}
	db.Close()

	done := make(chan error, 1)
	go func() {
		idx, err := Open(path, 5, discardLog())
		if idx != nil {
			idx.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Open after failed migration: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Open blocked after a failed migration; the write lock was not released")
	}
}

// Two openers racing a fresh database must not both apply the same migration.
func TestMigrateConcurrentOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	const n = 6

	var wg sync.WaitGroup
	errs := make([]error, n)
	idxs := make([]*Index, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			idxs[i], errs[i] = Open(path, 5, discardLog())
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Open #%d failed: %v", i, err)
			continue
		}
		idxs[i].Close()
	}

	idx, err := Open(path, 5, discardLog())
	if err != nil {
		t.Fatalf("final Open: %v", err)
	}
	defer idx.Close()
	var rows int
	if err := idx.db.QueryRow(`SELECT count(*) FROM schema_version`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != len(migrations) {
		t.Errorf("schema_version has %d rows after %d concurrent opens, want %d", rows, n, len(migrations))
	}
}

// SchemaVersion must tolerate a nil index, matching Close.
func TestSchemaVersionNilReceiver(t *testing.T) {
	var idx *Index
	v, err := idx.SchemaVersion(context.Background())
	if err != nil || v != 0 {
		t.Errorf("nil receiver: got (%d, %v), want (0, nil)", v, err)
	}
}
