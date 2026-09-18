package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
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
//
// This no longer asserts "exactly one embedded migration" — that was only
// ever true in the narrow window right after the ADR-0074 squash, before
// any new migration legitimately landed on top of the baseline (the first
// being 002_refund_of_line_id.sql, ut-docs#1560). What this test actually
// pins is narrower and still holds regardless of how many migrations exist
// today: migs[0] (loadMigrations sorts by version) is always version 1,
// 001_init.sql, the baseline itself.
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
	if len(migs) < 1 || migs[0].Version != 1 || migs[0].Name != "001_init.sql" {
		t.Fatalf("expected migs[0] to be the version-1 ADR-0074 baseline (001_init.sql), got %+v", migs)
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

// v0180BaselineChecksum is what every till that installed v0.1.0…v0.18.0
// recorded for version 1, and what the restored 001_init.sql yields again
// (ut-docs#2395). v0192BaselineChecksum is the accidentally-shipped variant
// that v0.19.0–v0.19.2 fresh installs recorded (001 edited by ut-docs#2312).
// Both are literals on purpose — computing them from the file would make
// the population tests below tautological.
const (
	v0180BaselineChecksum = "ee6f0a910e4259cea503aeddb845c169e82e0d34182ef637f191c9ae1ae71b21"
	v0192BaselineChecksum = "13898ca67f47c37411eb3b76a8265c1bc30a465e631fe006fcc806b0bc794d42"
)

// restampWarnings returns the recent warn lines emitted by
// verifyAppliedMigrations' acceptedPriorChecksums path for version 1.
func restampWarnings() []logging.Problem {
	var out []logging.Problem
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "migration 1:") && strings.Contains(p.Msg, "re-stamp") {
			out = append(out, p)
		}
	}
	return out
}

