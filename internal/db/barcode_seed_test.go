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
