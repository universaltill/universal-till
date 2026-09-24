package data

import (
	"context"
	"database/sql"
	"testing"
)

// ut-docs#167: the demo catalogue carries café items (a latte and a
// sandwich) whose VAT differs by order type — dine-in at the standard rate,
// takeaway at a reduced one. Core has no per-item order-type rate of its
// own; the dine-in/takeaway pair lives on the tax code
// (takeaway_rate_basis_points, the same model a catalog import uses —
// ut-docs#512), which the German tax plugin's activation reconcile turns
// into an active takeaway override (ut-docs#1370). So the seed carries a
// dedicated demo tax code with that pair, and the two café items use it.

const demoCafeTaxCodeID = "tax_demo_cafe"

func TestSeedDemoCatalogueCafeItemsCarryDineInTakeawayTaxPair(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if err := NewDemoSeedRepo(d.DB).SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue: %v", err)
	}

	var rate int
	var takeaway sql.NullInt64
	if err := d.DB.QueryRow(`SELECT rate_basis_points, takeaway_rate_basis_points FROM tax_codes WHERE id = ?`,
		demoCafeTaxCodeID).Scan(&rate, &takeaway); err != nil {
		t.Fatalf("demo café tax code missing: %v", err)
	}
	if !takeaway.Valid || int(takeaway.Int64) == rate {
		t.Fatalf("demo café tax code = dine-in %d, takeaway %v; want a takeaway rate that differs from dine-in", rate, takeaway)
	}
	if rate != 2000 || takeaway.Int64 != 500 {
		t.Fatalf("demo café tax code = %d/%d bp, want 2000/500 (the till's generic standard/reduced rates)", rate, takeaway.Int64)
	}

	for _, id := range []string{"itm051", "itm052"} {
		var taxCode string
		var sample, untracked int
		if err := d.DB.QueryRow(`SELECT tax_code_id, is_sample_data, stock_untracked FROM items WHERE id = ?`, id).Scan(&taxCode, &sample, &untracked); err != nil {
			t.Fatalf("café item %s missing: %v", id, err)
		}
		if taxCode != demoCafeTaxCodeID || sample != 1 {
			t.Errorf("café item %s = tax code %q, sample %d; want %q, 1", id, taxCode, sample, demoCafeTaxCodeID)
		}
		// Made to order with no stock rows: untracked, or every sale of it
		// is refused "not enough stock".
		if untracked != 1 {
			t.Errorf("café item %s stock_untracked = %d, want 1", id, untracked)
		}
	}
}

// tax_codes.name is UNIQUE: an operator who already has a tax code with the
// demo code's name makes its INSERT OR IGNORE a no-op. The café items must
// then still seed (on the standard rate) — a dangling tax_code_id would
// FK-fail and roll back the whole demo catalogue.
func TestSeedDemoCatalogueCafeTaxCodeNameClashFallsBackToStandard(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO tax_codes (id, name, rate_basis_points) VALUES ('own_tax', 'Café dine-in 20% / takeaway 5%', 1000)`); err != nil {
		t.Fatal(err)
	}
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue with a clashing tax-code name: %v", err)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 52 {
		t.Fatalf("SampleItemCount = %d, %v; want 52, nil", n, err)
	}
	var taxCode string
	if err := d.DB.QueryRow(`SELECT tax_code_id FROM items WHERE id = 'itm051'`).Scan(&taxCode); err != nil {
		t.Fatal(err)
	}
	if taxCode != "tax_std" {
		t.Fatalf("itm051 tax code = %q, want tax_std fallback", taxCode)
	}
	var name string
	if err := d.DB.QueryRow(`SELECT name FROM tax_codes WHERE id = 'own_tax'`).Scan(&name); err != nil {
		t.Fatalf("operator's own tax code touched: %v", err)
	}
}

func demoCafeTaxCodeExists(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tax_codes WHERE id = ?`, demoCafeTaxCodeID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// "Remove sample data" takes the demo tax code with it once nothing uses it.
func TestRemoveDemoCatalogueRemovesUnusedDemoTaxCode(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RemoveDemoCatalogue(ctx); err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if demoCafeTaxCodeExists(t, d.DB) {
		t.Fatal("demo café tax code survived removal with no item left on it")
	}
	// The structural codes are never touched.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM tax_codes WHERE id IN ('tax_std','tax_red','tax_zero')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("structural tax codes after removal = %d, want 3", n)
	}
}

// Kept while any item still uses it: an operator's own item, or a demo item
// that removal had to keep (sold). Deleting it would FK-fail the removal.
func TestRemoveDemoCatalogueKeepsDemoTaxCodeInUse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup string
	}{
		{"operator item", `INSERT INTO items (id, name, base_price, tax_code_id) VALUES ('own-1', 'My Flat White', 300, 'tax_demo_cafe')`},
		{"kept demo item", `INSERT INTO stock_movements (id, item_id, variant_id, location_id, type, quantity) VALUES ('sm-1', 'itm051', NULL, 'loc_main', 'adjust', 3)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openDemoSeedTestDB(t)
			ctx := context.Background()
			repo := NewDemoSeedRepo(d.DB)
			if err := repo.SeedDemoCatalogue(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB.Exec(tc.setup); err != nil {
				t.Fatal(err)
			}
			if _, _, err := repo.RemoveDemoCatalogue(ctx); err != nil {
				t.Fatalf("RemoveDemoCatalogue: %v", err)
			}
			if !demoCafeTaxCodeExists(t, d.DB) {
				t.Fatal("demo café tax code removed while an item still uses it")
			}
		})
	}
}

// An operator who edited the demo tax code (renamed it, changed a rate) has
// made it theirs — same pristine rule as a demo item (ut-docs#566).
func TestRemoveDemoCatalogueKeepsEditedDemoTaxCode(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE tax_codes SET takeaway_rate_basis_points = 700 WHERE id = 'tax_demo_cafe'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RemoveDemoCatalogue(ctx); err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if !demoCafeTaxCodeExists(t, d.DB) {
		t.Fatal("edited demo café tax code was removed")
	}
}

// An operator's own item already on a café SKU makes that demo item's
// INSERT OR IGNORE a no-op; its price-history row must then be skipped too,
// not FK-fail (and roll back) the whole catalogue.
func TestSeedDemoCatalogueCafeSKUClashSkipsOnlyThatItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price) VALUES ('own-latte', 'SKU-0051', 'Our Latte', 300)`); err != nil {
		t.Fatal(err)
	}
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue with a clashing café SKU: %v", err)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 51 {
		t.Fatalf("SampleItemCount = %d, %v; want 51 (every demo item but the clashing latte)", n, err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM price_history WHERE item_id IN ('itm051', 'itm052')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("café price_history rows = %d, want 1 (the sandwich only)", n)
	}
}
