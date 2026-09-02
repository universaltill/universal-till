package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// withBaseline returns the real embedded migration set (just 001_init.sql,
// per ADR-0074) plus extra — verifyAppliedMigrations' contract (matching how
// migrate() actually calls it) is "the complete set of migrations currently
// on disk," not an arbitrary subset: since Open already applies the real
// baseline, its ledger row is always present too, and the reverse-direction
// orphan check (ut-docs#1425 review finding F2) would otherwise misread an
// incomplete test-only slice as the baseline itself having been renumbered
// away.
func withBaseline(t *testing.T, extra ...migration) []migration {
	t.Helper()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	return append(migs, extra...)
}

// TestVerifyAppliedMigrations_DetectsRenameAndEdit exercises the drift
// guard (ADR-0074 Decision 3, ut-docs#1425) directly with synthetic
// migrations recorded through the real applyMigration, so it needs no
// on-disk fixture: an applied version whose file is later renamed or whose
// statements are later edited must fail boot; a comment-only edit, an
// unapplied version, and an intact file must not.
func TestVerifyAppliedMigrations_DetectsRenameAndEdit(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "drift.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	m := migration{Version: 9001, Name: "9001_synthetic.sql", SQL: "-- probe\nCREATE TABLE IF NOT EXISTS drift_probe (n INTEGER);\n"}
	if err := d.applyMigration(m); err != nil {
		t.Fatal(err)
	}

	if err := d.verifyAppliedMigrations(withBaseline(t, m), 9001); err != nil {
		t.Fatalf("intact file must verify clean: %v", err)
	}

	renamed := m
	renamed.Name = "9001_renumbered_later.sql"
	err = d.verifyAppliedMigrations(withBaseline(t, renamed), 9001)
	if err == nil {
		t.Fatal("renamed file under an applied version must fail")
	}
	for _, want := range []string{"migration 9001", `"9001_synthetic.sql"`, `"9001_renumbered_later.sql"`, "renamed or edited"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("rename error %q missing %q", err.Error(), want)
		}
	}

	edited := m
	edited.SQL = m.SQL + "CREATE TABLE IF NOT EXISTS drift_probe_2 (n INTEGER);\n"
	err = d.verifyAppliedMigrations(withBaseline(t, edited), 9001)
	if err == nil {
		t.Fatal("edited statements under an applied version must fail")
	}
	if !strings.Contains(err.Error(), migrationChecksum(m.SQL)) || !strings.Contains(err.Error(), migrationChecksum(edited.SQL)) {
		t.Errorf("edit error %q must name both checksums", err.Error())
	}
	if n := columnCount(t, d, "drift_probe_2", "n"); n != 0 {
		t.Fatal("a failed verification must not have executed the edited file")
	}

	commented := m
	commented.SQL = "-- a comment added after the fact\n" + m.SQL
	if err := d.verifyAppliedMigrations(withBaseline(t, commented), 9001); err != nil {
		t.Fatalf("comment-only edit is not drift: %v", err)
	}

	// m (version 9001) is still genuinely applied and unchanged at this
	// point in the test — include it alongside each new probe so the
	// reverse-direction orphan check (F2) doesn't misread it as having been
	// renumbered away just because this particular call's slice omitted it.
	pending := migration{Version: 9002, Name: "9002_pending.sql", SQL: "CREATE TABLE never_run (n INTEGER);"}
	if err := d.verifyAppliedMigrations(withBaseline(t, m, pending), 9001); err != nil {
		t.Fatalf("a version above the watermark is not checked: %v", err)
	}

	// A file numbered below the watermark with no ledger row is the
	// ut-docs#1056 case 2 shape: it never ran and the watermark would skip
	// it forever. Loud, not silent.
	never := migration{Version: 8999, Name: "8999_slipped_in.sql", SQL: "CREATE TABLE slipped (n INTEGER);"}
	err = d.verifyAppliedMigrations(withBaseline(t, m, never), 9001)
	if err == nil || !strings.Contains(err.Error(), "never recorded as applied") {
		t.Fatalf("unrecorded version below the watermark must fail loudly, got: %v", err)
	}
}

