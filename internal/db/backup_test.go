package db

import (
	"database/sql"
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

// ut-docs#636: reset-archive batches (040_reset_archive.sql, ut-docs#187) are
// retained fiscal data, not disposable test data — a backup taken after a
// reset must restore them intact. `VACUUM INTO` rebuilds the entire logical
// database (every table in sqlite_schema), so this passes without any
// table-specific wiring; the test pins that behaviour so a future switch to
// a selective (per-table) backup mechanism can't silently drop the archive
// tables again.
func TestSnapshotAndRestore_PreservesResetArchiveBatch(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	if _, err := d.Exec(`INSERT INTO reset_batches (id, created_at, actor_id, sales_count) VALUES ('batch-1', '2026-08-16T00:00:00Z', 'user-1', 1)`); err != nil {
		t.Fatalf("seed reset_batches: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO sales_archive (
		id, receipt_no, status, sale_type, tender_type, offline, sync_status,
		sync_attempts, register_id, currency, subtotal, discount_total, tax_total,
		total, rounding, created_at, till_id, service_charge_amount, order_type,
		order_status, reset_batch_id
	) VALUES (
		'sale-1', 'R-1', 'completed', 'sale', 'cash', 0, 'synced',
		0, 'register-1', 'GBP', 1000, 0, 200,
		1200, 0, '2026-08-16T00:00:00Z', 'till-1', 0, 'counter',
		'completed', 'batch-1'
	)`); err != nil {
		t.Fatalf("seed sales_archive: %v", err)
	}

	snap, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// A plain connection, not Open(snap): Open runs migrations and flips
	// journal_mode, which would mutate this disaster-recovery artifact before
	// the restore below reads it back.
	sd, err := sql.Open("sqlite", snap)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	var batches, sales int
	if err := sd.QueryRow(`SELECT COUNT(*) FROM reset_batches WHERE id = 'batch-1'`).Scan(&batches); err != nil {
		t.Fatalf("count reset_batches in snapshot: %v", err)
	}
	if err := sd.QueryRow(`SELECT COUNT(*) FROM sales_archive WHERE reset_batch_id = 'batch-1'`).Scan(&sales); err != nil {
		t.Fatalf("count sales_archive in snapshot: %v", err)
	}
	sd.Close()
	if batches != 1 || sales != 1 {
		t.Fatalf("snapshot archive rows: batches=%d sales=%d, want 1 and 1", batches, sales)
	}

	// Full round-trip: mutate after the snapshot, restore, and confirm the
	// archived batch (not the mutation) is what comes back.
	if _, err := d.Exec(`DELETE FROM sales_archive WHERE id = 'sale-1'`); err != nil {
		t.Fatalf("mutate after snapshot: %v", err)
	}
	if err := StageRestore(path, filepath.Base(snap)); err != nil {
		t.Fatalf("stage: %v", err)
	}
	d.Close()
	if applied, err := ApplyPendingRestore(path); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	restored := openTest(t, path)
	var restoredSales int
	if err := restored.QueryRow(`SELECT COUNT(*) FROM sales_archive WHERE id = 'sale-1'`).Scan(&restoredSales); err != nil {
		t.Fatalf("count sales_archive after restore: %v", err)
	}
	if restoredSales != 1 {
		t.Errorf("restored sales_archive rows = %d, want 1 — reset-archive data did not survive a backup/restore round-trip", restoredSales)
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
