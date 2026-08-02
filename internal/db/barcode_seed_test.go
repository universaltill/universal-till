package db

import (
	"path/filepath"
	"testing"
)

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
	// "duplicate column"/"already exists". Drop all four so the simulated pre-023 state is
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
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // re-applies only 023
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