// TestVerifyAppliedMigrations_DetectsUpwardRenumbering (ut-docs#1425 review
// finding F2): the mirror image of the "renumbered under an applied
// version" case above — a ledger row exists for a version whose file has
// since been renumbered to a DIFFERENT, higher version number. A files-only
// loop would miss this entirely: the renumbered file's new version is above
// the watermark and gets applied as if genuinely new, while the orphaned
// ledger row is never inspected.
func TestVerifyAppliedMigrations_DetectsUpwardRenumbering(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "orphan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	m := migration{Version: 9004, Name: "9004_will_be_renumbered.sql", SQL: "CREATE TABLE IF NOT EXISTS orphan_probe (n INTEGER);\n"}
	if err := d.applyMigration(m); err != nil {
		t.Fatal(err)
	}

	// The file that was 9004 is gone from the loaded set entirely (as if
	// renamed to 9005 and 9005 hasn't been "released" yet in this scenario,
	// or simply removed) — current stays 9004 (nothing new applied), but the
	// loaded migration list no longer contains version 9004 at all.
	err = d.verifyAppliedMigrations(withBaseline(t), 9004)
	if err == nil {
		t.Fatal("an orphaned ledger row (no matching on-disk version) must fail boot")
	}
	for _, want := range []string{"migration 9004", "9004_will_be_renumbered.sql", "no on-disk file carries that version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("orphan error %q missing %q", err.Error(), want)
		}
	}
}

// TestVerifyAppliedMigrations_AllowlistedVersionRerunsInPlace: a version in
// idempotentRerunVersions re-applies its current on-disk statements and
// refreshes the ledger row instead of failing.
func TestVerifyAppliedMigrations_AllowlistedVersionRerunsInPlace(t *testing.T) {
	idempotentRerunVersions[9003] = true
	defer delete(idempotentRerunVersions, 9003)

	d, err := Open(filepath.Join(t.TempDir(), "rerun.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	m := migration{Version: 9003, Name: "9003_rerun.sql", SQL: "CREATE TABLE IF NOT EXISTS rerun_probe (n INTEGER);\nINSERT INTO rerun_probe (n) VALUES (1);\n"}
	if err := d.applyMigration(m); err != nil {
		t.Fatal(err)
	}
	edited := m
	edited.Name = "9003_rerun_v2.sql"
	edited.SQL = "CREATE TABLE IF NOT EXISTS rerun_probe (n INTEGER);\nINSERT INTO rerun_probe (n) VALUES (2);\n"

	if err := d.verifyAppliedMigrations(withBaseline(t, edited), 9003); err != nil {
		t.Fatalf("allowlisted drift must re-apply, not fail: %v", err)
	}
	var sum int
	if err := d.QueryRow(`SELECT COALESCE(SUM(n), 0) FROM rerun_probe`).Scan(&sum); err != nil || sum != 3 {
		t.Fatalf("rerun_probe sum = %d err=%v, want 3 (original row 1 + re-applied row 2)", sum, err)
	}
	var name, checksum string
	if err := d.QueryRow(`SELECT name, checksum FROM schema_migrations WHERE version = 9003`).Scan(&name, &checksum); err != nil {
		t.Fatal(err)
	}
	if name != edited.Name || checksum != migrationChecksum(edited.SQL) {
		t.Fatalf("ledger row after re-apply = (%s, %s), want the on-disk file's (%s, %s)", name, checksum, edited.Name, migrationChecksum(edited.SQL))
	}
	// And a second boot sees no drift.
	if err := d.verifyAppliedMigrations(withBaseline(t, edited), 9003); err != nil {
		t.Fatalf("after re-apply the ledger must match: %v", err)
	}
}

// TestFreshBaselineRecordsNameAndChecksum pins the wiring end to end: a
// fresh Open records the real 001_init.sql's name and checksum, and a
// reopen against the unchanged file verifies clean (no false positive).
func TestFreshBaselineRecordsNameAndChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 1 {
		t.Fatalf("expected exactly one embedded migration (the ADR-0074 baseline), got %d", len(migs))
	}
	var name, checksum string
	if err := d.QueryRow(`SELECT name, checksum FROM schema_migrations WHERE version = 1`).Scan(&name, &checksum); err != nil {
		t.Fatal(err)
	}
	if name != "001_init.sql" || checksum != migrationChecksum(migs[0].SQL) {
		t.Fatalf("ledger row = (%s, %s), want (001_init.sql, %s)", name, checksum, migrationChecksum(migs[0].SQL))
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen against the unchanged baseline must not report drift: %v", err)
	}
	d.Close()
}

// TestOpenFailsWhenAppliedBaselineDriftsOnDisk drives the guard through
// Open itself: simulate the on-disk file having changed since it was
// applied by rewriting what the ledger recorded, then reopen.
func TestOpenFailsWhenAppliedBaselineDriftsOnDisk(t *testing.T) {
	for _, tc := range []struct{ name, update string }{
		{"checksum", `UPDATE schema_migrations SET checksum = 'stale' WHERE version = 1`},
		{"name", `UPDATE schema_migrations SET name = '001_before_rename.sql' WHERE version = 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "drift-open.db")
			d, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(tc.update); err != nil {
				t.Fatal(err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = Open(path)
			if err == nil {
				t.Fatal("Open must fail when the ledger and the on-disk file disagree")
			}
			for _, want := range []string{"migration 1:", `"001_init.sql"`, "renamed or edited"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}
