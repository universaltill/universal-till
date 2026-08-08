package db

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// countTills opens the SQLite file at dbPath on a fresh connection and
// returns (total rows, rows with a non-NULL bearer_hash) in tills.
func countTills(t *testing.T, dbPath string) (total, withHash int) {
	t.Helper()
	sd, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer sd.Close()
	if err := sd.QueryRow(`SELECT COUNT(*) FROM tills`).Scan(&total); err != nil {
		t.Fatalf("count tills in %s: %v", dbPath, err)
	}
	if err := sd.QueryRow(`SELECT COUNT(*) FROM tills WHERE bearer_hash IS NOT NULL`).Scan(&withHash); err != nil {
		t.Fatalf("count bearer_hash in %s: %v", dbPath, err)
	}
	return total, withHash
}

// ut-docs#426: the copy served to a joining replica must have every till's
// bearer_hash NULLed, while the REAL backup snapshot and the live DB keep
// the real secrets (they're genuine disaster-recovery artifacts).
func TestRedactedJoinSnapshot_NullsBearerHashOnCopyOnly(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	for _, row := range [][2]string{{"till-1", "hash-one"}, {"till-2", "hash-two"}} {
		if _, err := d.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES (?, ?, ?)`,
			row[0], "Till "+row[0], row[1]); err != nil {
			t.Fatalf("seed till: %v", err)
		}
	}

	// A real backup snapshot taken alongside — must stay pristine.
	realSnap, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	copyPath, cleanup, err := RedactedJoinSnapshot(d.DB, path)
	if err != nil {
		t.Fatalf("RedactedJoinSnapshot: %v", err)
	}
	if copyPath == realSnap {
		t.Fatalf("redacted copy must be a NEW file, not the real snapshot %s", realSnap)
	}

	if total, withHash := countTills(t, copyPath); total != 2 || withHash != 0 {
		t.Errorf("redacted copy: got %d rows, %d with bearer_hash; want 2 rows, 0 with bearer_hash", total, withHash)
	}
	if total, withHash := countTills(t, realSnap); total != 2 || withHash != 2 {
		t.Errorf("REAL snapshot mutated: got %d rows, %d with bearer_hash; want 2 and 2", total, withHash)
	}
	var live int
	if err := d.QueryRow(`SELECT COUNT(*) FROM tills WHERE bearer_hash IS NOT NULL`).Scan(&live); err != nil {
		t.Fatalf("count live tills: %v", err)
	}
	if live != 2 {
		t.Errorf("LIVE DB mutated: %d rows with bearer_hash, want 2", live)
	}

	// The throwaway copy must be invisible to the settings-page backups list
	// (and un-restorable by name) — it is not a disaster-recovery artifact.
	list, err := ListBackups(path)
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	for _, b := range list {
		if b.Name == filepath.Base(copyPath) {
			t.Errorf("redacted copy %s leaked into ListBackups", b.Name)
		}
	}
	if ValidBackupName(filepath.Base(copyPath)) {
		t.Errorf("redacted copy name %s passes ValidBackupName — it must not be restorable", filepath.Base(copyPath))
	}

	cleanup()
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove the copy: stat err = %v", err)
	}
}

// ut-docs#426 (review finding): SQL-level NULL is not enough. SQLite doesn't
// zero the bytes a shrunk record frees, so a plain `UPDATE ... SET
// bearer_hash = NULL` leaves the real hash strings physically present in the
// copy's page slack — recoverable with a raw byte scan even though every row
// reads back NULL over SQL (which is exactly why the SQL-level assertions in
// the other test here, and in sync_api_test.go, are blind to this class of
// leak on their own). This test reads the copy file's raw bytes, not SQL.
func TestRedactedJoinSnapshot_RealHashesNotRecoverableInRawBytes(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	hashes := []string{
		"7f8e9d0c1b2a3948576655443322110ffeeddccbbaa99887766554433221100",
		"1122334455667788990011223344556677889900112233445566778899aabb",
	}
	for i, h := range hashes {
		id := fmt.Sprintf("till-%d", i+1)
		if _, err := d.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES (?, ?, ?)`,
			id, "Till "+id, h); err != nil {
			t.Fatalf("seed till: %v", err)
		}
	}

	copyPath, cleanup, err := RedactedJoinSnapshot(d.DB, path)
	if err != nil {
		t.Fatalf("RedactedJoinSnapshot: %v", err)
	}
	defer cleanup()

	raw, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	for _, h := range hashes {
		if bytes.Contains(raw, []byte(h)) {
			t.Errorf("real bearer_hash %q recoverable in the served file's raw bytes — a joining replica's copy still physically contains it (ut-docs#426)", h)
		}
	}
}

// An empty tills table (fresh install serving its first join) must not error.
func TestRedactedJoinSnapshot_EmptyTillsTableIsFine(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)

	copyPath, cleanup, err := RedactedJoinSnapshot(d.DB, path)
	if err != nil {
		t.Fatalf("RedactedJoinSnapshot on empty tills: %v", err)
	}
	defer cleanup()
	if total, withHash := countTills(t, copyPath); total != 0 || withHash != 0 {
		t.Errorf("expected an empty tills table in the copy, got %d/%d", total, withHash)
	}
}

// ut-docs#436: a join-snapshot-*.db left behind by a crash between
// os.CreateTemp and cleanup() (simulated here directly, not by an actual
// crash) must be reaped once it's old enough to no longer plausibly be
// mid-request.
func TestSweepOrphanedJoinSnapshots_RemovesStaleOrphan(t *testing.T) {
	path := testDBPath(t)
	dir, err := BackupDir(path)
	if err != nil {
		t.Fatalf("backup dir: %v", err)
	}

	orphan := filepath.Join(dir, "join-snapshot-stale123.db")
	if err := os.WriteFile(orphan, []byte("stale copy"), 0o644); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(orphan+suffix, []byte("sidecar"), 0o644); err != nil {
			t.Fatalf("write sidecar %s: %v", suffix, err)
		}
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatalf("backdate orphan mtime: %v", err)
	}

	n, err := SweepOrphanedJoinSnapshots(path)
	if err != nil {
		t.Fatalf("SweepOrphanedJoinSnapshots: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	for _, p := range []string{orphan, orphan + "-wal", orphan + "-shm"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after sweep (stat err = %v)", p, err)
		}
	}
}

// A join-snapshot-*.db created moments ago (simulating one genuinely
// mid-request) must survive the sweep — only genuinely stale copies are
// orphans.
func TestSweepOrphanedJoinSnapshots_LeavesFreshCopyAlone(t *testing.T) {
	path := testDBPath(t)
	dir, err := BackupDir(path)
	if err != nil {
		t.Fatalf("backup dir: %v", err)
	}

	fresh := filepath.Join(dir, "join-snapshot-fresh456.db")
	if err := os.WriteFile(fresh, []byte("in-flight copy"), 0o644); err != nil {
		t.Fatalf("write fresh copy: %v", err)
	}

	n, err := SweepOrphanedJoinSnapshots(path)
	if err != nil {
		t.Fatalf("SweepOrphanedJoinSnapshots: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0 — a fresh in-flight copy must not be swept", n)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh copy removed by sweep: stat err = %v", err)
	}
}

// Real backup files (unitill-pos-*.db) must never be touched by this sweep,
// no matter how old they are.
func TestSweepOrphanedJoinSnapshots_NeverTouchesRealBackups(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)

	realBackup, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(realBackup, old, old); err != nil {
		t.Fatalf("backdate real backup mtime: %v", err)
	}

	if _, err := SweepOrphanedJoinSnapshots(path); err != nil {
		t.Fatalf("SweepOrphanedJoinSnapshots: %v", err)
	}
	if _, err := os.Stat(realBackup); err != nil {
		t.Errorf("real backup %s removed by join-snapshot sweep: stat err = %v", realBackup, err)
	}
}
