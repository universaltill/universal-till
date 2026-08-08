package db

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
