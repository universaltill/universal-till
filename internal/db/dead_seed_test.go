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
	// Rewinding the ledger doesn't undo migration 024's, 025's, 026's or
	// 027's physical DDL (ALTER TABLE ADD COLUMN / CREATE TABLE aren't
	// idempotent) -- without this, replaying them on reopen fails with
	// "duplicate column"/"already exists". Drop all five so the
	// simulated pre-upgrade state is physically accurate (ut-docs#72).
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN service_charge_amount`); err != nil {
		t.Fatalf("rewind service_charge_amount column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE categories DROP COLUMN color`); err != nil {
		t.Fatalf("rewind categories.color column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN order_type`); err != nil {
		t.Fatalf("rewind order_type column: %v", err)
	}
	// Migration 027 creates a table, not a column -- same non-idempotent-
	// replay problem, drop it too (ut-docs#184).
	if _, err := d.DB.Exec(`DROP TABLE pending_pairings`); err != nil {
		t.Fatalf("rewind pending_pairings table: %v", err)
	}
	// Same for migration 028's lead_time_days column (universaltill/ut-docs#85).
	if _, err := d.DB.Exec(`ALTER TABLE items DROP COLUMN lead_time_days`); err != nil {
		t.Fatalf("rewind lead_time_days column: %v", err)
	}
	// Migration 029 adds another column -- same problem again (ut-docs#49).
	if _, err := d.DB.Exec(`ALTER TABLE stock_locations DROP COLUMN is_active`); err != nil {
		t.Fatalf("rewind stock_locations.is_active column: %v", err)
	}
	// Migration 032 creates a table -- same non-idempotent-replay problem as
	// 027, drop it too (ut-docs#348).
	if _, err := d.DB.Exec(`DROP TABLE issue_reports_sent`); err != nil {
		t.Fatalf("rewind issue_reports_sent table: %v", err)
	}
	// Migration 033 adds two sales columns and a table -- same problem again
	// (ut-docs#526).
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN order_status`); err != nil {
		t.Fatalf("rewind sales.order_status column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN order_status_updated_at`); err != nil {
		t.Fatalf("rewind sales.order_status_updated_at column: %v", err)
	}
	if _, err := d.DB.Exec(`DROP TABLE order_status_events`); err != nil {
		t.Fatalf("rewind order_status_events table: %v", err)
	}
	// Migration 034 creates three tables -- same non-idempotent-replay
	// problem, drop them too (ut-docs#516).
	for _, tbl := range []string{"item_station_routes", "category_station_routes", "kitchen_stations"} {
		if _, err := d.DB.Exec(`DROP TABLE ` + tbl); err != nil {
			t.Fatalf("rewind %s table: %v", tbl, err)
		}
	}
	// Migration 035 adds two more sales columns -- same problem again
	// (ut-docs#517).
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN kitchen_print_failed_at`); err != nil {
		t.Fatalf("rewind sales.kitchen_print_failed_at column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN receipt_print_failed_at`); err != nil {
		t.Fatalf("rewind sales.receipt_print_failed_at column: %v", err)
	}
	// Migration 036 adds items.is_sample_data -- same problem again
	// (ut-docs#539).
	if _, err := d.DB.Exec(`ALTER TABLE items DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind items.is_sample_data column: %v", err)
	}
	// Migration 037 adds report_archive.cloud_acked_at -- same problem
	// again (ut-docs#571).
	if _, err := d.DB.Exec(`ALTER TABLE report_archive DROP COLUMN cloud_acked_at`); err != nil {
		t.Fatalf("rewind report_archive.cloud_acked_at column: %v", err)
	}
	// Migration 038 adds customers/promotions.is_sample_data -- same
	// non-idempotent replay problem (ut-docs#567).
	if _, err := d.DB.Exec(`ALTER TABLE customers DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind customers.is_sample_data column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE promotions DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind promotions.is_sample_data column: %v", err)
	}
	// Migration 049 adds audit_log.blocked_actor_id -- same non-idempotent
	// replay problem (ut-docs#557).
	if _, err := d.DB.Exec(`ALTER TABLE audit_log DROP COLUMN blocked_actor_id`); err != nil {
		t.Fatalf("rewind audit_log.blocked_actor_id column: %v", err)
	}
	// Migrations 050/051 add payments/payments_archive's card-present
	// columns -- same non-idempotent replay problem (ut-docs#543).
	for _, col := range []string{"masked_pan", "auth_code", "terminal_id", "trace_id"} {
		if _, err := d.DB.Exec(`ALTER TABLE payments DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments.%s column: %v", col, err)
		}
		if _, err := d.DB.Exec(`ALTER TABLE payments_archive DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewind payments_archive.%s column: %v", col, err)
		}
	}
	// Migration 054 adds the `tables` table and held_sales.table_id -- same
	// non-idempotent replay problem (ut-docs#814).
	rewindTables054(t, d)
	rewindTracking058(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // re-applies 022 and its followers (027-049)
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

// rewindTables054 undoes migration 054's non-idempotent DDL (the table
// floor plan's `tables` table plus held_sales.table_id, ut-docs#814) — and,
// riding along, 055's (held_sales_archive.table_id, added 2026-08-19 to keep
// the archive twin column-identical to held_sales per 040's own invariant)
// and 056's (sales.table_id/sales_archive.table_id, ut-docs#820, same
// column-identical-archive invariant) — so the upgrade tests in this
// package — which rewind schema_migrations below 54 and reopen — can
// replay all three cleanly. 055/056 have no independent rewind path: they
// only exist because 054 does, so any test rewinding past 054 must rewind
// 055 and 056 too, or the replay hits their own ADD COLUMN a second time.
// Order matters: SQLite refuses to drop a column an index still
// references, so both indexes go first; every `table_id` column is dropped
// before `tables` itself, since all of them reference it.
func rewindTables054(t *testing.T, d *DB) {
	t.Helper()
	for _, q := range []string{
		`DROP INDEX idx_held_sales_table`,
		`DROP INDEX idx_sales_table`,
		`ALTER TABLE held_sales DROP COLUMN table_id`,
		`ALTER TABLE held_sales_archive DROP COLUMN table_id`,
		`ALTER TABLE sales DROP COLUMN table_id`,
		`ALTER TABLE sales_archive DROP COLUMN table_id`,
		`DROP TABLE tables`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("rewind 054 (%s): %v", q, err)
		}
	}
}

// rewindTracking058 undoes migration 058's non-idempotent DDL
// (sales.tracking_token + its partial unique index + the sales_archive
// mirror, ut-docs#527) — same replay problem as rewindTables054 above; the
// index must go first, since SQLite refuses to drop a column an index still
// references.
func rewindTracking058(t *testing.T, d *DB) {
	t.Helper()
	for _, q := range []string{
		`DROP INDEX idx_sales_tracking_token`,
		`ALTER TABLE sales DROP COLUMN tracking_token`,
		`ALTER TABLE sales_archive DROP COLUMN tracking_token`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("rewind 058 (%s): %v", q, err)
		}
	}
}
