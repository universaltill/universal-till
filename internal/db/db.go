package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"fmt"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	// A fresh install extracts to a folder with no data/ directory, so the
	// default ./data/unitill-pos.db path can't be opened (SQLite CANTOPEN,
	// reported as "out of memory (14)"). Create the parent directory first.
	// Skip in-memory DBs used by tests (":memory:", "file::memory:...").
	if !strings.Contains(path, ":memory:") {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create data dir %q: %w", dir, err)
			}
		}
	}
	// _pragma= is how modernc.org/sqlite applies a pragma to EVERY pooled
	// connection — an Exec'd PRAGMA reaches only the one connection that
	// ran it (the old `_foreign_keys=on` was mattn syntax and silently
	// ignored, so FK enforcement was missing on pooled connections). The
	// busy timeout keeps a concurrent writer (e.g. the sync pull's apply
	// transaction) from surfacing SQLITE_BUSY to a sale mid-checkout.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Belt-and-braces for the first connection.
	if _, err := sqlDB.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return nil, fmt.Errorf("enable foreign_keys pragma: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	db := &DB{DB: sqlDB}

	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}

func (db *DB) migrate() error {
	// ensure schema_migrations table exists
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	// find current max version
	var current int
	row := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("read current schema version: %w", err)
	}

	migs, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migs {
		if m.Version <= current {
			continue
		}
		if err := db.applyMigration(m); err != nil {
			return err
		}
	}

	return nil
}

func (db *DB) applyMigration(m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", m.Version, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.SQL); err != nil {
		return fmt.Errorf("exec migration %d (%s): %w", m.Version, m.Name, err)
	}

	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, m.Version); err != nil {
		return fmt.Errorf("record migration %d: %w", m.Version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.Version, err)
	}

	return nil
}
