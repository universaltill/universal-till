package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// preResetLedgerDB builds a SYNTHETIC pre-reset database by hand — a
// schema_migrations table in its old two-column shape holding a few fake
// version rows, and no schema_lineage table — without going through Open
// and without resurrecting any of the deleted 002..078 files. This is the
// exact shape every device that ran the old ledger is left in (ADR-0074
// Decision 3, ut-docs#1425).
func preResetLedgerDB(t *testing.T, withEmptyLineageTable bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old-ledger.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations (version) VALUES (1), (40), (78)`); err != nil {
		t.Fatal(err)
	}
	if withEmptyLineageTable {
		if _, err := raw.Exec(`CREATE TABLE schema_lineage (id INTEGER PRIMARY KEY CHECK (id = 1), reset_marker TEXT NOT NULL, reset_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// tableNames returns the user tables in the database at path, read through
// a raw connection so the assertion is independent of Open.
func tableNames(t *testing.T, path string) []string {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

// TestOpenRefusesDatabaseThatPredatesReset: a ledger with versions but no
// lineage marker must fail with the exact operator-facing message, and
// must NOT have run the baseline against the old database (which the
// pre-ADR-0074 runner would have silently skipped anyway, since the sole
// remaining migration is version 1 <= 78).
func TestOpenRefusesDatabaseThatPredatesReset(t *testing.T) {
	path := preResetLedgerDB(t, false)

	_, err := Open(path)
	if err == nil {
		t.Fatal("Open on a pre-reset ledger must fail, got nil")
	}
	if !errors.Is(err, ErrDatabasePredatesReset) {
		t.Fatalf("error must wrap ErrDatabasePredatesReset, got: %v", err)
	}
	const want = "database predates the schema reset — delete the data directory and start again"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to carry %q verbatim", err.Error(), want)
	}

	if got := tableNames(t, path); strings.Join(got, ",") != "schema_migrations" {
		t.Fatalf("tables after refused Open = %v, want only schema_migrations (no migration may run on a pre-reset database)", got)
	}
}

// TestOpenRefusesLedgerWithEmptyLineageTable: the table alone is not a
// marker — the row is.
func TestOpenRefusesLedgerWithEmptyLineageTable(t *testing.T) {
	path := preResetLedgerDB(t, true)
	_, err := Open(path)
	if !errors.Is(err, ErrDatabasePredatesReset) {
		t.Fatalf("Open on a ledger with an empty schema_lineage table must fail with ErrDatabasePredatesReset, got: %v", err)
	}
}

// TestOpenPostResetDatabaseReopensClean pins the happy path on both sides
// of the guard: a fresh install (watermark 0, no lineage yet) applies the
// baseline, which writes the marker; reopening that database (watermark
// 1, marker present) passes the guard and applies nothing.
func TestOpenPostResetDatabaseReopensClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "post-reset.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var marker string
	if err := d.QueryRow(`SELECT reset_marker FROM schema_lineage WHERE id = 1`).Scan(&marker); err != nil {
		t.Fatalf("schema_lineage marker missing after fresh install: %v", err)
	}
	if marker != "2026-09-migration-baseline-reset" {
		t.Fatalf("reset_marker = %q", marker)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen of a post-reset database must succeed: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_migrations rows after reopen = %d err=%v, want exactly 1", n, err)
	}
}
