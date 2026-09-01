package db

import (
	"path/filepath"
	"testing"
)

// TestMigration071BackfillsEqualPairTakeawayRate simulates a till carrying a
// tax code hand-created BEFORE ut-docs#1013's fix to parseTaxCodeForm — an
// explicit takeaway_rate_basis_points equal to its own rate_basis_points
// (e.g. 7%/7% for food, typed literally into both fields) — and confirms
// migration 071 canonicalizes it to NULL on upgrade, matching every code
// created after the fix and the CSV-import path's own long-standing rule
// (ut-docs#536). See internal/db/migrations/071_backfill_equal_pair_takeaway_rate.sql.
func TestMigration071BackfillsEqualPairTakeawayRate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m071-upgrade.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// The legacy equal-pair row this migration exists to fix.
	if _, err := d.DB.Exec(`INSERT INTO tax_codes (id, name, rate_basis_points, takeaway_rate_basis_points, is_active)
		VALUES ('legacy-equal', 'Speisen 7%', 700, 700, 1)`); err != nil {
		t.Fatalf("seed legacy equal-pair tax code: %v", err)
	}
	// A genuinely different pair must survive untouched.
	if _, err := d.DB.Exec(`INSERT INTO tax_codes (id, name, rate_basis_points, takeaway_rate_basis_points, is_active)
		VALUES ('genuine-override', 'Milk Drinks', 1900, 700, 1)`); err != nil {
		t.Fatalf("seed genuine-override tax code: %v", err)
	}
	// A code with no takeaway rate at all (already NULL) must survive
	// untouched -- this is the common case, not the exception.
	if _, err := d.DB.Exec(`INSERT INTO tax_codes (id, name, rate_basis_points, takeaway_rate_basis_points, is_active)
		VALUES ('no-takeaway', 'Standard Rate', 1900, NULL, 1)`); err != nil {
		t.Fatalf("seed no-takeaway tax code: %v", err)
	}

	// Rewind so 071 replays on reopen. 071 is pure DML (no ALTER TABLE), but
	// 072 (payments voucher_id, ut-docs#1053) now sits after it and replays
	// too — undo its non-idempotent DDL first, same as the other rewind
	// tests' helpers.
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 71`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	rewindPaymentsVoucherID072(t, d)
	rewindCountryDefaultLocale073(t, d)
	rewindSaleLineOrderType078(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // replays 071 against the simulated pre-fix till
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var legacyTakeaway *int64
	if err := d.DB.QueryRow(`SELECT takeaway_rate_basis_points FROM tax_codes WHERE id = 'legacy-equal'`).Scan(&legacyTakeaway); err != nil {
		t.Fatal(err)
	}
	if legacyTakeaway != nil {
		t.Errorf("legacy equal-pair code's takeaway_rate_basis_points = %v, want NULL after backfill", *legacyTakeaway)
	}

	var genuineTakeaway *int64
	if err := d.DB.QueryRow(`SELECT takeaway_rate_basis_points FROM tax_codes WHERE id = 'genuine-override'`).Scan(&genuineTakeaway); err != nil {
		t.Fatal(err)
	}
	if genuineTakeaway == nil || *genuineTakeaway != 700 {
		t.Errorf("genuine-override code's takeaway_rate_basis_points = %v, want 700 unchanged", genuineTakeaway)
	}

	var noTakeaway *int64
	if err := d.DB.QueryRow(`SELECT takeaway_rate_basis_points FROM tax_codes WHERE id = 'no-takeaway'`).Scan(&noTakeaway); err != nil {
		t.Fatal(err)
	}
	if noTakeaway != nil {
		t.Errorf("no-takeaway code's takeaway_rate_basis_points = %v, want still NULL", *noTakeaway)
	}
}

// TestMigration071IsIdempotentOnCleanData confirms 071 is a genuine no-op
// the SECOND time it runs against a till already clean of equal pairs --
// the common case for every till upgrading past 071 already having gone
// through it once, and every till created after ut-docs#1013's write-path
// fix, which never produces an equal pair to begin with.
func TestMigration071IsIdempotentOnCleanData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m071-clean.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.DB.Exec(`INSERT INTO tax_codes (id, name, rate_basis_points, takeaway_rate_basis_points, is_active)
		VALUES ('c1', 'Milk Drinks', 1900, 700, 1)`); err != nil {
		t.Fatalf("seed tax code: %v", err)
	}

	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 71`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	// 072's DDL replays with it — undo it first (see the sibling test above).
	rewindPaymentsVoucherID072(t, d)
	rewindCountryDefaultLocale073(t, d)
	rewindSaleLineOrderType078(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path) // re-applies 071 a second time
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var takeaway *int64
	if err := d.DB.QueryRow(`SELECT takeaway_rate_basis_points FROM tax_codes WHERE id = 'c1'`).Scan(&takeaway); err != nil {
		t.Fatal(err)
	}
	if takeaway == nil || *takeaway != 700 {
		t.Errorf("takeaway_rate_basis_points after re-applying 071 = %v, want unchanged 700", takeaway)
	}
}
