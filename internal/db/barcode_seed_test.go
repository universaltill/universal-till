package db

import (
	"path/filepath"
	"testing"

	barcodepkg "github.com/universaltill/universal-till/internal/barcode"
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

// isValidEAN13 reports whether barcode is 13 digits with a valid check
// digit. Delegates to the shared internal/barcode checksum (ADR-0059
// Decision §1 — "reuse/extract ... rather than duplicating it") instead of
// keeping its own third copy of the algorithm (ut-docs#933 review finding
// F5 — this file and internal/data/catalog_repo.go had each grown one).
func isValidEAN13(barcode string) bool {
	return barcodepkg.ValidEAN13Checksum(barcode)
}
