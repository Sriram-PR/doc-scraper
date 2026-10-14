package index

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err, "open raw db")
	defer db.Close()
	_, err = db.Exec(ddl)
	require.NoError(t, err, "apply raw ddl")
}

func TestMigrationsAreWellFormed(t *testing.T) {
	require.NotEmpty(t, migrations, "no migrations declared")
	seen := map[string]bool{}
	for i, m := range migrations {
		assert.Equal(t, i+1, m.version, "migration %d: versions must be contiguous from 1", i)
		assert.NotEmpty(t, m.name, "migration %d has no name", m.version)
		_, err := migrationFS.ReadFile(m.file)
		require.NoError(t, err, "migration %d declares %q which is not embedded", m.version, m.file)
		seen[m.file] = true
	}

	// Every embedded file must be declared, or a migration silently never runs.
	entries, err := migrationFS.ReadDir("migrations")
	require.NoError(t, err, "read migrations dir")
	for _, e := range entries {
		p := "migrations/" + e.Name()
		assert.True(t, seen[p], "%s is embedded but not declared in the migrations slice", p)
	}
}

func TestMigrateFreshDatabaseStampsLatest(t *testing.T) {
	idx := newTestIndex(t, 5)

	got, err := idx.SchemaVersion(context.Background())
	require.NoError(t, err, "SchemaVersion")
	assert.Equal(t, latestVersion(), got, "fresh database version")

	for _, table := range []string{"crawls", "page_history", "schema_version"} {
		var n int
		q := `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`
		require.NoError(t, idx.db.QueryRow(q, table).Scan(&n), "table %q", table)
		assert.Equal(t, 1, n, "table %q missing after migration", table)
	}
}

// The case that matters: a database written before migrations existed must be
// adopted and stamped, never rebuilt, and must keep every row.
func TestMigrateAdoptsLegacyDatabaseWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	writeRawDB(t, path, legacySchema)

	// Seed history the way a pre-migration build would have.
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err, "open raw")
	seedClosed := false
	defer func() {
		if !seedClosed {
			_ = db.Close()
		}
	}()
	res, err := db.Exec(`INSERT INTO crawls(site_key,crawl_started_at,crawl_ended_at,total_pages,mode)
	                     VALUES('docs','2026-05-01T10:00:00Z','2026-05-01T10:05:00Z',2,'full')`)
	require.NoError(t, err, "seed crawl")
	id, _ := res.LastInsertId()
	for _, p := range []struct{ url, hash string }{{"https://d/a", "h1"}, {"https://d/b", "h2"}} {
		_, err := db.Exec(`INSERT INTO page_history(crawl_id,url,title,content_hash,depth) VALUES(?,?,?,?,0)`,
			id, p.url, "T", p.hash)
		require.NoError(t, err, "seed page")
	}
	var before int
	_ = db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&before)
	require.Equal(t, 0, before, "legacy fixture should have no version row")
	// Must be closed before Open: Windows refuses to work with the file while
	// another handle is live, and the migration takes a write lock.
	require.NoError(t, db.Close(), "close seed db")
	seedClosed = true

	idx, err := Open(path, 5, discardLog())
	require.NoError(t, err, "Open on legacy database")
	defer idx.Close()

	v, err := idx.SchemaVersion(context.Background())
	require.NoError(t, err, "SchemaVersion")
	assert.Equal(t, latestVersion(), v, "legacy database stamp")

	latest, err := idx.GetLatestCrawl(context.Background(), "docs")
	require.NoError(t, err, "GetLatestCrawl")
	require.NotNil(t, latest, "pre-existing crawl history was lost by the migration")
	assert.Equal(t, 2, latest.TotalPages, "history altered")
	assert.Equal(t, ModeFull, latest.Mode, "history altered")

	var pages int
	require.NoError(t, idx.db.QueryRow(`SELECT count(*) FROM page_history`).Scan(&pages), "count page_history")
	assert.Equal(t, 2, pages, "page_history rows")
}

