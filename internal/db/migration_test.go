package db

import (
	"testing"
	"testing/fstest"
)

// TestCheckNoDuplicateVersions_DetectsCollision reproduces the real
// ut-docs#1056 collision by hand (067_vouchers.sql vs
// 067_shift_cash_reconciliation.sql) — this is the exact shape that used
// to let sort.Slice silently pick a winner with no defined tie-break.
func TestCheckNoDuplicateVersions_DetectsCollision(t *testing.T) {
	migs := []migration{
		{Version: 67, Name: "067_vouchers.sql"},
		{Version: 67, Name: "067_shift_cash_reconciliation.sql"},
	}
	err := checkNoDuplicateVersions(migs)
	if err == nil {
		t.Fatal("expected an error for two migrations sharing version 67, got nil")
	}
}

// TestCheckNoDuplicateVersions_AllUnique proves the check doesn't
// false-positive on ordinary, non-colliding migrations.
func TestCheckNoDuplicateVersions_AllUnique(t *testing.T) {
	migs := []migration{
		{Version: 1, Name: "001_init.sql"},
		{Version: 2, Name: "002_next.sql"},
		{Version: 67, Name: "067_vouchers.sql"},
	}
	if err := checkNoDuplicateVersions(migs); err != nil {
		t.Fatalf("unexpected error on unique versions: %v", err)
	}
}

// TestCheckNoDuplicateVersions_Empty guards the zero-migration edge case
// (an empty slice is not itself a collision).
func TestCheckNoDuplicateVersions_Empty(t *testing.T) {
	if err := checkNoDuplicateVersions(nil); err != nil {
		t.Fatalf("unexpected error on an empty migration set: %v", err)
	}
}

// TestLoadMigrations_RealFilesHaveNoDuplicateVersions is the belt to
// scripts/ci/guard-migration-version-collision.sh's braces (ut-docs#1056):
// this runs at `go test ./...` time, on every build, so a future collision
// in the real embedded migrations directory fails loudly here even if the
// shell guard were ever skipped.
func TestLoadMigrations_RealFilesHaveNoDuplicateVersions(t *testing.T) {
	if _, err := loadMigrations(); err != nil {
		t.Fatalf("loadMigrations on the real embedded migrations dir: %v", err)
	}
}

// TestLoadMigrationsFromFS_PropagatesDuplicateVersionError proves the
// *wiring*, not just checkNoDuplicateVersions in isolation: that
// loadMigrations's real read→parse→dedup→sort path actually calls it and
// actually returns its error. //go:embed can't be fed a fake collision (it
// only ever sees the real on-disk migrations/ directory), which is exactly
// why loadMigrations delegates to loadMigrationsFromFS over an fs.FS — this
// test exercises that seam directly with an injected fstest.MapFS
// reproducing the real ut-docs#1056 collision shape. Without this, every
// other test here could pass while a future refactor silently dropped the
// checkNoDuplicateVersions call from loadMigrationsFromFS and nothing would
// catch it.
func TestLoadMigrationsFromFS_PropagatesDuplicateVersionError(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/067_vouchers.sql":                  &fstest.MapFile{Data: []byte("-- vouchers")},
		"migrations/067_shift_cash_reconciliation.sql": &fstest.MapFile{Data: []byte("-- shift cash")},
	}
	_, err := loadMigrationsFromFS(fsys, "migrations")
	if err == nil {
		t.Fatal("expected loadMigrationsFromFS to reject two files sharing version 67, got nil error")
	}
}

// TestLoadMigrationsFromFS_UniqueVersionsLoadCleanly proves the new seam
// doesn't false-positive on an ordinary, non-colliding fixture set.
func TestLoadMigrationsFromFS_UniqueVersionsLoadCleanly(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/001_init.sql":     &fstest.MapFile{Data: []byte("-- init")},
		"migrations/002_next.sql":     &fstest.MapFile{Data: []byte("-- next")},
		"migrations/067_vouchers.sql": &fstest.MapFile{Data: []byte("-- vouchers")},
	}
	migs, err := loadMigrationsFromFS(fsys, "migrations")
	if err != nil {
		t.Fatalf("unexpected error on unique versions: %v", err)
	}
	if len(migs) != 3 {
		t.Fatalf("expected 3 migrations, got %d", len(migs))
	}
}
