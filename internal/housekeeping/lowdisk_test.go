package housekeeping

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// putSized writes a file of n bytes with the given mtime.
func putSized(t *testing.T, root, rel string, n int, mod time.Time) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// snapshots writes n daily snapshots, newest first (day 0 = testNow).
func snapshots(t *testing.T, root string, n, size int) []string {
	t.Helper()
	var names []string
	for i := 0; i < n; i++ {
		at := testNow.Add(-time.Duration(i) * 24 * time.Hour)
		name := fmt.Sprintf("backups/unitill-pos-%s.db", at.Format("20060102-150405"))
		putSized(t, root, name, size, at)
		names = append(names, name)
	}
	return names
}

// ut-docs#3121 AC 2: below the floor each kind is pruned to its minimum —
// the newest 3 backups, the newest restore copy outside the statutory floor,
// unsent bug reports and update downloads older than a day — while the
// normal limits would have kept all of them.
func TestRunWith_LowDiskPrunesEachKindToItsMinimum(t *testing.T) {
	root, dbPath := layout(t)
	snaps := snapshots(t, root, 10, 10)
	put(t, root, "backups/pre-restore-20260926-120000.db", testNow.Add(-48*time.Hour))
	put(t, root, "backups/pre-restore-20260928-000000.db", testNow.Add(-12*time.Hour))
	put(t, root, "issue-reports/pending/old/report.json", testNow.Add(-36*time.Hour))
	put(t, root, "issue-reports/pending/new/report.json", testNow.Add(-time.Hour))
	age(t, root, "issue-reports/pending/old", testNow.Add(-36*time.Hour))
	age(t, root, "issue-reports/pending/new", testNow.Add(-time.Hour))

	// Normal limits: nothing here is past them.
	for _, r := range RunWith(dbPath, testNow, 0, Normal) {
		if r.Removed != 0 {
			t.Fatalf("normal limits removed %d %s", r.Removed, r.Kind)
		}
	}

	got := map[string]Result{}
	for _, r := range RunWith(dbPath, testNow, 0, LowDisk) {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Kind, r.Err)
		}
		got[r.Kind] = r
	}
	have := files(t, root)
	for i, s := range snaps {
		if want := i < MinBackupKeep; have[s] != want {
			t.Errorf("snapshot %d (%s) kept=%v, want %v", i, s, have[s], want)
		}
	}
	if got[KindBackups].Removed != 10-MinBackupKeep || got[KindBackups].Freed != int64(10*(10-MinBackupKeep)) {
		t.Errorf("backups result %+v, want %d removed", got[KindBackups], 10-MinBackupKeep)
	}
	if have["backups/pre-restore-20260926-120000.db"] || !have["backups/pre-restore-20260928-000000.db"] {
		t.Errorf("pre-restore: want only the newest kept, have %v", have)
	}
	if have["issue-reports/pending/old/report.json"] || !have["issue-reports/pending/new/report.json"] {
		t.Errorf("issue reports: want only the one under a day old kept, have %v", have)
	}
}

// The statutory floor still binds below the disk floor: a restore copy
// inside the shop's archive retention is never removed, however full the
// disk (ADR-0040, ut-docs#3365).
func TestRunWith_LowDiskStillKeepsRestoreCopiesInsideTheStatutoryFloor(t *testing.T) {
	root, dbPath := layout(t)
	put(t, root, "backups/pre-restore-20260901-120000.db", testNow.Add(-27*24*time.Hour))
	put(t, root, "backups/pre-restore-20260928-000000.db", testNow.Add(-12*time.Hour))
	RunWith(dbPath, testNow, 10*365*24*time.Hour, LowDisk)
	have := files(t, root)
	if !have["backups/pre-restore-20260901-120000.db"] || !have["backups/pre-restore-20260928-000000.db"] {
		t.Fatalf("low disk removed a restore copy inside the statutory floor: %v", have)
	}
}

// The low-disk limits are never looser than the normal ones.
func TestLowDiskLimitsAreTighter(t *testing.T) {
	if LowDisk.BackupKeep > Normal.BackupKeep || LowDisk.PreRestoreKeep > Normal.PreRestoreKeep ||
		LowDisk.PreRestoreMaxAge > Normal.PreRestoreMaxAge || LowDisk.IssueReportMaxAge > Normal.IssueReportMaxAge ||
		LowDisk.UpdateDownloadMaxAge > Normal.UpdateDownloadMaxAge {
		t.Fatalf("LowDisk %+v looser than Normal %+v", LowDisk, Normal)
	}
	if LowDisk.BackupKeep != MinBackupKeep || MinBackupKeep < 3 {
		t.Fatalf("low disk keeps %d backups, the card requires at least 3", LowDisk.BackupKeep)
	}
}

// ut-docs#3121 AC 4: the snapshots together use at most BackupSharePercent
// of the free space they could release (free + their own size); the oldest
// go first, never below MinBackupKeep.
func TestCapBackups(t *testing.T) {
	for _, c := range []struct {
		name     string
		n, size  int
		free     uint64
		wantKept int
	}{
		// 10×100 B, free 3000: budget 25% of 4000 = 1000 → all fit.
		{"fits", 10, 100, 3000, 10},
		// 10×100 B, free 1000: budget 25% of 2000 = 500 → newest 5.
		{"over budget", 10, 100, 1000, 5},
		// Nearly full disk: the budget would keep 0, the floor keeps 3.
		{"never below the minimum", 10, 100, 0, MinBackupKeep},
		// Fewer than the minimum: nothing to do.
		{"few", 2, 100, 0, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, dbPath := layout(t)
			snaps := snapshots(t, root, c.n, c.size)
			res := CapBackups(dbPath, c.free)
			if res.Err != nil {
				t.Fatal(res.Err)
			}
			have := files(t, root)
			for i, s := range snaps {
				if want := i < c.wantKept; have[s] != want {
					t.Errorf("snapshot %d kept=%v, want %v", i, have[s], want)
				}
			}
			if res.Kind != KindBackups || res.Removed != c.n-c.wantKept || res.Freed != int64((c.n-c.wantKept)*c.size) {
				t.Errorf("result %+v, want %d removed", res, c.n-c.wantKept)
			}
		})
	}
}

// The cap never touches restore copies or anything else in backups/.
func TestCapBackups_OnlySnapshots(t *testing.T) {
	root, dbPath := layout(t)
	snapshots(t, root, 5, 100)
	put(t, root, "backups/pre-restore-20200101-000000.db", testNow)
	put(t, root, "backups/notes.txt", testNow)
	CapBackups(dbPath, 0)
	have := files(t, root)
	if !have["backups/pre-restore-20200101-000000.db"] || !have["backups/notes.txt"] {
		t.Fatalf("cap removed a non-snapshot file: %v", have)
	}
}

// No backups directory yet (a fresh till) is not an error.
func TestCapBackups_NoBackupsYet(t *testing.T) {
	_, dbPath := layout(t)
	if res := CapBackups(dbPath, 0); res.Err != nil || res.Removed != 0 {
		t.Fatalf("fresh till: %+v", res)
	}
}
