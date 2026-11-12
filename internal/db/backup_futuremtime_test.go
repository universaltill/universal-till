package db

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stampName is a snapshot file name stamped delta from now (UTC layout).
func stampName(now time.Time, delta time.Duration) string {
	return backupPrefix + now.UTC().Add(delta).Format(backupTimeLayout) + ".db"
}

// ut-docs#5523: a future mtime must not count as the newest backup forever.

func TestEffectiveTimeToleranceBoundary(t *testing.T) {
	now := time.Now()
	name := stampName(now, -72*time.Hour)
	cases := []struct {
		label     string
		mtime     time.Time
		wantKnown bool
		wantMtime bool // effective time is the mtime (else the name stamp)
	}{
		{"past", now.Add(-time.Hour), true, true},
		{"+4m trusted", now.Add(4 * time.Minute), true, true},
		{"exactly +5m trusted", now.Add(backupClockSkew), true, true},
		{"+6m not trusted, name used", now.Add(6 * time.Minute), true, false},
	}
	for _, c := range cases {
		got, ok := effectiveTime(name, c.mtime, now)
		if ok != c.wantKnown {
			t.Fatalf("%s: known=%v want %v", c.label, ok, c.wantKnown)
		}
		if c.wantMtime && !got.Equal(c.mtime) {
			t.Fatalf("%s: got %v want mtime %v", c.label, got, c.mtime)
		}
		if !c.wantMtime && got.After(now.Add(-71*time.Hour)) {
			t.Fatalf("%s: got %v, want the ~3d-old name time", c.label, got)
		}
	}
}

func TestFutureMtimePastNameUsesNameTime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	real := stampName(now, -2*time.Hour)
	bogus := stampName(now, -72*time.Hour)
	putBackup(t, dbPath, real, now, 2*time.Hour)
	putBackup(t, dbPath, bogus, now, -time.Hour) // mtime now+1h

	list, err := ListBackupsAt(dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != real || list[1].Name != bogus {
		t.Fatalf("order = %v, want real then bogus", list)
	}
	if !list[1].AgeKnown || now.Sub(list[1].EffectiveTime) < 71*time.Hour {
		t.Fatalf("bogus effective = %v known=%v, want ~3d old", list[1].EffectiveTime, list[1].AgeKnown)
	}
	if got := NewestOwnBackup(list); got != real {
		t.Fatalf("newest = %q, want %q", got, real)
	}
}

func TestFutureMtimeFutureNameIsUnknownAgeAndSortsLast(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	real := stampName(now, -26*time.Hour)
	futureName := stampName(now, 2*time.Hour)
	manual := backupPrefix + "manual.db"
	putBackup(t, dbPath, real, now, 26*time.Hour)
	putBackup(t, dbPath, futureName, now, -time.Hour)
	putBackup(t, dbPath, manual, now, -time.Hour)

	list, err := ListBackupsAt(dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Name != real {
		t.Fatalf("order = %v, want real first", list)
	}
	// Unknown ages sort by name desc: "manual" > "20..." lexically.
	if list[1].Name != manual || list[2].Name != futureName {
		t.Fatalf("unknown order = %q,%q", list[1].Name, list[2].Name)
	}
	for _, b := range list[1:] {
		if b.AgeKnown || !b.EffectiveTime.IsZero() {
			t.Fatalf("%s should be unknown age", b.Name)
		}
	}
	if got := NewestOwnBackup(list); got != real {
		t.Fatalf("newest = %q, want %q", got, real)
	}
}

func TestNewestOwnBackupOnlyUnknownAgeReturnsHighestName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	a := stampName(now, 2*time.Hour)
	b := stampName(now, 3*time.Hour)
	putBackup(t, dbPath, a, now, -time.Hour)
	putBackup(t, dbPath, b, now, -time.Hour)
	list, err := ListBackupsAt(dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := NewestOwnBackup(list); got != b {
		t.Fatalf("newest = %q, want %q", got, b)
	}
	n, err := PruneBackupsOlderThan(dbPath, 1, now)
	if err != nil || n != 0 {
		t.Fatalf("prune removed %d (err %v), want 0", n, err)
	}
}

func TestPruneBackupsOlderThanAgesFutureMtimeByNameAndKeepsUnknown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	real := stampName(now, -time.Hour)
	oldFuture := stampName(now, -10*24*time.Hour)
	unknown := stampName(now, 2*time.Hour)
	putBackup(t, dbPath, real, now, time.Hour)
	putBackup(t, dbPath, oldFuture, now, -time.Hour)
	putBackup(t, dbPath, unknown, now, -time.Hour)

	n, err := PruneBackupsOlderThan(dbPath, 7, now)
	if err != nil || n != 1 {
		t.Fatalf("removed %d (err %v), want 1", n, err)
	}
	dir, _ := BackupDir(dbPath)
	if _, err := os.Stat(filepath.Join(dir, oldFuture)); !os.IsNotExist(err) {
		t.Fatalf("old name-aged file should be gone: %v", err)
	}
	// Even at the 1-day minimum the unknown-age file survives.
	if _, err := PruneBackupsOlderThan(dbPath, 1, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, unknown)); err != nil {
		t.Fatalf("unknown-age file must be retained: %v", err)
	}
}

