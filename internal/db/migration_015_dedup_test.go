package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigration015DedupsBeforeUniqueIndex is the ut-docs#1871 independent
// review finding: CREATE UNIQUE INDEX IF NOT EXISTS only guards against
// the index itself already existing, not against pre-existing data that
// violates it. Migration 015 exists specifically because the race it
// closes (SetItemThumbnail's old non-atomic UPDATE-then-INSERT) may
// already have produced duplicate item_images rows on a live till, so an
// unguarded CREATE UNIQUE INDEX against such a database would fail the
// migration outright — dropping the till into read-only safe mode
// (ADR-0075) with no self-service repair. This proves the migration's own
// SQL dedups first: seeds the exact shape the pre-fix race produced (two
// role='thumbnail' rows for one item), runs the real migration file
// verbatim (not a re-typed copy), and asserts it both succeeds and
// actually enforces uniqueness afterward.
func TestMigration015DedupsBeforeUniqueIndex(t *testing.T) {
	sqlBytes, err := os.ReadFile(filepath.Join("migrations", "015_item_images_thumbnail_unique.sql"))
	if err != nil {
		t.Fatalf("read migration 015: %v", err)
	}

	dbh, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "dedup.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer dbh.Close()

	if _, err := dbh.Exec(`CREATE TABLE item_images (
		id TEXT PRIMARY KEY, item_id TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'thumbnail',
		path TEXT NOT NULL, sort_order INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create pre-migration schema: %v", err)
	}
	seed := []struct{ id, itemID, path string }{
		{"dup-1", "item-a", "/old/placeholder.svg"},
		{"dup-2", "item-a", "/new/real-photo.png"}, // higher rowid -> must survive
		{"solo-1", "item-b", "/only/photo.png"},
	}
	for _, s := range seed {
		if _, err := dbh.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES (?, ?, ?, 'thumbnail')`, s.id, s.itemID, s.path); err != nil {
			t.Fatalf("seed %s: %v", s.id, err)
		}
	}

	for _, stmt := range strings.Split(string(sqlBytes), ";") {
		stmt = strings.TrimSpace(stripLineComments(stmt))
		if stmt == "" {
			continue
		}
		if _, err := dbh.Exec(stmt); err != nil {
			t.Fatalf("migration 015 statement %q failed against a database with a pre-existing duplicate: %v", stmt, err)
		}
	}

	var itemAPath string
	var itemACount int
	if err := dbh.QueryRow(`SELECT COUNT(*) FROM item_images WHERE item_id = 'item-a'`).Scan(&itemACount); err != nil {
		t.Fatal(err)
	}
	if itemACount != 1 {
		t.Fatalf("expected the duplicate for item-a to be deduped to 1 row, got %d", itemACount)
	}
	if err := dbh.QueryRow(`SELECT path FROM item_images WHERE item_id = 'item-a'`).Scan(&itemAPath); err != nil {
		t.Fatal(err)
	}
	if itemAPath != "/new/real-photo.png" {
		t.Fatalf("expected the higher-rowid (most recently written) row to survive, got path %q", itemAPath)
	}

	var itemBCount int
	if err := dbh.QueryRow(`SELECT COUNT(*) FROM item_images WHERE item_id = 'item-b'`).Scan(&itemBCount); err != nil {
		t.Fatal(err)
	}
	if itemBCount != 1 {
		t.Fatalf("item-b's single row must survive untouched, got %d rows", itemBCount)
	}

	if _, err := dbh.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('extra', 'item-b', '/another.png', 'thumbnail')`); err == nil {
		t.Fatal("expected the unique index to reject a second thumbnail row for item-b, insert succeeded")
	}
}

// TestMigration015NoOpOnCleanData proves the common, expected case (no
// pre-existing duplicates — every fresh or already-healthy database)
// deletes nothing.
func TestMigration015NoOpOnCleanData(t *testing.T) {
	sqlBytes, err := os.ReadFile(filepath.Join("migrations", "015_item_images_thumbnail_unique.sql"))
	if err != nil {
		t.Fatalf("read migration 015: %v", err)
	}

	dbh, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "clean.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer dbh.Close()

	if _, err := dbh.Exec(`CREATE TABLE item_images (
		id TEXT PRIMARY KEY, item_id TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'thumbnail',
		path TEXT NOT NULL, sort_order INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create pre-migration schema: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-1', 'item-a', '/photo.png', 'thumbnail')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, stmt := range strings.Split(string(sqlBytes), ";") {
		stmt = strings.TrimSpace(stripLineComments(stmt))
		if stmt == "" {
			continue
		}
		if _, err := dbh.Exec(stmt); err != nil {
			t.Fatalf("migration 015 statement %q failed: %v", stmt, err)
		}
	}

	var count int
	if err := dbh.QueryRow(`SELECT COUNT(*) FROM item_images`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected the single existing row untouched, got %d rows", count)
	}
}
