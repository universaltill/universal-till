package db

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ut-docs#6294: a data folder restored onto a new install owes the same
// receipt/Z number jump as a backup restore, through the same marker file.

func TestOweNumberJump_WritesTokenNextToDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	if err := OweNumberJump(dbPath, "restored-install:A", nil); err != nil {
		t.Fatal(err)
	}
	if !NumberJumpPending(dbPath) {
		t.Fatal("marker not written")
	}
	if got := NumberJumpToken(dbPath); got != "restored-install:A" {
		t.Fatalf("token = %q", got)
	}
}

func TestOweNumberJump_KeepsExistingMarker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	p := filepath.Join(filepath.Dir(dbPath), "restore-numbers-pending")
	if err := os.WriteFile(p, []byte("staged-tok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := OweNumberJump(dbPath, "restored-install:A", nil); err != nil {
		t.Fatal(err)
	}
	if got := NumberJumpToken(dbPath); got != "staged-tok" {
		t.Fatalf("token = %q, want the existing one kept", got)
	}
}

func TestOweNumberJump_EmptyTokenRefused(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	if err := OweNumberJump(dbPath, "  ", nil); err == nil {
		t.Fatal("empty token must be refused")
	}
	if NumberJumpPending(dbPath) {
		t.Fatal("no marker for a refused token")
	}
}

func TestOweNumberJump_UnwritableDirErrors(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := OweNumberJump(filepath.Join(dir, "unitill-pos.db"), "restored-install:A", nil); err == nil {
		t.Fatal("want an error from an unwritable directory")
	}
}

func TestOweNumberJump_KeepsExistingMarkerNotYetApplied(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	p := filepath.Join(filepath.Dir(dbPath), "restore-numbers-pending")
	if err := os.WriteFile(p, []byte("staged-tok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := OweNumberJump(dbPath, "restored-install:A", func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if got := NumberJumpToken(dbPath); got != "staged-tok" {
		t.Fatalf("token = %q, want the pending one kept", got)
	}
}

func TestOweNumberJump_ReplacesSpentMarker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	dir := filepath.Dir(dbPath)
	if err := os.WriteFile(filepath.Join(dir, "restore-numbers-pending"), []byte("spent-tok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := OweNumberJump(dbPath, "restored-install:A", func(tok string) bool { return tok == "spent-tok" }); err != nil {
		t.Fatal(err)
	}
	if got := NumberJumpToken(dbPath); got != "restored-install:A" {
		t.Fatalf("token = %q, want the spent one replaced", got)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
}
