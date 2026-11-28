package plugindb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Reviewer tests for ut-docs#3157 slice B (docs/code-reviews/
// 2026-11-28-plugin-sqlite-slice-b-3157.md, findings R5, R7).

// R7: pragma_database_list would hand the guest the host's absolute path of
// its database (data directory layout, OS user name).
func TestHostFilePathIsNotReadableByThePlugin_3157(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.MigrateForInstall(ctx, "leak", "", "1.0.0", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	s, err := m.Open(ctx, "leak", Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SELECT file FROM pragma_database_list",
		"SELECT file FROM PRAGMA_Database_List()",
		`SELECT * FROM "pragma_database_list"`,
	} {
		if b, err := s.Query(ctx, q); !errors.Is(err, ErrStatementRefused) {
			t.Errorf("%s = %s, %v; want ErrStatementRefused", q, b, err)
		}
	}
}

// R5: swapping a database file never passes through "no plugin.db at all"
// (the next Open would create an empty one). A failed copy leaves the current
// file exactly as it was.
func TestReplaceDBFileKeepsTheCurrentFileWhenTheCopyFails_3157(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, DBFileName)
	if err := os.WriteFile(p, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, side := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(p+side, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := replaceDBFile(filepath.Join(dir, "missing.db"), p); err == nil {
		t.Fatal("copy of a missing source succeeded")
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "current" {
		t.Fatalf("current database after a failed swap: %q, %v", b, err)
	}
	src := filepath.Join(dir, "copy.db")
	if err := os.WriteFile(src, []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceDBFile(src, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "restored" {
		t.Fatalf("after swap: %q", b)
	}
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(p + side); err == nil {
			t.Fatalf("stale %s would be replayed onto the restored file", side)
		}
	}
}
