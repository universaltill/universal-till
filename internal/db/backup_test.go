package db

import (
	"os"
	"path/filepath"
	"testing"
)

func testDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "unitill-pos.db")
}

func openTest(t *testing.T, path string) *DB {
	t.Helper()
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestSnapshotAndListAndPrune(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES ('marker', 'v1')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	snap, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// The snapshot is a valid SQLite DB containing the data.
	sd, err := Open(snap)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	var v string
	if err := sd.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v); err != nil || v != "v1" {
		t.Fatalf("snapshot data: %q %v", v, err)
	}
	sd.Close()

	list, err := ListBackups(path)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if err := PruneBackups(path, 1); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if list, _ = ListBackups(path); len(list) != 1 {
		t.Fatalf("prune must keep the newest, got %d", len(list))
	}
	// keep=0 clamps to 1 — never delete every backup.
	if err := PruneBackups(path, 0); err != nil {
		t.Fatal(err)
	}
	if list, _ = ListBackups(path); len(list) != 1 {
		t.Fatalf("keep=0 must clamp to 1, got %d", len(list))
	}
}

func TestValidBackupName(t *testing.T) {
	good := "unitill-pos-20260714-120000.db"
	if !ValidBackupName(good) {
		t.Error("valid name rejected")
	}
	for _, bad := range []string{"../../etc/passwd", "unitill-pos-x.txt", "other.db", "unitill-pos-/../x.db"} {
		if ValidBackupName(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestStageAndApplyRestore(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES ('marker', 'before')`); err != nil {
		t.Fatal(err)
	}
	snap, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate after the snapshot; the restore must roll this back.
	if _, err := d.Exec(`UPDATE settings SET value = 'after' WHERE key = 'marker'`); err != nil {
		t.Fatal(err)
	}
	if err := StageRestore(path, filepath.Base(snap)); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !PendingRestore(path) {
		t.Fatal("pending restore not detected")
	}
	d.Close()

	applied, err := ApplyPendingRestore(path)
	if err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	restored := openTest(t, path)
	var v string
	if err := restored.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v); err != nil || v != "before" {
		t.Fatalf("restored marker = %q %v, want before", v, err)
	}
	// The replaced DB is preserved.
	dir, _ := BackupDir(path)
	entries, _ := os.ReadDir(dir)
	preFound := false
	for _, e := range entries {
		if len(e.Name()) > 11 && e.Name()[:11] == "pre-restore" {
			preFound = true
		}
	}
	if !preFound {
		t.Error("pre-restore copy of the replaced DB missing")
	}
	// No pending restore left; a second apply is a no-op.
	if applied, err := ApplyPendingRestore(path); err != nil || applied {
		t.Fatalf("second apply: %v %v", applied, err)
	}
}

func TestStageRestoreRejectsBadNames(t *testing.T) {
	path := testDBPath(t)
	openTest(t, path)
	if err := StageRestore(path, "../evil.db"); err == nil {
		t.Error("path traversal accepted")
	}
	if err := StageRestore(path, "unitill-pos-99999999-000000.db"); err == nil {
		t.Error("missing backup accepted")
	}
}
