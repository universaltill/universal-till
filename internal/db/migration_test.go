package db

import "testing"

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