// TestOpenUpgradesV018TillViaMigration033 is population A of ut-docs#2395:
// a till that applied 001 as shipped in v0.1.0…v0.18.0 (ledger checksum
// v0180BaselineChecksum, no catalog_management rows, watermark 32) boots
// on the fixed tree with no drift error and gets the permission from 033.
// The first assertion is the one the whole hotfix hangs on: the restored
// 001_init.sql must record EXACTLY the checksum those tills already hold.
func TestOpenUpgradesV018TillViaMigration033(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pop-a.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err := d.QueryRow(`SELECT checksum FROM schema_migrations WHERE version = 1`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != v0180BaselineChecksum {
		t.Fatalf("restored 001_init.sql records checksum %s, want the v0.18.0 value %s that every upgrading till's ledger holds (ut-docs#2395)", checksum, v0180BaselineChecksum)
	}
	// Rewind to the v0.18.0 shape: 033 never ran, its rows do not exist.
	for _, q := range []string{
		`DELETE FROM schema_migrations WHERE version = 33`,
		`DELETE FROM role_permissions WHERE action = 'catalog_management'`,
		`DELETE FROM permission_actions WHERE action = 'catalog_management'`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if actions, grants := catalogManagementCounts(t, d); actions != 0 || grants != 0 {
		t.Fatalf("rewind left %d/%d catalog_management rows, want 0/0", actions, grants)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	logging.ResetRecent()
	d, err = Open(path)
	if err != nil {
		t.Fatalf("a v0.18.0 till must boot on the fixed tree without a drift error: %v", err)
	}
	defer d.Close()
	if actions, grants := catalogManagementCounts(t, d); actions != 1 || grants != 3 {
		t.Fatalf("after upgrade: catalog_management rows = %d action / %d grants, want 1 / 3 (from 033)", actions, grants)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 33`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("033 ledger row count = %d err=%v, want 1", n, err)
	}
	if w := restampWarnings(); len(w) != 0 {
		t.Fatalf("population A must not go through the re-stamp path, got %v", w)
	}
}

// TestOpenAcceptsV019BaselineChecksumAndRestamps is population B of
// ut-docs#2395: a fresh v0.19.0–v0.19.2 install recorded the edited 001
// (v0192BaselineChecksum) and already has the catalog_management rows;
// its watermark is 32. On the fixed tree it must boot, warn once, get its
// version-1 ledger row re-stamped to the restored file's checksum WITHOUT
// re-running 001, apply 033 as a no-op, and end up with exactly 1/3 rows.
// The next boot must then be silent — the checksum matches.
func TestOpenAcceptsV019BaselineChecksumAndRestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pop-b.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewind to the v0.19.x shape (watermark 31 on v0.19.0, 32 on v0.19.1+;
	// either way below 33): 033 never ran, but its rows already
	// exist (from the edited 001), and the ledger holds the edited checksum.
	for _, q := range []string{
		`DELETE FROM schema_migrations WHERE version = 33`,
		`UPDATE schema_migrations SET checksum = '` + v0192BaselineChecksum + `' WHERE version = 1`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if actions, grants := catalogManagementCounts(t, d); actions != 1 || grants != 3 {
		t.Fatalf("v0.19.2 shape must already carry the rows, got %d/%d", actions, grants)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	logging.ResetRecent()
	d, err = Open(path)
	if err != nil {
		t.Fatalf("a v0.19.0–v0.19.2 install must boot on the fixed tree: %v", err)
	}
	var name, checksum string
	if err := d.QueryRow(`SELECT name, checksum FROM schema_migrations WHERE version = 1`).Scan(&name, &checksum); err != nil {
		t.Fatal(err)
	}
	if name != "001_init.sql" || checksum != v0180BaselineChecksum {
		t.Fatalf("ledger row for version 1 after accepting boot = (%s, %s), want (001_init.sql, %s) — the row must be re-stamped to the restored file", name, checksum, v0180BaselineChecksum)
	}
	if actions, grants := catalogManagementCounts(t, d); actions != 1 || grants != 3 {
		t.Fatalf("after accepting boot: catalog_management rows = %d/%d, want 1/3 (033 is a no-op here)", actions, grants)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 33`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("033 ledger row count = %d err=%v, want 1", n, err)
	}
	w := restampWarnings()
	if len(w) != 1 {
		t.Fatalf("accepting boot must warn exactly once about re-stamping version 1, got %d: %v", len(w), w)
	}
	for _, want := range []string{"migration 1:", v0192BaselineChecksum, "ut-docs#2395", "re-stamp"} {
		if !strings.Contains(w[0].Msg, want) {
			t.Errorf("re-stamp warning %q missing %q", w[0].Msg, want)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Second boot: nothing to accept, nothing to say.
	logging.ResetRecent()
	d, err = Open(path)
	if err != nil {
		t.Fatalf("second boot after re-stamp must be clean: %v", err)
	}
	defer d.Close()
	if w := restampWarnings(); len(w) != 0 {
		t.Fatalf("second boot must be silent, got %v", w)
	}
	if err := d.QueryRow(`SELECT checksum FROM schema_migrations WHERE version = 1`).Scan(&checksum); err != nil || checksum != v0180BaselineChecksum {
		t.Fatalf("checksum after second boot = %s err=%v, want %s", checksum, err, v0180BaselineChecksum)
	}
}

// TestOpenStillRejectsUnacceptedBaselineChecksum: acceptedPriorChecksums
// is a record of ONE shipped accident, not a bypass. A version-1 ledger
// checksum that is neither the current file's nor the listed v0.19.x
// variant still fails boot with the existing drift error, and the ledger
// row is left exactly as it was.
func TestOpenStillRejectsUnacceptedBaselineChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A well-formed sha256 hex that no release ever wrote.
	foreign := "0000000000000000000000000000000000000000000000000000000000002395"
	if _, err := d.Exec(`UPDATE schema_migrations SET checksum = ? WHERE version = 1`, foreign); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	if err == nil {
		t.Fatal("a version-1 checksum outside acceptedPriorChecksums must still fail boot")
	}
	for _, want := range []string{"migration 1:", `"001_init.sql"`, foreign, "renamed or edited"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var checksum string
	if err := ro.QueryRow(`SELECT checksum FROM schema_migrations WHERE version = 1`).Scan(&checksum); err != nil || checksum != foreign {
		t.Fatalf("a rejected boot must not touch the ledger: checksum = %s err=%v, want %s", checksum, err, foreign)
	}
}
