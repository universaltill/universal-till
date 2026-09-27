package data

import (
	"context"
	"database/sql"
	"testing"
)

// ut-docs#2697: brands.name is UNIQUE. If an operator already has a brand
// with the same name as a demo brand (e.g. "Coca-Cola"), that demo brand's
// INSERT OR IGNORE is a no-op — but every demo item referencing it still
// names the (now-absent) demo brand id, so a plain INSERT among them
// FK-fails and rolls back the whole transaction. brand_id falls back to
// NULL for the affected item(s) only (brand is optional metadata; sample
// data never reuses the operator's own same-named brand row) — every other
// item, including ones on other brands, still seeds normally.

// An operator's own brand already named "Coca-Cola" makes br_coca's INSERT
// OR IGNORE a no-op. itm001 (the only item on br_coca) must still land, with
// brand_id NULL instead of FK-failing the whole catalogue; every other item
// keeps its own brand, and the operator's brand is untouched.
func TestSeedDemoCatalogueBrandNameClashFallsBackToNullBrand(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO brands (id, name) VALUES ('my_coke', 'Coca-Cola')`); err != nil {
		t.Fatal(err)
	}
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue with a clashing brand name: %v", err)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 52 {
		t.Fatalf("SampleItemCount = %d, %v; want 52 (every demo item lands — only the brand clashed)", n, err)
	}

	var brandID sql.NullString
	if err := d.DB.QueryRow(`SELECT brand_id FROM items WHERE id = 'itm001'`).Scan(&brandID); err != nil {
		t.Fatal(err)
	}
	if brandID.Valid {
		t.Fatalf("itm001.brand_id = %q, want NULL (its brand br_coca never landed)", brandID.String)
	}

	// A sibling item on a different, unclashed brand keeps it.
	var pepsiBrand sql.NullString
	if err := d.DB.QueryRow(`SELECT brand_id FROM items WHERE id = 'itm002'`).Scan(&pepsiBrand); err != nil {
		t.Fatal(err)
	}
	if !pepsiBrand.Valid || pepsiBrand.String != "br_pepsi" {
		t.Fatalf("itm002.brand_id = %v, want br_pepsi (unaffected by the br_coca clash)", pepsiBrand)
	}

	// br_coca itself never landed.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM brands WHERE id = 'br_coca'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("br_coca was inserted despite the operator's clashing brand name")
	}

	// The operator's own brand is untouched.
	var name string
	if err := d.DB.QueryRow(`SELECT name FROM brands WHERE id = 'my_coke'`).Scan(&name); err != nil {
		t.Fatalf("operator's own brand missing: %v", err)
	}
	if name != "Coca-Cola" {
		t.Fatalf("operator's own brand name = %q, want unchanged Coca-Cola", name)
	}
}

// "Generic" is used by most items AND the café items (br_generic). A clash
// on it must not take any of them down: every item falls back to brand_id
// NULL, the café items land on their dine-in/takeaway tax code exactly as
// without the clash, and the whole catalogue still seeds.
func TestSeedDemoCatalogueBrandNameClashOnGenericFallsBackForAllAffectedItems(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO brands (id, name) VALUES ('my_generic', 'Generic')`); err != nil {
		t.Fatal(err)
	}
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue with a clashing 'Generic' brand name: %v", err)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 52 {
		t.Fatalf("SampleItemCount = %d, %v; want 52 (every demo item lands — only the brand clashed)", n, err)
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM brands WHERE id = 'br_generic'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("br_generic was inserted despite the operator's clashing brand name")
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE brand_id = 'br_generic'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("an item still references absent br_generic")
	}
	// A sample of the many br_generic items (itm003, ..., itm050) fell back
	// to NULL rather than being skipped or FK-failing.
	for _, id := range []string{"itm003", "itm011", "itm050"} {
		var brandID sql.NullString
		if err := d.DB.QueryRow(`SELECT brand_id FROM items WHERE id = ?`, id).Scan(&brandID); err != nil {
			t.Fatalf("item %s missing: %v", id, err)
		}
		if brandID.Valid {
			t.Errorf("item %s brand_id = %q, want NULL", id, brandID.String)
		}
	}

	// The café items (also br_generic) still seed, on their own demo tax
	// code, with brand_id NULL too.
	for _, id := range []string{"itm051", "itm052"} {
		var brandID sql.NullString
		var taxCode string
		if err := d.DB.QueryRow(`SELECT brand_id, tax_code_id FROM items WHERE id = ?`, id).Scan(&brandID, &taxCode); err != nil {
			t.Fatalf("café item %s missing: %v", id, err)
		}
		if brandID.Valid {
			t.Errorf("café item %s brand_id = %q, want NULL", id, brandID.String)
		}
		if taxCode != demoCafeTaxCodeID {
			t.Errorf("café item %s tax_code_id = %q, want %q", id, taxCode, demoCafeTaxCodeID)
		}
	}

	// The operator's own brand is untouched.
	var name string
	if err := d.DB.QueryRow(`SELECT name FROM brands WHERE id = 'my_generic'`).Scan(&name); err != nil {
		t.Fatalf("operator's own brand missing: %v", err)
	}
	if name != "Generic" {
		t.Fatalf("operator's own brand name = %q, want unchanged Generic", name)
	}
}

// "Remove sample data" still works normally after a brand clash, and leaves
// the operator's own brand alone.
func TestRemoveDemoCatalogueAfterBrandClashLeavesOperatorBrandAlone(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO brands (id, name) VALUES ('my_coke', 'Coca-Cola')`); err != nil {
		t.Fatal(err)
	}
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	removed, kept, err := repo.RemoveDemoCatalogue(ctx)
	if err != nil {
		t.Fatalf("RemoveDemoCatalogue: %v", err)
	}
	if removed != 52 || len(kept) != 0 {
		t.Fatalf("RemoveDemoCatalogue = removed %d, kept %d; want 52, 0", removed, len(kept))
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("items after removal = %d, want 0", n)
	}
	var name string
	if err := d.DB.QueryRow(`SELECT name FROM brands WHERE id = 'my_coke'`).Scan(&name); err != nil {
		t.Fatalf("operator's own brand missing after removal: %v", err)
	}
	if name != "Coca-Cola" {
		t.Fatalf("operator's own brand name = %q after removal, want unchanged Coca-Cola", name)
	}
}

// Without a clash the lookup must still resolve every brand: a regression
// that NULLed brand_id unconditionally would pass the clash tests above.
func TestSeedDemoCatalogueWithoutBrandClashKeepsEveryBrand(t *testing.T) {
	d := openDemoSeedTestDB(t)
	if err := NewDemoSeedRepo(d.DB).SeedDemoCatalogue(context.Background()); err != nil {
		t.Fatalf("SeedDemoCatalogue: %v", err)
	}
	var nulls int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE is_sample_data = 1 AND brand_id IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatalf("%d demo items have brand_id NULL with no brand clash, want 0", nulls)
	}
	for _, id := range []string{"itm001", "itm051", "itm052"} {
		want := map[string]string{"itm001": "br_coca", "itm051": "br_generic", "itm052": "br_generic"}[id]
		var got string
		if err := d.DB.QueryRow(`SELECT brand_id FROM items WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got != want {
			t.Errorf("%s.brand_id = %q, want %q", id, got, want)
		}
	}
}
