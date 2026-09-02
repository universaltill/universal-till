package db

import (
	"path/filepath"
	"testing"
)

// OpenReadOnly backs the boot-failure recovery screen's safe mode
// (ut-docs#1436, ADR-0075): when a migration fails partway through, the
// on-disk schema is exactly whatever migrations already committed (each
// applies in its own transaction — db.go's applyMigration), so a read-only
// connection that skips migrate() entirely can still serve today's sales
// read-only, no writes, while a normal Open (which would re-attempt and
// re-fail the same migration) stays unavailable.
func TestOpenReadOnly_ServesExistingDataWithoutRunningMigrations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")

	// A normal Open runs migrations and creates the schema.
	full, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := full.Exec(`INSERT INTO schema_migrations (version) VALUES (999999)`); err != nil {
		t.Fatalf("seed a marker row: %v", err)
	}
	if err := full.Close(); err != nil {
		t.Fatalf("close full: %v", err)
	}

	ro, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	var n int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 999999`).Scan(&n); err != nil {
		t.Fatalf("query existing data via read-only connection: %v", err)
	}
	if n != 1 {
		t.Fatalf("marker row visible via read-only connection = %d, want 1", n)
	}

	// The whole point: writes must be refused, not silently allowed.
	if _, err := ro.Exec(`INSERT INTO schema_migrations (version) VALUES (1000000)`); err == nil {
		t.Fatal("OpenReadOnly connection accepted a write — safe mode must be genuinely read-only")
	}
}

// A read-only open must not try to create the schema it can't write —
// proving it never calls migrate() at all, not just that migrate()
// happens to no-op on an up-to-date DB.
func TestOpenReadOnly_NeverCreatesSchemaOnAMissingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "does-not-exist.db")

	if _, err := OpenReadOnly(dbPath); err == nil {
		t.Fatal("OpenReadOnly on a nonexistent database succeeded — it must fail, not create one")
	}
}
