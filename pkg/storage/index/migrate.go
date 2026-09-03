package index

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one ordered, forward-only schema step. Adding one means adding a
// file under migrations/ and a line here; versions must stay contiguous from 1,
// which TestMigrationsAreWellFormed enforces.
type migration struct {
	version int
	name    string
	file    string
	sql     string
}

// embedded loads a migration at init. A missing file is a build error, not a
// runtime condition, so it panics rather than deferring the failure to Open.
func embedded(version int, name, file string) migration {
	b, err := migrationFS.ReadFile(file)
	if err != nil {
		panic(fmt.Sprintf("migration %d (%s): %v", version, file, err))
	}
	return migration{version: version, name: name, file: file, sql: string(b)}
}

var migrations = []migration{
	embedded(1, "initial crawl history", "migrations/0001_initial.sql"),
	embedded(2, "chunks and full-text search", "migrations/0002_chunks.sql"),
}

// schemaVersionDDL is applied outside the migration list because the runner has
// to read its own bookkeeping before it can decide what to apply. Its shape is
// deliberately identical to the table shipped before migrations existed, so
// databases created by either path are indistinguishable.
const schemaVersionDDL = `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)`

// latestVersion is the schema version this build understands.
func latestVersion() int {
	return latestOf(migrations)
}

func latestOf(steps []migration) int {
	if len(steps) == 0 {
		return 0
	}
	return steps[len(steps)-1].version
}

// migrate brings db up to latestVersion, applying only the steps it has not
// already recorded. Databases predating the migration runner carry the tables
// but no version row; every migration is written to be idempotent so such a
// database is stamped rather than rebuilt.
func migrate(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	return migrateSteps(ctx, db, log, migrations)
}

func migrateSteps(ctx context.Context, db *sql.DB, log *slog.Logger, steps []migration) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, schemaVersionDDL); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	// BEGIN IMMEDIATE takes the write lock now instead of on first write, so two
	// processes opening the same fresh database cannot both decide to apply the
	// same migration and then collide on the version stamp.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Fresh context: ctx may already be cancelled, and leaving the write
			// lock held would block every later opener.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var current int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if latest := latestOf(steps); current > latest {
		return fmt.Errorf(
			"index database is at schema version %d but this build understands only %d; upgrade doc-scraper or remove the index to start fresh",
			current, latest)
	}

	applied := 0
	for _, m := range steps {
		if m.version <= current {
			continue
		}
		if _, err := conn.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES(?)`, m.version); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", m.version, m.name, err)
		}
		log.Info("applied schema migration", "version", m.version, "name", m.name)
		applied++
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	committed = true

	if applied == 0 {
		log.Debug("schema up to date", "version", current)
	}
	return nil
}

// SchemaVersion reports the schema version recorded in the database. Safe to
// call on a nil receiver, which reports 0.
func (i *Index) SchemaVersion(ctx context.Context) (int, error) {
	if i == nil || i.db == nil {
		return 0, nil
	}
	var v int
	err := i.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	return v, err
}
