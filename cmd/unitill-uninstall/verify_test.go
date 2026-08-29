package main

import (
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
