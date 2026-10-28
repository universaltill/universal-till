package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crashedSwap builds the state a crash between the two renames of a restore
// leaves behind: no DB file, the staged file and the journal in place, the
// live data in a pre-restore copy (ut-docs#5089).
func crashedSwap(t *testing.T, withPending bool) (path, aside string) {
	t.Helper()
	path = testDBPath(t)
	d := openTest(t, path)
	if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES ('marker', 'live')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE settings SET value = 'staged' WHERE key = 'marker'`); err != nil {
		t.Fatal(err)
	}
	staged, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE settings SET value = 'live' WHERE key = 'marker'`); err != nil {
		t.Fatal(err)
	}
	d.Close()

	dir, _ := BackupDir(path)
	aside = filepath.Join(dir, preRestorePrefix+"20260101-000000.db")
	if err := os.Rename(path, aside); err != nil {
		t.Fatal(err)
	}
	if withPending {
		if err := os.Rename(staged, filepath.Join(filepath.Dir(path), restorePendingName)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeRestoreSwap(path, filepath.Base(aside)); err != nil {
		t.Fatal(err)
	}
	return path, aside
}

func markerOf(t *testing.T, path string) string {
	t.Helper()
	d := openTest(t, path)
	defer d.Close()
	var v string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRecoverInterruptedRestoreFinishesStagedSwap(t *testing.T) {
	path, _ := crashedSwap(t, true)
	applied, err := ApplyPendingRestore(path)
	if err != nil || !applied {
		t.Fatalf("apply after crash: %v %v", applied, err)
	}
	if got := markerOf(t, path); got != "staged" {
		t.Fatalf("marker = %q, want the staged restore", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), restoreSwapName)); err == nil {
		t.Fatal("journal left behind")
	}
}

func TestRecoverInterruptedRestoreUndoesWhenStagedFileGone(t *testing.T) {
	path, _ := crashedSwap(t, false)
	applied, err := RecoverInterruptedRestore(path)
	if err != nil || applied {
		t.Fatalf("recover: %v %v", applied, err)
	}
	if got := markerOf(t, path); got != "live" {
		t.Fatalf("marker = %q, want the pre-restore data back", got)
	}
}

func TestRecoverInterruptedRestoreDropsJournalWhenDBPresent(t *testing.T) {
	path := testDBPath(t)
	openTest(t, path).Close()
	if err := writeRestoreSwap(path, "pre-restore-20260101-000000.db"); err != nil {
		t.Fatal(err)
	}
	if applied, err := RecoverInterruptedRestore(path); err != nil || applied {
		t.Fatalf("recover: %v %v", applied, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), restoreSwapName)); err == nil {
		t.Fatal("journal kept although the DB file exists")
	}
}

func TestRecoverInterruptedRestoreRefusesJournalOutsideBackups(t *testing.T) {
	path := testDBPath(t)
	if err := writeRestoreSwap(path, "../evil.db"); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverInterruptedRestore(path); err == nil || !strings.Contains(err.Error(), "unexpected file") {
		t.Fatalf("err = %v, want refusal", err)
	}
}

func TestApplyPendingRestoreLeavesNoJournal(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	snap, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := StageRestore(path, filepath.Base(snap)); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if applied, err := ApplyPendingRestore(path); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), restoreSwapName)); err == nil {
		t.Fatal("journal left behind after a clean swap")
	}
}

func TestRecoverInterruptedRestoreDropsOldSidecarsBeforeFinishing(t *testing.T) {
	path, _ := crashedSwap(t, true)
	if err := os.WriteFile(path+"-wal", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if applied, err := RecoverInterruptedRestore(path); err != nil || !applied {
		t.Fatalf("recover: %v %v", applied, err)
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		t.Fatal("old DB's wal kept next to the restored file")
	}
	if got := markerOf(t, path); got != "staged" {
		t.Fatalf("marker = %q", got)
	}
}

func TestRestoreSwapPending(t *testing.T) {
	path := testDBPath(t)
	if RestoreSwapPending(path) {
		t.Fatal("pending without a journal")
	}
	if err := writeRestoreSwap(path, "pre-restore-x.db"); err != nil {
		t.Fatal(err)
	}
	if !RestoreSwapPending(path) {
		t.Fatal("journal not seen")
	}
}
