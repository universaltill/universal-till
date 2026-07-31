package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func memDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if _, err := sqlDB.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return sqlDB
}

func countRows(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestWithTxCommits(t *testing.T) {
	sqlDB := memDB(t)
	err := WithTx(context.Background(), sqlDB, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO t (n) VALUES (1)`)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if got := countRows(t, sqlDB); got != 1 {
		t.Fatalf("rows after commit = %d; want 1", got)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	sqlDB := memDB(t)
	boom := errors.New("boom")
	err := WithTx(context.Background(), sqlDB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO t (n) VALUES (1)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx error = %v; want the fn's own error", err)
	}
	if got := countRows(t, sqlDB); got != 0 {
		t.Fatalf("rows after rollback = %d; want 0", got)
	}
}

func TestWithTxBeginError(t *testing.T) {
	sqlDB := memDB(t)
	sqlDB.Close()
	err := WithTx(context.Background(), sqlDB, func(tx *sql.Tx) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "begin tx") {
		t.Fatalf("WithTx on closed DB = %v; want begin tx error", err)
	}
}

func TestStageRestoreFromReaderStagesPendingFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unitill-pos.db")
	if err := StageRestoreFromReader(dbPath, strings.NewReader("snapshot-bytes")); err != nil {
		t.Fatalf("StageRestoreFromReader: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, restorePendingName))
	if err != nil {
		t.Fatalf("read pending file: %v", err)
	}
	if string(got) != "snapshot-bytes" {
		t.Fatalf("pending file content = %q", got)
	}
}

// failingReader errors mid-copy, like a dropped download connection.
type failingReader struct{ served bool }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.served {
		return 0, errors.New("connection reset")
	}
	f.served = true
	n := copy(p, "partial")
	return n, nil
}

func TestStageRestoreFromReaderCleansUpOnCopyError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unitill-pos.db")
	err := StageRestoreFromReader(dbPath, &failingReader{})
	if err == nil {
		t.Fatal("StageRestoreFromReader with failing reader: want error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, restorePendingName)); !os.IsNotExist(statErr) {
		t.Fatalf("partial pending file left behind (stat err = %v)", statErr)
	}
}

func TestStageRestoreFromReaderFailsWhenDirMissing(t *testing.T) {
	// Deliberate: StageRestoreFromReader stages NEXT TO an existing DB (whose
	// dir Open() already created) — staging beside a nonexistent DB is a
	// genuine error, not a missing-MkdirAll bug.
	dbPath := filepath.Join(t.TempDir(), "no-such-dir", "unitill-pos.db")
	if err := StageRestoreFromReader(dbPath, strings.NewReader("x")); err == nil {
		t.Fatal("StageRestoreFromReader into missing dir: want error")
	}
}

func TestOpenFailsWhenDataDirUncreatable(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	// Parent "directory" is a regular file — MkdirAll must fail.
	_, err := Open(filepath.Join(blocker, "sub", "unitill-pos.db"))
	if err == nil || !strings.Contains(err.Error(), "create data dir") {
		t.Fatalf("Open under a file = %v; want create data dir error", err)
	}
}
