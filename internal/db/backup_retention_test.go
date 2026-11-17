package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// putBackup writes a fake backup file whose mtime is age before now.
func putBackup(t *testing.T, dbPath, name string, now time.Time, age time.Duration) {
	t.Helper()
	dir, err := BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := now.Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func hasBackup(t *testing.T, dbPath, name string) bool {
	t.Helper()
	dir, _ := BackupDir(dbPath)
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// ut-docs#3092 (reopened): the owner's rule is "keep the last 7 days", not
// "keep 14 files". 14 daily backups must be pruned to the last 7 days.
func TestPruneBackupsOlderThanKeepsTheLastNDays(t *testing.T) {
	path := testDBPath(t)
	now := time.Now()
	for i := 0; i < 14; i++ {
		putBackup(t, path, fmt.Sprintf("unitill-pos-2026010%02d-030000.db", i), now, time.Duration(i)*24*time.Hour+time.Hour)
	}
	n, err := PruneBackupsOlderThan(path, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	// ages 1h,25h,...,145h (days 0..6) stay; 169h (day 7) and older go.
	if n != 7 {
		t.Fatalf("removed %d, want 7", n)
	}
	list, _ := ListBackups(path)
	if len(list) != 7 {
		t.Fatalf("%d backups left, want 7", len(list))
	}
}

func TestPruneBackupsOlderThanNeverDeletesTheNewest(t *testing.T) {
	path := testDBPath(t)
	now := time.Now()
	putBackup(t, path, "unitill-pos-20200101-000000.db", now, 400*24*time.Hour)
	putBackup(t, path, "unitill-pos-20200102-000000.db", now, 399*24*time.Hour)
	if _, err := PruneBackupsOlderThan(path, 7, now); err != nil {
		t.Fatal(err)
	}
	if !hasBackup(t, path, "unitill-pos-20200102-000000.db") {
		t.Fatal("the newest backup was deleted although every backup is old")
	}
	if hasBackup(t, path, "unitill-pos-20200101-000000.db") {
		t.Fatal("the older backup was kept")
	}
}

func TestPruneBackupsOlderThanLeavesPreRestoreAndUploadsAlone(t *testing.T) {
	path := testDBPath(t)
	now := time.Now()
	putBackup(t, path, "unitill-pos-20260101-000000.db", now, time.Hour)
	putBackup(t, path, "pre-restore-20200101-000000.db", now, 400*24*time.Hour)
	putBackup(t, path, "unitill-pos-20200101-000000-file1.db", now, 400*24*time.Hour)
	if _, err := PruneBackupsOlderThan(path, 7, now); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"pre-restore-20200101-000000.db", "unitill-pos-20200101-000000-file1.db"} {
		if !hasBackup(t, path, n) {
			t.Fatalf("%s was removed by the snapshot age prune", n)
		}
	}
}

func TestPruneBackupsOlderThanNeverBelowOneDay(t *testing.T) {
	path := testDBPath(t)
	now := time.Now()
	putBackup(t, path, "unitill-pos-20260101-000000.db", now, time.Hour)
	putBackup(t, path, "unitill-pos-20260102-000000.db", now, 3*time.Hour)
	putBackup(t, path, "unitill-pos-20260103-000000.db", now, 30*time.Hour)
	if _, err := PruneBackupsOlderThan(path, 0, now); err != nil {
		t.Fatal(err)
	}
	list, _ := ListBackups(path)
	if len(list) != 2 {
		t.Fatalf("days=0 left %d backups, want the 2 within a day", len(list))
	}
}

func TestDeleteBackup(t *testing.T) {
	path := testDBPath(t)
	now := time.Now()
	putBackup(t, path, "unitill-pos-20260101-000000.db", now, time.Hour)
	putBackup(t, path, "unitill-pos-20260102-000000.db", now, 48*time.Hour)

	if err := DeleteBackup(path, "unitill-pos-20260101-000000.db"); !errors.Is(err, ErrNewestBackup) {
		t.Fatalf("deleting the newest: %v, want ErrNewestBackup", err)
	}
	if !hasBackup(t, path, "unitill-pos-20260101-000000.db") {
		t.Fatal("newest backup was deleted")
	}
	for _, bad := range []string{"../unitill-pos-x.db", "pre-restore-20260101-000000.db", "restore-pending.db", "x.db", ""} {
		if err := DeleteBackup(path, bad); !errors.Is(err, ErrInvalidBackupName) {
			t.Fatalf("DeleteBackup(%q) = %v, want ErrInvalidBackupName", bad, err)
		}
	}
	if err := DeleteBackup(path, "unitill-pos-29990101-000000.db"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("missing backup: %v, want ErrBackupNotFound", err)
	}
	if err := DeleteBackup(path, "unitill-pos-20260102-000000.db"); err != nil {
		t.Fatalf("delete older: %v", err)
	}
	if hasBackup(t, path, "unitill-pos-20260102-000000.db") {
		t.Fatal("older backup still there")
	}
}

// An uploaded file (ut-docs#5070) is renamed into place with mtime = now, so
// it sorts first. It must not take over the "newest" protection from the
// till's own newest snapshot (review of ut-docs#3092).
func TestNewestProtectionIsAboutTheTillsOwnSnapshots(t *testing.T) {
	path := testDBPath(t)
	now := time.Now()
	own := "unitill-pos-20200101-000000.db"
	oldOwn := "unitill-pos-20190101-000000.db"
	upload := "unitill-pos-20260101-000000-file1.db"
	putBackup(t, path, oldOwn, now, 500*24*time.Hour)
	putBackup(t, path, own, now, 400*24*time.Hour)
	putBackup(t, path, upload, now, time.Minute)

	if err := DeleteBackup(path, own); !errors.Is(err, ErrNewestBackup) {
		t.Fatalf("deleting the till's newest own snapshot: %v, want ErrNewestBackup", err)
	}
	if n, err := PruneBackupsOlderThan(path, 7, now); err != nil || n != 1 {
		t.Fatalf("prune removed %d (%v), want only the older own snapshot", n, err)
	}
	if !hasBackup(t, path, own) || !hasBackup(t, path, upload) || hasBackup(t, path, oldOwn) {
		t.Fatal("wrong files kept after the age prune")
	}
	if err := DeleteBackup(path, upload); err != nil {
		t.Fatalf("a manager must be able to delete their own upload: %v", err)
	}
}

func TestPruneBackupsOlderThanSkipsNamedFiles(t *testing.T) {
	path := testDBPath(t)
	now := time.Now()
	putBackup(t, path, "unitill-pos-20260101-000000.db", now, time.Hour)
	putBackup(t, path, "unitill-pos-20200101-000000.db", now, 400*24*time.Hour)
	if n, _ := PruneBackupsOlderThan(path, 7, now, "unitill-pos-20200101-000000.db"); n != 0 {
		t.Fatalf("removed %d, want the named restore source kept", n)
	}
}

// Equal mtimes must not make the order (and so "newest") change between calls.
func TestListBackupsTieBreaksByName(t *testing.T) {
	path := testDBPath(t)
	now := time.Now()
	for _, n := range []string{"unitill-pos-20260101-000002.db", "unitill-pos-20260101-000001.db", "unitill-pos-20260101-000003.db"} {
		putBackup(t, path, n, now, time.Hour)
	}
	for i := 0; i < 20; i++ {
		list, _ := ListBackups(path)
		if list[0].Name != "unitill-pos-20260101-000003.db" {
			t.Fatalf("run %d: newest = %s", i, list[0].Name)
		}
	}
}
