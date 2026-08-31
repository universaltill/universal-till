package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#1363: single-item versions of the whole-catalog queries
// (ListItems/ItemBarcodes/ItemVariants) so the catalog admin's per-row HTMX
// OOB responses don't refetch the entire unbounded catalog per mutation.

func newRowQueryDB(t *testing.T) (*data.CatalogRepo, *db.DB, func(q string, args ...any)) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ex := func(q string, args ...any) {
		if _, err := d.DB.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	return data.NewCatalogRepo(d.DB), d, ex
}

func TestGetItem_SingleItemMatchesListItemsShape(t *testing.T) {
	repo, _, ex := newRowQueryDB(t)
	ctx := context.Background()
	ex(`INSERT INTO categories (id, name) VALUES ('cat1','Drinks')`)
	ex(`INSERT INTO tax_codes (id, name, rate_basis_points, is_active) VALUES ('tax1','Standard',1900,1)`)
	ex(`INSERT INTO items (id, sku, name, description, category_id, unit, base_price, tax_code_id, is_active, is_weighed)
	    VALUES ('it1','SKU1','Coffee','nice','cat1','each',300,'tax1',1,0)`)

	itm, ok, err := repo.GetItem(ctx, "it1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !ok {
		t.Fatal("GetItem: item not found")
	}
	if itm.ID != "it1" || itm.SKU != "SKU1" || itm.Name != "Coffee" || itm.Description != "nice" ||
		itm.BasePrice != 300 || itm.Unit != "each" || !itm.IsActive || itm.IsWeighed {
		t.Fatalf("GetItem = %+v, fields don't match the seeded row", itm)
	}
	if itm.CategoryID == nil || *itm.CategoryID != "cat1" {
		t.Fatalf("CategoryID = %v, want cat1", itm.CategoryID)
	}
	if itm.TaxCodeID == nil || *itm.TaxCodeID != "tax1" {
		t.Fatalf("TaxCodeID = %v, want tax1", itm.TaxCodeID)
	}

	// NULL sku must scan as "" (same COALESCE contract as ListItems,
	// ut-docs#1176).
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it2',NULL,'Tea',200,1,0,'each')`)
	itm2, ok, err := repo.GetItem(ctx, "it2")
	if err != nil || !ok {
		t.Fatalf("GetItem it2: ok=%v err=%v", ok, err)
	}
	if itm2.SKU != "" {
		t.Fatalf("NULL sku scanned as %q, want empty", itm2.SKU)
	}
}

