package catimport

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // pure-Go driver: same one bkp.go uses
)

// ut-docs#2033: a tmpPath (os.CreateTemp-derived) containing '#', '?' or
// '%' must still resolve to the real intended file once escaped — not a
// silently truncated one — and must still take the _pragma query string
// that follows it in the DSN. Verified by identity (PRAGMA database_list +
// os.SameFile), not just "no error", per this card's own acceptance
// criteria.
func TestEscapeSQLiteURIPath_ResolvesToIntendedFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "hash#test", "q?mark", "percent%dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "backup.db")

	dsn := "file:" + escapeSQLiteURIPath(path) + "?_pragma=temp_store(2)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqlDB.Close()
	if _, err := sqlDB.Exec(`CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatalf("create table against the real intended path must succeed: %v", err)
	}

	var openedFile string
	if err := sqlDB.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&openedFile); err != nil {
		t.Fatalf("read database_list: %v", err)
	}
	wantInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat intended path %q: %v", path, err)
	}
	gotInfo, err := os.Stat(openedFile)
	if err != nil {
		t.Fatalf("stat file %q reported by SQLite as opened: %v", openedFile, err)
	}
	if !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("opened %q, want the file at the full intended path %q — the DSN path was truncated at a special character", openedFile, path)
	}

	var tempStore int
	if err := sqlDB.QueryRow(`PRAGMA temp_store`).Scan(&tempStore); err != nil {
		t.Fatalf("read temp_store: %v", err)
	}
	if tempStore != 2 {
		t.Fatalf("temp_store = %d, want 2 — the _pragma query string was silently dropped", tempStore)
	}
}

// A path with no special characters must round-trip unchanged.
func TestEscapeSQLiteURIPath_NoOpOnNormalPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "normal", "backup.db")
	if got := escapeSQLiteURIPath(path); got != path {
		t.Fatalf("escapeSQLiteURIPath must be a no-op on a path with no special chars: got %q, want %q", got, path)
	}
}
