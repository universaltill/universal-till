package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeAged creates name in dir with the given modification time.
func writeAged(t *testing.T, dir, name string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return p
}

// ut-docs#3092: every restore leaves a full DB copy as pre-restore-<ts>.db,
// and nothing ever removed them. Keep the newest 3, none older than 30 days,
// and never touch a real snapshot or anything else in the backup dir.
func TestPrunePreRestore_KeepsNewestAndDropsOld(t *testing.T) {
	dbPath := testDBPath(t)
	dir, err := BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	fresh := []string{
		writeAged(t, dir, "pre-restore-20260928-110000.db", now.Add(-1*time.Hour)),
		writeAged(t, dir, "pre-restore-20260927-110000.db", now.Add(-1*day)),
		writeAged(t, dir, "pre-restore-20260926-110000.db", now.Add(-2*day)),
	}
	overCount := writeAged(t, dir, "pre-restore-20260925-110000.db", now.Add(-3*day))
	// Alone it would be within the count, but it is past the age limit.
	snapshot := writeAged(t, dir, backupPrefix+"20200101-000000.db", now.Add(-1000*day))
	other := writeAged(t, dir, "join-snapshot-abc.db", now.Add(-1000*day))
	liveNeighbour := writeAged(t, filepath.Dir(dbPath), "pre-restore-20200101-000000.db", now.Add(-1000*day))

	n, freed, err := PrunePreRestore(dbPath, 3, 30*day, now, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 || freed != 1 {
		t.Fatalf("removed %d files / %d bytes, want 1 / 1", n, freed)
	}
	for _, p := range fresh {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("newest pre-restore copy removed: %s", p)
		}
	}
	if _, err := os.Stat(overCount); !os.IsNotExist(err) {
		t.Errorf("4th-newest pre-restore copy kept: %v", err)
	}
	for _, p := range []string{snapshot, other, liveNeighbour} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file outside the pre-restore namespace removed: %s", p)
		}
	}
}

func TestPrunePreRestore_AgeLimitBeatsCount(t *testing.T) {
	dbPath := testDBPath(t)
	dir, _ := BackupDir(dbPath)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	keep := writeAged(t, dir, "pre-restore-20260920-000000.db", now.Add(-8*day))
	old := writeAged(t, dir, "pre-restore-20260801-000000.db", now.Add(-31*day))

	n, _, err := PrunePreRestore(dbPath, 3, 30*day, now, 0)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1, nil", n, err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("recent copy removed")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("31-day-old copy kept")
	}
}

// Review finding (ut-docs#3092): os.Rename keeps the old database's mtime,
// so a till idle for over 30 days that restores a backup would have its
// fresh safety copy look ancient. The age and the order come from the
// restore time in the file name, not the mtime.
func TestPrunePreRestore_AgesByRestoreTimeInName(t *testing.T) {
	dbPath := testDBPath(t)
	dir, _ := BackupDir(dbPath)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	ancientMtime := now.Add(-90 * 24 * time.Hour)
	justRestored := writeAged(t, dir, "pre-restore-20260928-115800.db", ancientMtime)
	older := []string{
		writeAged(t, dir, "pre-restore-20260927-000000.db", now),
		writeAged(t, dir, "pre-restore-20260926-000000.db", now),
		writeAged(t, dir, "pre-restore-20260925-000000.db", now),
	}

	n, _, err := PrunePreRestore(dbPath, 3, 30*24*time.Hour, now, 0)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1, nil", n, err)
	}
	if _, err := os.Stat(justRestored); err != nil {
		t.Fatal("the copy set aside two minutes ago was removed")
	}
	if _, err := os.Stat(older[2]); !os.IsNotExist(err) {
		t.Error("the oldest-by-name copy was kept over the just-restored one")
	}
}

// ut-docs#3365: a pre-restore copy is a full database, so it holds sales,
// payments, invoices, Z reports and the audit log. A copy still inside the
// statutory retention floor is kept even when it is past maxAge and beyond
// the keep count; the floor overrides both rules.
func TestPrunePreRestore_StatutoryFloorOverridesCountAndAge(t *testing.T) {
	dbPath := testDBPath(t)
	dir, _ := BackupDir(dbPath)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	files := []string{
		writeAged(t, dir, "pre-restore-20260927-120000.db", now),
		writeAged(t, dir, "pre-restore-20260926-120000.db", now),
		writeAged(t, dir, "pre-restore-20260925-120000.db", now),
		// 10 days old: past the 5-day maxAge and the 4th-newest, so the
		// count and age rules would both remove it.
		writeAged(t, dir, "pre-restore-20260918-120000.db", now),
	}

	n, _, err := PrunePreRestore(dbPath, 3, 5*day, now, 3650*day)
	if err != nil || n != 0 {
		t.Fatalf("prune = %d, %v; want 0, nil", n, err)
	}
	for _, p := range files {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("copy inside the statutory floor removed: %s", p)
		}
	}

	// keep=0: the count rule alone would remove every copy.
	n, _, err = PrunePreRestore(dbPath, 0, 5*day, now, 3650*day)
	if err != nil || n != 0 {
		t.Fatalf("prune keep=0 = %d, %v; want 0, nil", n, err)
	}
}

// ut-docs#3365: the floor is not "keep forever". A copy older than the
// floor that is also past the count/age limits is still removed.
func TestPrunePreRestore_PastStatutoryFloorStillPruned(t *testing.T) {
	dbPath := testDBPath(t)
	dir, _ := BackupDir(dbPath)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	young := writeAged(t, dir, "pre-restore-20260918-120000.db", now)   // 10 days
	ancient := writeAged(t, dir, "pre-restore-20150101-000000.db", now) // ~11.7 years

	n, _, err := PrunePreRestore(dbPath, 3, 30*day, now, 3650*day)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1, nil", n, err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Error("young copy removed")
	}
	if _, err := os.Stat(ancient); !os.IsNotExist(err) {
		t.Error("copy past the statutory floor and the age limit was kept")
	}
}