// GetItem returns INACTIVE items too, with ok=true and IsActive=false —
// unlike ListItems' is_active filter. The row renderer needs to see "this
// item just went inactive" to answer with an OOB row delete rather than
// treating it as missing.
func TestGetItem_ReturnsInactiveAndReportsMissing(t *testing.T) {
	repo, _, ex := newRowQueryDB(t)
	ctx := context.Background()
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it1','S1','Coffee',300,0,0,'each')`)

	itm, ok, err := repo.GetItem(ctx, "it1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !ok || itm.IsActive {
		t.Fatalf("inactive item: ok=%v IsActive=%v, want ok=true IsActive=false", ok, itm.IsActive)
	}

	if _, ok, err := repo.GetItem(ctx, "nope"); err != nil || ok {
		t.Fatalf("missing item: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

// ItemVariantViews must return exactly what ItemVariants' whole-catalog map
// holds for that item: ACTIVE variants only, name order, each with its
// primary barcode.
func TestItemVariantViews_MatchesWholeCatalogQueryForItem(t *testing.T) {
	repo, _, ex := newRowQueryDB(t)
	ctx := context.Background()
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it1','S1','Coffee',300,1,0,'each')`)
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it2','S2','Tea',200,1,0,'each')`)
	ex(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('v2','it1','Zebra',400,1)`)
	ex(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('v1','it1','Alpha',350,1)`)
	ex(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('v3','it1','Retired',100,0)`)
	ex(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('vx','it2','Other',150,1)`)
	ex(`INSERT INTO variant_barcodes (variant_id, barcode, is_primary) VALUES ('v1','999',1),('v1','111',0)`)

	got, err := repo.ItemVariantViews(ctx, "it1")
	if err != nil {
		t.Fatalf("ItemVariantViews: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d variants, want 2 (active only, this item only): %+v", len(got), got)
	}
	if got[0].Name != "Alpha" || got[1].Name != "Zebra" {
		t.Fatalf("order = [%s %s], want name order [Alpha Zebra]", got[0].Name, got[1].Name)
	}
	if got[0].Barcode != "999" {
		t.Fatalf("Alpha's barcode = %q, want primary-first 999", got[0].Barcode)
	}
	if got[1].Barcode != "" {
		t.Fatalf("Zebra's barcode = %q, want empty", got[1].Barcode)
	}

	whole, err := repo.ItemVariants(ctx)
	if err != nil {
		t.Fatalf("ItemVariants: %v", err)
	}
	if len(whole["it1"]) != len(got) {
		t.Fatalf("single-item view (%d) drifted from whole-catalog map (%d) for it1", len(got), len(whole["it1"]))
	}
}

func TestCountActiveItems(t *testing.T) {
	repo, _, ex := newRowQueryDB(t)
	ctx := context.Background()
	if n, err := repo.CountActiveItems(ctx); err != nil || n != 0 {
		t.Fatalf("empty: n=%d err=%v, want 0", n, err)
	}
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it1','S1','Coffee',300,1,0,'each')`)
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it2','S2','Tea',200,0,0,'each')`)
	if n, err := repo.CountActiveItems(ctx); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, want 1 (inactive rows don't count)", n, err)
	}
}

func TestItemIDForVariant(t *testing.T) {
	repo, _, ex := newRowQueryDB(t)
	ctx := context.Background()
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it1','S1','Coffee',300,1,0,'each')`)
	ex(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('v1','it1','Large',350,0)`)

	// Resolves even for an inactive variant — the caller needs the parent
	// row to re-render right after deactivating it.
	id, ok, err := repo.ItemIDForVariant(ctx, "v1")
	if err != nil || !ok || id != "it1" {
		t.Fatalf("ItemIDForVariant = (%q,%v,%v), want (it1,true,nil)", id, ok, err)
	}
	if _, ok, err := repo.ItemIDForVariant(ctx, "nope"); err != nil || ok {
		t.Fatalf("missing variant: ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestItemIDForBarcode_ResolvesItemAndVariantOwners(t *testing.T) {
	repo, _, ex := newRowQueryDB(t)
	ctx := context.Background()
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it1','S1','Coffee',300,1,0,'each')`)
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('it2','S2','Tea',200,1,0,'each')`)
	ex(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES ('v1','it2','Large',350,1)`)
	ex(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('it1','111',1)`)
	ex(`INSERT INTO variant_barcodes (variant_id, barcode, is_primary) VALUES ('v1','222',1)`)

	if id, ok, err := repo.ItemIDForBarcode(ctx, "111"); err != nil || !ok || id != "it1" {
		t.Fatalf("item-owned barcode = (%q,%v,%v), want (it1,true,nil)", id, ok, err)
	}
	// A variant-owned barcode resolves to the PARENT ITEM — the catalog
	// table has one row per item, never per variant.
	if id, ok, err := repo.ItemIDForBarcode(ctx, "222"); err != nil || !ok || id != "it2" {
		t.Fatalf("variant-owned barcode = (%q,%v,%v), want (it2,true,nil)", id, ok, err)
	}
	if _, ok, err := repo.ItemIDForBarcode(ctx, "000"); err != nil || ok {
		t.Fatalf("unattached barcode: ok=%v err=%v, want false,nil", ok, err)
	}
}
