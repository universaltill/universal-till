package db

import (
	"path/filepath"
	"testing"
)

// ut-docs#12: 001_init.sql seeds pos.tax_inclusive, but nothing ever read
// that key — both the save and load paths use store.tax_inclusive. 001 is
// released and append-only, so migration 022 deletes the dead row; fresh
// and upgraded databases end up in the same state.
func TestDeadTaxInclusiveSeedRemoved(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m022.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'pos.tax_inclusive'`).Scan(&n); err != nil {
		t.Fatalf("count pos.tax_inclusive: %v", err)
	}
	if n != 0 {
		t.Fatalf("pos.tax_inclusive still present after migrations (n=%d); migration 022 should have removed the dead seed", n)
	}

	// The neighboring defaults seeded by the same 001 statement must survive.
	for _, key := range []string{"store.name", "store.currency"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, key).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", key, err)
		}
		if n != 1 {
			t.Fatalf("seed %s: got %d rows, want 1 (022 must only delete pos.tax_inclusive)", key, n)
		}
	}
}

// The upgrade path: a released till already has the row (its 001 ran long
// ago) and only 022 runs on the next boot. Simulate by re-inserting the dead
// row and un-recording 022, then reopening — 022 alone must remove it while
// a live store.tax_inclusive value survives.
func TestDeadTaxInclusiveSeedRemovedOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// OR REPLACE so a regressed 022 (row still present) fails on the real
	// assertion below, not on a UNIQUE violation here in setup.
	if _, err := d.DB.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('pos.tax_inclusive', 'false'), ('store.tax_inclusive', 'true')`); err != nil {
		t.Fatalf("seed pre-upgrade state: %v", err)
	}
	// Rewind everything >= 22, not just row 22: migrate() gates on
	// MAX(version), so leaving a later migration's row (e.g. 23+) in place
	// would mask the watermark and skip reapplying 22 entirely.
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 22`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	// Rewinding the ledger doesn't undo migration 024's physical DDL
	// (ALTER TABLE ADD COLUMN isn't idempotent) -- without this, replaying
	// 024 on reopen fails with "duplicate column". Drop it too so the
	// simulated pre-upgrade state is physically accurate (ut-docs#72).
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN service_charge_amount`); err != nil {
		t.Fatalf("rewind service_charge_amount column: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // re-applies only 022
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'pos.tax_inclusive'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pos.tax_inclusive survived the 022 upgrade (n=%d)", n)
	}
	var v string
	if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = 'store.tax_inclusive'`).Scan(&v); err != nil {
		t.Fatalf("store.tax_inclusive should survive 022: %v", err)
	}
	if v != "true" {
		t.Fatalf("store.tax_inclusive = %q after upgrade, want %q untouched", v, "true")
	}
}
