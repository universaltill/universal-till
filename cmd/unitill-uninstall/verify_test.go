package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// A real snapshot of a real (migrated) database passes verification.
func TestVerifyBackupGood(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unitill-pos.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	snap, err := db.Snapshot(database.DB, dbPath)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := verifyBackup(snap); err != nil {
		t.Errorf("good backup must verify: %v", err)
	}
}

// A truncated copy — the exact half-written-file failure this check exists
// for — must be caught before anything is removed.
func TestVerifyBackupTruncated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unitill-pos.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	snap, err := db.Snapshot(database.DB, dbPath)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	trunc := filepath.Join(dir, "truncated.db")
	if err := os.WriteFile(trunc, raw[:len(raw)/3], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(trunc); err == nil {
		t.Error("truncated backup must fail verification")
	}
}

func TestVerifyBackupGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(p, []byte("this is not a sqlite database at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(p); err == nil {
		t.Error("non-SQLite file must fail verification")
	}
}

func TestVerifyBackupEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(p); err == nil {
		t.Error("zero-byte backup must fail verification")
	}
}

func TestVerifyBackupMissingFile(t *testing.T) {
	if err := verifyBackup(filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Error("missing backup must fail verification")
	}
}

// ut-docs#2033: a backup path containing '#', '?' or '%' must still
// verify correctly, not fail on a silently truncated/wrong DSN. The
// backup file here is built directly through the same escaped DSN
// verifyBackup itself uses — not via db.Open/db.Snapshot, which at the
// time this test was written still carried the unescaped-DSN bug
// ut-docs#2030 fixes in a separate, unmerged PR, and would silently
// create the db at the WRONG (truncated) path, making this test pass for
// the wrong reason.
func TestVerifyBackupGoodAtSpecialCharPath(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "hash#test", "q?mark", "percent%dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "backup.db")

	sqlDB, err := sql.Open("sqlite", "file:"+escapeSQLiteURIPath(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sqlDB.Exec(`CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := verifyBackup(path); err != nil {
		t.Errorf("a good backup at a special-char path must verify: %v", err)
	}
}
