package db

import (
	"os"
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

// ut-docs#2030: OpenReadOnly() builds its DSN the same unescaped way Open()
// did — a path containing '#' or '?' silently truncates both the path and
// the _pragma=busy_timeout(5000) query string.
//
// Review finding on this test's first draft: a marker-row-visible assertion
// alone does NOT catch this bug, because under the pre-fix code Open() and
// OpenReadOnly() truncate to the very SAME wrong file — the row is visible
// there too, self-consistently. Nor does asserting busy_timeout, because
// that pragma is parsed by the Go driver itself at the first '?' anywhere
// in the whole DSN (independent of SQLite's own URI path truncation) and
// still applies even when the path was truncated. The load-bearing
// assertion is PRAGMA database_list's file column — what SQLite itself
// believes it opened — compared by file identity (os.SameFile) against the
// real intended path, since SQLite may report a differently formatted but
// equivalent path string.
func TestOpenReadOnly_HandlesSpecialCharsInPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hash#test", "q?mark")
	dbPath := filepath.Join(dir, "unitill-pos.db")

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
		t.Fatalf("OpenReadOnly on a path containing #/? must succeed, got: %v", err)
	}
	defer ro.Close()

	var openedFile string
	if err := ro.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&openedFile); err != nil {
		t.Fatalf("read database_list: %v", err)
	}
	wantInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat intended path: %v", err)
	}
	gotInfo, err := os.Stat(openedFile)
	if err != nil {
		t.Fatalf("stat file %q reported by SQLite as opened: %v", openedFile, err)
	}
	if !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("OpenReadOnly opened %q, want the file at the full intended path %q — the DSN path was truncated at a special character", openedFile, dbPath)
	}

	var n int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 999999`).Scan(&n); err != nil {
		t.Fatalf("query existing data via read-only connection: %v", err)
	}
	if n != 1 {
		t.Fatalf("marker row visible via read-only connection at a special-char path = %d, want 1", n)
	}

	var busyTimeout int
	if err := ro.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000 — the _pragma query string was silently dropped", busyTimeout)
	}
}
