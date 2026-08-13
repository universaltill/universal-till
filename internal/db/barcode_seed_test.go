package db

import (
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data/seeddata"
)

// seedDemoCatalogue restores the opt-in demo catalogue into a freshly
// opened DB. Since migration 036 (ut-docs#539) a fresh install has NO demo
// rows — these barcode-checksum guards now validate the seeddata asset the
// opt-in path inserts, not a boot-time seed.
func seedDemoCatalogue(t *testing.T, d *DB) {
	t.Helper()
	if _, err := d.DB.Exec(seeddata.DemoCatalogueSQL); err != nil {
		t.Fatalf("seed demo catalogue: %v", err)
	}
}

// ut-docs#17: the seeded item_barcodes rows carried no valid EAN-13 check
// digit, so a printed scanner-test sheet had to be rendered as Code 128 (an
// EAN-mode scanner refuses them). This guards against the demo catalog
// regressing back to non-checksummed barcodes.
func TestSeedItemBarcodesValidEAN13(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "barcodes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedDemoCatalogue(t, d)

	rows, err := d.DB.Query(`SELECT barcode FROM item_barcodes`)
	if err != nil {
		t.Fatalf("query item_barcodes: %v", err)
	}
	defer rows.Close()

	var barcodes []string
	for rows.Next() {
		var barcode string
		if err := rows.Scan(&barcode); err != nil {
			t.Fatalf("scan barcode: %v", err)
		}
		barcodes = append(barcodes, barcode)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(barcodes) != 50 {
		t.Fatalf("got %d seeded item_barcodes, want 50", len(barcodes))
	}

	var invalid []string
	for _, barcode := range barcodes {
		if !isValidEAN13(barcode) {
			invalid = append(invalid, barcode)
		}
	}
	if len(invalid) > 0 {
		t.Fatalf("seeded item_barcodes with invalid EAN-13 check digit: %v", invalid)
	}
}

// ut-docs#17 follow-up (independent review): variant_barcodes also seeds
// barcode_type='EAN13' rows and carried the same fabricated check digit.
func TestSeedVariantBarcodesValidEAN13(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "variant-barcodes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedDemoCatalogue(t, d)

	rows, err := d.DB.Query(`SELECT barcode FROM variant_barcodes`)
	if err != nil {
		t.Fatalf("query variant_barcodes: %v", err)
	}
	defer rows.Close()

	var barcodes []string
	for rows.Next() {
		var barcode string
		if err := rows.Scan(&barcode); err != nil {
			t.Fatalf("scan barcode: %v", err)
		}
		barcodes = append(barcodes, barcode)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(barcodes) != 12 {
		t.Fatalf("got %d seeded variant_barcodes, want 12", len(barcodes))
	}

	var invalid []string
	for _, barcode := range barcodes {
		if !isValidEAN13(barcode) {
			invalid = append(invalid, barcode)
		}
	}
	if len(invalid) > 0 {
		t.Fatalf("seeded variant_barcodes with invalid EAN-13 check digit: %v", invalid)
	}
}

// ut-docs#191 (split from #17's independent review): shortcut_buttons also
// seeds a barcode column and carried the same fabricated check digit.
func TestSeedShortcutButtonsValidEAN13(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "shortcut-buttons.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedDemoCatalogue(t, d)

	rows, err := d.DB.Query(`SELECT barcode FROM shortcut_buttons`)
	if err != nil {
		t.Fatalf("query shortcut_buttons: %v", err)
	}
	defer rows.Close()

	var barcodes []string
	for rows.Next() {
		var barcode string
		if err := rows.Scan(&barcode); err != nil {
			t.Fatalf("scan barcode: %v", err)
		}
		barcodes = append(barcodes, barcode)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(barcodes) != 10 {
		t.Fatalf("got %d seeded shortcut_buttons, want 10", len(barcodes))
	}

	var invalid []string
	for _, barcode := range barcodes {
		if !isValidEAN13(barcode) {
			invalid = append(invalid, barcode)
		}
	}
	if len(invalid) > 0 {
		t.Fatalf("seeded shortcut_buttons with invalid EAN-13 check digit: %v", invalid)
	}
}

// The upgrade path: a till that installed before migration 023 existed
// already has the broken checksums from 001's seed. Simulate by rewinding
// schema_migrations past 023 and restoring one broken barcode, then
// reopening — 023 alone must correct it on the next boot.
func TestSeedBarcodeChecksumsFixedOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "barcode-upgrade.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-023 till still has the demo catalogue from its own 001 run (036
	// removes it only when untouched) — restore it, and give itm001 real
	// trading history so 036's replay below must keep it (ut-docs#539).
	seedDemoCatalogue(t, d)
	if _, err := d.DB.Exec(`INSERT INTO stock_movements (id, item_id, variant_id, location_id, type, quantity)
		VALUES ('sm-023', 'itm001', NULL, 'loc_main', 'adjust', 1)`); err != nil {
		t.Fatalf("touch itm001: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE item_barcodes SET barcode = '5000000000011' WHERE barcode = '5000000000012'`); err != nil {
		t.Fatalf("simulate pre-023 broken checksum: %v", err)
	}
	// >= 23, not = 23: migrate() gates on MAX(version) (see db.go), so
	// leaving a later migration's row in place would mask the watermark
	// and skip reapplying 023 — the same trap this commit just fixed in
	// dead_seed_test.go.
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 23`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	// Rewinding the ledger doesn't undo migration 024's, 025's, 026's or 027's
	// physical DDL (ALTER TABLE ADD COLUMN isn't idempotent, unlike 023's
	// UPDATE) -- without this, replaying them on reopen fails with
	// "duplicate column"/"already exists". Drop all five so the simulated pre-023 state is
	// physically accurate, same as a real till that never had them
	// (ut-docs#72).
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
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // re-applies 023 and its followers (027-037)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var barcode string
	if err := d.DB.QueryRow(`SELECT barcode FROM item_barcodes WHERE item_id = 'itm001'`).Scan(&barcode); err != nil {
		t.Fatal(err)
	}
	if barcode != "5000000000012" {
		t.Fatalf("itm001 barcode = %q after 023 upgrade, want %q", barcode, "5000000000012")
	}
}

// The upgrade path for #191's fix: a till that installed before migration
// 031 existed already has the broken shortcut_buttons checksum from 001's
// seed. Simulate by rewinding schema_migrations past 031 and restoring one
// broken barcode, then reopening — 031 alone must correct it on the next
// boot. Unlike 023's followers, 031 is a pure UPDATE (no DDL), so no column/
// table rewind is needed alongside it.
func TestSeedShortcutButtonChecksumFixedOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shortcut-button-upgrade.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same as the 023 upgrade test above: restore the catalogue a pre-031
	// till would have, and touch itm001 so 036's replay keeps it (its
	// shortcut_buttons row rides on the item via ON DELETE CASCADE).
	seedDemoCatalogue(t, d)
	if _, err := d.DB.Exec(`INSERT INTO stock_movements (id, item_id, variant_id, location_id, type, quantity)
		VALUES ('sm-031', 'itm001', NULL, 'loc_main', 'adjust', 1)`); err != nil {
		t.Fatalf("touch itm001: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE shortcut_buttons SET barcode = '2000010000017' WHERE barcode = '2000010000012'`); err != nil {
		t.Fatalf("simulate pre-031 broken checksum: %v", err)
	}
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 31`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	// 031 itself is a pure UPDATE (no DDL), but rewinding past it also
	// replays 032, whose CREATE TABLE isn't idempotent — drop the table so
	// the replay works, same as the pre-023 rewind above (ut-docs#348).
	if _, err := d.DB.Exec(`DROP TABLE issue_reports_sent`); err != nil {
		t.Fatalf("rewind issue_reports_sent table: %v", err)
	}
	// Same for 033's sales columns + order_status_events table (ut-docs#526).
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
	// Same for 035's print-failure columns (ut-docs#517).
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN kitchen_print_failed_at`); err != nil {
		t.Fatalf("rewind sales.kitchen_print_failed_at column: %v", err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE sales DROP COLUMN receipt_print_failed_at`); err != nil {
		t.Fatalf("rewind sales.receipt_print_failed_at column: %v", err)
	}
	// Same for 036's items.is_sample_data column (ut-docs#539).
	if _, err := d.DB.Exec(`ALTER TABLE items DROP COLUMN is_sample_data`); err != nil {
		t.Fatalf("rewind items.is_sample_data column: %v", err)
	}
	// Same for 037's report_archive.cloud_acked_at column (ut-docs#571).
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
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // re-applies 031 through 037
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var barcode string
	if err := d.DB.QueryRow(`SELECT barcode FROM shortcut_buttons WHERE item_id = 'itm001'`).Scan(&barcode); err != nil {
		t.Fatal(err)
	}
	if barcode != "2000010000012" {
		t.Fatalf("itm001 shortcut barcode = %q after 031 upgrade, want %q", barcode, "2000010000012")
	}
}

// isValidEAN13 reports whether barcode is 13 digits and its final digit is
// the correct mod-10 weighted check digit (odd positions from the left, 1-
// indexed, weight 1; even positions weight 3).
func isValidEAN13(barcode string) bool {
	if len(barcode) != 13 {
		return false
	}
	digits := make([]int, 13)
	for i, r := range barcode {
		if r < '0' || r > '9' {
			return false
		}
		digits[i] = int(r - '0')
	}

	sum := 0
	for i := 0; i < 12; i++ {
		weight := 1
		if i%2 == 1 {
			weight = 3
		}
		sum += digits[i] * weight
	}
	check := (10 - (sum % 10)) % 10

	return check == digits[12]
}