func TestPruneBackupsKeepDropsUnknownAgeFirst(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	older := stampName(now, -48*time.Hour)
	newer := stampName(now, -24*time.Hour)
	unknown := stampName(now, 2*time.Hour)
	putBackup(t, dbPath, older, now, 48*time.Hour)
	putBackup(t, dbPath, newer, now, 24*time.Hour)
	putBackup(t, dbPath, unknown, now, -time.Hour)

	if err := PruneBackups(dbPath, 2); err != nil {
		t.Fatal(err)
	}
	dir, _ := BackupDir(dbPath)
	for name, want := range map[string]bool{older: true, newer: true, unknown: false} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Fatalf("%s exists=%v want %v", name, err == nil, want)
		}
	}
}

func TestDeleteBackupUnknownAgeDeletableWhenKnownExists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	real := stampName(now, -time.Hour)
	unknown := stampName(now, 2*time.Hour)
	putBackup(t, dbPath, real, time.Now(), time.Hour)
	// DeleteBackup uses the real clock: +2h mtime is beyond tolerance.
	putBackup(t, dbPath, unknown, time.Now(), -time.Hour)
	if err := DeleteBackup(dbPath, unknown); err != nil {
		t.Fatalf("delete unknown-age: %v", err)
	}
	if err := DeleteBackup(dbPath, real); err != ErrNewestBackup {
		t.Fatalf("delete newest = %v, want ErrNewestBackup", err)
	}
}

func TestUploadedFutureMtimeStillIgnored(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	real := stampName(now, -time.Hour)
	up := backupPrefix + now.UTC().Add(-time.Minute).Format(backupTimeLayout) + "-file1.db"
	putBackup(t, dbPath, real, now, time.Hour)
	putBackup(t, dbPath, up, now, -time.Hour)
	list, err := ListBackupsAt(dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := NewestOwnBackup(list); got != real {
		t.Fatalf("newest = %q, want %q", got, real)
	}
	n, err := PruneBackupsOlderThan(dbPath, 1, now)
	if err != nil || n != 0 {
		t.Fatalf("removed %d (err %v), want 0", n, err)
	}
}

func TestListBackupsEqualEffectiveTimeTiesByName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	same := now.Add(-time.Hour)
	// Equal mtimes in the past, arbitrary names: higher name first.
	n1, n2 := stampName(now, -5*time.Hour), stampName(now, -4*time.Hour)
	putBackup(t, dbPath, n1, same, 0)
	putBackup(t, dbPath, n2, same, 0)
	list, err := ListBackupsAt(dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Name != n2 || list[1].Name != n1 {
		t.Fatalf("tie order = %q,%q", list[0].Name, list[1].Name)
	}
}

// uploadedName is an uploaded-backup name (-fileN) stamped delta from now.
func uploadedName(now time.Time, delta time.Duration, n int) string {
	return strings.TrimSuffix(stampName(now, delta), ".db") + "-file" + strconv.Itoa(n) + ".db"
}

// ut-docs#5523 review M1: uploads must never displace the newest own snapshot.
func TestPruneBackupsKeepsOnlyOwnUnknownAge(t *testing.T) {
	for _, tc := range []struct{ keep, uploads int }{{1, 1}, {3, 3}} {
		dbPath := filepath.Join(t.TempDir(), "till.db")
		now := time.Now()
		own := stampName(now, 2*time.Hour)
		putBackup(t, dbPath, own, now, -time.Hour) // mtime and name both in the future
		for i := 0; i < tc.uploads; i++ {
			putBackup(t, dbPath, uploadedName(now, -time.Duration(i+1)*time.Minute, i), now, 0)
		}
		if err := PruneBackups(dbPath, tc.keep); err != nil {
			t.Fatal(err)
		}
		if !hasBackup(t, dbPath, own) {
			t.Fatalf("keep=%d: the only own backup was deleted", tc.keep)
		}
		list, _ := ListBackups(dbPath)
		if len(list) > tc.keep {
			t.Fatalf("keep=%d: %d files left", tc.keep, len(list))
		}
	}
}

func TestPruneBackupsKeepsOlderKnownAgeOwn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	now := time.Now()
	own := stampName(now, -48*time.Hour)
	putBackup(t, dbPath, own, now, 48*time.Hour)
	for i := 0; i < 3; i++ {
		putBackup(t, dbPath, uploadedName(now, -time.Duration(i+1)*time.Minute, i), now, 0)
	}
	if err := PruneBackups(dbPath, 3); err != nil {
		t.Fatal(err)
	}
	if !hasBackup(t, dbPath, own) {
		t.Fatal("the older own backup was displaced by uploads")
	}
	if list, _ := ListBackups(dbPath); len(list) > 3 {
		t.Fatalf("%d files left, want <= 3", len(list))
	}
}

// ut-docs#5523 review L2: the -fileN suffix must not hide the name stamp.
func TestUploadedFutureMtimePastStampAgeKnown(t *testing.T) {
	now := time.Now()
	name := uploadedName(now, -72*time.Hour, 2)
	got, ok := effectiveTime(name, now.Add(time.Hour), now)
	if !ok || now.Sub(got) < 71*time.Hour {
		t.Fatalf("effective=%v known=%v, want known ~3d old", got, ok)
	}
}
