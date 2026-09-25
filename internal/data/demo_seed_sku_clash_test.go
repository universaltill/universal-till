package data

import (
	"context"
	"testing"
)

// ut-docs#2639: items.sku and item_variants.sku are UNIQUE. If an operator
// already has a row on the same SKU a demo item/variant would use,
// INSERT OR IGNORE skips only that one demo row — but its dependents
// (item_barcodes, item_images, item_variants, variant_barcodes, inventory,
// price_history, shortcut_buttons) still reference the now-absent id, so a
// plain INSERT among them FK-fails and rolls back the whole transaction.
// Every dependent insert must gate on its parent actually having landed
// (INSERT … SELECT … FROM (VALUES …) WHERE EXISTS (parent)), so a clash
// skips only that item/variant and its own dependents.

// An operator's own item already on SKU-0001 makes itm001's INSERT OR
// IGNORE a no-op. Every itm001 dependent row must be skipped too, not
// FK-fail the whole catalogue.
func TestSeedDemoCatalogueItemSKUClashSkipsOnlyThatItem(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, tax_code_id) VALUES ('own_item', 'SKU-0001', 'My Own Cola', 150, 'tax_std')`); err != nil {
		t.Fatal(err)
	}
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue with a clashing item SKU: %v", err)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 51 {
		t.Fatalf("SampleItemCount = %d, %v; want 51 (every demo item but the clashing itm001)", n, err)
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("itm001 was inserted despite the operator's clashing SKU-0001")
	}

	for _, tbl := range []string{"item_barcodes", "item_images", "shortcut_buttons"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + tbl + ` WHERE item_id = 'itm001'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s has a row referencing absent itm001 — should have been skipped", tbl)
		}
	}
	for _, tbl := range []string{"inventory", "price_history"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + tbl + ` WHERE item_id = 'itm001'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s has a row referencing absent itm001 — should have been skipped", tbl)
		}
	}

	// The operator's own item is untouched.
	var sku, name string
	var price int
	if err := d.DB.QueryRow(`SELECT sku, name, base_price FROM items WHERE id = 'own_item'`).Scan(&sku, &name, &price); err != nil {
		t.Fatalf("operator's own item missing: %v", err)
	}
	if sku != "SKU-0001" || name != "My Own Cola" || price != 150 {
		t.Fatalf("operator's own item = (%q, %q, %d), want unchanged (SKU-0001, My Own Cola, 150)", sku, name, price)
	}

	// A sibling item's dependents are unaffected.
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE item_id = 'itm002'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("itm002 barcode row = %d, want 1 — sibling items must still seed", n)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE item_id = 'itm002'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("itm002 inventory row = %d, want 1 — sibling items must still seed", n)
	}
}

// An operator's own item already on SKU-0006 makes itm006's INSERT OR
// IGNORE a no-op. itm006 carries variants var001/var002 (item_variants) —
// with the parent item never landing, those variants (and everything
// hanging off them) must be skipped too, not FK-fail the whole catalogue.
func TestSeedDemoCatalogueItemSKUClashSkipsDependentVariants(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, tax_code_id) VALUES ('own_item', 'SKU-0006', 'My Own Apple Juice', 200, 'tax_std')`); err != nil {
		t.Fatal(err)
	}
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue with a clashing item SKU: %v", err)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 51 {
		t.Fatalf("SampleItemCount = %d, %v; want 51 (every demo item but the clashing itm006)", n, err)
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM item_variants WHERE item_id = 'itm006'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("item_variants has a row for absent itm006 — should have been skipped")
	}
	for _, variantID := range []string{"var001", "var002"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM item_variants WHERE id = ?`, variantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("item_variants %s still present despite its parent item itm006 never landing", variantID)
		}
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM variant_barcodes WHERE variant_id = ?`, variantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("variant_barcodes has a row referencing absent variant %s", variantID)
		}
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE variant_id = ?`, variantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("inventory has a row referencing absent variant %s", variantID)
		}
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM price_history WHERE variant_id = ?`, variantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("price_history has a row referencing absent variant %s", variantID)
		}
	}
}

// An operator's own variant already on SKU-0006-6P makes var001's INSERT OR
// IGNORE a no-op (the parent item itm006 lands fine — only the variant SKU
// clashes). var001's own dependents (variant_barcodes, inventory,
// price_history) must be skipped too; var002 (a sibling variant of the same
// item) must still land normally.
func TestSeedDemoCatalogueVariantSKUClashSkipsOnlyThatVariant(t *testing.T) {
	d := openDemoSeedTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, tax_code_id) VALUES ('own_item', 'SKU-9001', 'My Own Item', 200, 'tax_std')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('own_variant', 'own_item', 'SKU-0006-6P', 'My Own Pack', 500)`); err != nil {
		t.Fatal(err)
	}
	repo := NewDemoSeedRepo(d.DB)
	if err := repo.SeedDemoCatalogue(ctx); err != nil {
		t.Fatalf("SeedDemoCatalogue with a clashing variant SKU: %v", err)
	}
	if n, err := repo.SampleItemCount(ctx); err != nil || n != 52 {
		t.Fatalf("SampleItemCount = %d, %v; want 52 (every demo item lands — only the variant clashed)", n, err)
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM item_variants WHERE id = 'var001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("var001 was inserted despite the operator's clashing SKU-0006-6P")
	}
	for _, tbl := range []string{"variant_barcodes", "inventory", "price_history"} {
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM ` + tbl + ` WHERE variant_id = 'var001'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s has a row referencing absent var001 — should have been skipped", tbl)
		}
	}

	// var002 (sibling variant, same parent item itm006) is unaffected.
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM item_variants WHERE id = 'var002'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("var002 missing — a sibling variant's SKU clash must not affect it")
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM variant_barcodes WHERE variant_id = 'var002'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("var002's barcode row missing")
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM price_history WHERE variant_id = 'var002'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("var002's price_history row missing")
	}

	// The operator's own variant is untouched.
	var sku string
	if err := d.DB.QueryRow(`SELECT sku FROM item_variants WHERE id = 'own_variant'`).Scan(&sku); err != nil {
		t.Fatalf("operator's own variant missing: %v", err)
	}
	if sku != "SKU-0006-6P" {
		t.Fatalf("operator's own variant sku = %q, want unchanged SKU-0006-6P", sku)
	}
}