// Reopening must not re-apply a migration or add a duplicate version row.
func TestMigrateIsIdempotentAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	for i := range 3 {
		idx, err := Open(path, 5, discardLog())
		require.NoError(t, err, "Open #%d", i+1)
		t.Cleanup(func() { _ = idx.Close() })
		var rows int
		require.NoError(t, idx.db.QueryRow(`SELECT count(*) FROM schema_version`).Scan(&rows), "count schema_version")
		assert.Equal(t, len(migrations), rows, "after Open #%d schema_version rows", i+1)
		idx.Close()
	}
}

// An index written by a newer build must be refused, not written to with a
// schema this binary does not understand.
func TestMigrateRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(path, 5, discardLog())
	require.NoError(t, err, "Open")
	stamped := false
	defer func() {
		if !stamped {
			_ = idx.Close()
		}
	}()
	future := latestVersion() + 7
	_, err = idx.db.Exec(`INSERT INTO schema_version(version) VALUES(?)`, future)
	require.NoError(t, err, "stamp future version")
	require.NoError(t, idx.Close(), "close")
	stamped = true

	_, err = Open(path, 5, discardLog())
	require.Error(t, err, "expected Open to refuse a database from a newer build")
	for _, want := range []string{fmt.Sprint(future), "upgrade doc-scraper"} {
		require.ErrorContains(t, err, want)
	}
}

// A migration that fails partway must leave nothing behind: not the objects its
// earlier statements created, and not a version stamp.
func TestMigrateFailedStepRollsBackEntirely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err, "open raw")
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
	require.Error(t, err, "expected the broken migration to fail")
	require.ErrorContains(t, err, "deliberately broken", "error should name the failing migration")

	var halfApplied int
	q := `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='half_applied'`
	require.NoError(t, db.QueryRow(q).Scan(&halfApplied), "probe half_applied")
	assert.Equal(t, 0, halfApplied, "a table created by the failed migration survived; the step was not atomic")

	var version int
	require.NoError(t, db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version), "read version")
	assert.Equal(t, 0, version, "version stamped despite the migration failing")

	// The whole batch rolled back, so the good steps must still be pending.
	var crawls int
	q = `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='crawls'`
	require.NoError(t, db.QueryRow(q).Scan(&crawls), "probe crawls")
	assert.Equal(t, 0, crawls, "migration 1 persisted even though a later step in the same batch failed")
}

// After a failed migration the write lock must be released, or every later
// opener would block until busy_timeout.
func TestMigrateReleasesLockAfterFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err, "open raw")
	brokenClosed := false
	defer func() {
		if !brokenClosed {
			_ = db.Close()
		}
	}()
	broken := append(append([]migration{}, migrations...), migration{
		version: latestVersion() + 1, name: "broken", file: "test://broken",
		sql: `THIS IS NOT VALID SQL;`,
	})
	require.Error(t, migrateSteps(context.Background(), db, discardLog(), broken), "expected failure")
	require.NoError(t, db.Close(), "close db")
	brokenClosed = true

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
		require.NoError(t, err, "Open after failed migration")
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
		if !assert.NoError(t, err, "concurrent Open #%d", i) {
			continue
		}
		assert.NoError(t, idxs[i].Close(), "close #%d", i)
	}

	idx, err := Open(path, 5, discardLog())
	require.NoError(t, err, "final Open")
	defer idx.Close()
	var rows int
	require.NoError(t, idx.db.QueryRow(`SELECT count(*) FROM schema_version`).Scan(&rows), "count schema_version")
	assert.Equal(t, len(migrations), rows, "schema_version rows after %d concurrent opens", n)
}

// SchemaVersion must tolerate a nil index, matching Close.
func TestSchemaVersionNilReceiver(t *testing.T) {
	var idx *Index
	v, err := idx.SchemaVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, v)
}
