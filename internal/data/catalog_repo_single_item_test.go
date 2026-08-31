package data_test

// Single-item counterparts to the whole-catalog listing methods
// (ut-docs#1363): after a catalog mutation the handlers re-render only the
// ONE affected row, so they need per-item fetches that return exactly what
// ListItems/ItemBarcodes/ItemVariants would have said about that item —
// same columns, same ordering — without touching any other row.

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"

	"github.com/universaltill/universal-till/internal/data"
)

func TestGetItem(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedCategory(t, db, "cat1", "Drinks", true)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{
		ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 320,
		TaxCodeID: "tax_std", IsActive: true,
	})
	// ItemSeed carries no category field; set it directly so the nullable
	// lookup columns round-trip through GetItem's scan.
	if _, err := db.Exec(`UPDATE items SET category_id = 'cat1' WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}

	itm, ok, err := repo.GetItem(ctx, "i1")
	if err != nil || !ok {
		t.Fatalf("expected item, got ok=%v err=%v", ok, err)
	}
	if itm.ID != "i1" || itm.SKU != "S1" || itm.Name != "Latte" || itm.BasePrice != 320 {
		t.Fatalf("unexpected item: %+v", itm)
	}
	if itm.TaxCodeID == nil || *itm.TaxCodeID != "tax_std" {
		t.Fatalf("expected tax code preserved, got %+v", itm.TaxCodeID)
	}
	if itm.CategoryID == nil || *itm.CategoryID != "cat1" {
		t.Fatalf("expected category preserved, got %+v", itm.CategoryID)
	}

	// Unlike ListItems there is deliberately NO is_active filter — the one
	// caller (row re-render after a mutation) may need the row it just
	// deactivated to decide between "re-render" and "remove".
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Retired", BasePrice: 100, IsActive: false})
	itm2, ok, err := repo.GetItem(ctx, "i2")
	if err != nil || !ok {
		t.Fatalf("expected the inactive item to be returned, got ok=%v err=%v", ok, err)
	}
	if itm2.IsActive {
		t.Fatal("expected IsActive=false to round-trip")
	}

	// Missing id: (zero, false, nil) — not an error.
	if _, ok, err := repo.GetItem(ctx, "missing"); err != nil || ok {
		t.Fatalf("expected ok=false for a missing item, got ok=%v err=%v", ok, err)
	}
}

func TestGetItem_NullableColumnsAndNoSKU(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	// A NULL sku (no real SKU, ut-docs#1176) must scan as "" — same
	// COALESCE ListItems carries.
	if _, err := db.Exec(`INSERT INTO items (id, sku, name, description, unit, base_price, is_active, is_weighed)
VALUES ('i1', NULL, 'Bare Item', NULL, 'each', 150, 1, 0)`); err != nil {
		t.Fatal(err)
	}

	itm, ok, err := repo.GetItem(ctx, "i1")
	if err != nil || !ok {
		t.Fatalf("expected item, got ok=%v err=%v", ok, err)
	}
	if itm.SKU != "" || itm.Description != "" {
		t.Fatalf("expected NULL sku/description to read as empty strings, got %+v", itm)
	}
	if itm.TaxCodeID != nil || itm.CategoryID != nil || itm.BrandID != nil {
		t.Fatalf("expected nil lookups for NULL columns, got %+v", itm)
	}
}

func TestItemBarcodesFor(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Other", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary)
VALUES ('333','i1',0),('111','i1',0),('222','i1',1),('999','i2',1)`); err != nil {
		t.Fatal(err)
	}

	// Same ordering contract as ItemBarcodes: primary first, then by code —
	// and ONLY this item's codes, never a sibling's.
	got, err := repo.ItemBarcodesFor(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"222", "111", "333"}
	if len(got) != len(want) {
		t.Fatalf("barcodes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("barcodes = %v, want %v", got, want)
		}
	}

	// No barcodes: empty, not an error.
	if got, err := repo.ItemBarcodesFor(ctx, "missing"); err != nil || len(got) != 0 {
		t.Fatalf("expected no barcodes for an unknown item, got %v err=%v", got, err)
	}
}

func TestItemVariantsFor(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 120, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Other", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "i1", SKU: "S1-B", Name: "Bottle", Price: 200, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-A", Name: "Can", Price: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v3", ItemID: "i1", SKU: "S1-C", Name: "Retired", Price: 300, IsActive: false})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v4", ItemID: "i2", SKU: "S2-A", Name: "Alien", Price: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES ('555','v1',1)`); err != nil {
		t.Fatal(err)
	}

	// Same contract as ItemVariants: active only, ORDER BY name, each with
	// its primary barcode — and only THIS item's variants.
	got, err := repo.ItemVariantsFor(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 active variants, got %d: %+v", len(got), got)
	}
	if got[0].Name != "Bottle" || got[1].Name != "Can" {
		t.Fatalf("expected name order Bottle, Can — got %q, %q", got[0].Name, got[1].Name)
	}
	if got[1].Barcode != "555" {
		t.Fatalf("expected Can to carry its primary barcode, got %+v", got[1])
	}
	if got[0].Barcode != "" {
		t.Fatalf("expected Bottle to have no barcode, got %q", got[0].Barcode)
	}

	if got, err := repo.ItemVariantsFor(ctx, "missing"); err != nil || len(got) != 0 {
		t.Fatalf("expected no variants for an unknown item, got %v err=%v", got, err)
	}
}

// The variant-deactivate and barcode-delete endpoints can be called with no
// item id in the form at all (no panel open) — the affected row's item has
// to be resolved server-side so its summary line can still be re-rendered.
func TestItemIDForVariant(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})

	id, ok, err := repo.ItemIDForVariant(ctx, "v1")
	if err != nil || !ok || id != "i1" {
		t.Fatalf("want (i1,true), got id=%q ok=%v err=%v", id, ok, err)
	}

	// A deactivated variant still resolves — the deactivate handler asks
	// AFTER flipping is_active, and the row is soft-deleted, never gone.
	if _, err := db.Exec(`UPDATE item_variants SET is_active = 0 WHERE id = 'v1'`); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := repo.ItemIDForVariant(ctx, "v1"); err != nil || !ok || id != "i1" {
		t.Fatalf("inactive variant must still resolve, got id=%q ok=%v err=%v", id, ok, err)
	}

	if _, ok, err := repo.ItemIDForVariant(ctx, "missing"); err != nil || ok {
		t.Fatalf("unknown variant: want ok=false, got ok=%v err=%v", ok, err)
	}
}

func TestItemIDForBarcode(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES ('111','i1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES ('222','v1',1)`); err != nil {
		t.Fatal(err)
	}

	// An item barcode resolves to its item.
	if id, ok, err := repo.ItemIDForBarcode(ctx, "111"); err != nil || !ok || id != "i1" {
		t.Fatalf("item barcode: want (i1,true), got id=%q ok=%v err=%v", id, ok, err)
	}
	// A variant barcode resolves to the variant's PARENT item — that's the
	// row whose summary line shows it.
	if id, ok, err := repo.ItemIDForBarcode(ctx, "222"); err != nil || !ok || id != "i1" {
		t.Fatalf("variant barcode: want (i1,true), got id=%q ok=%v err=%v", id, ok, err)
	}
	// Unknown code: (empty, false, nil) — not an error.
	if _, ok, err := repo.ItemIDForBarcode(ctx, "never-attached"); err != nil || ok {
		t.Fatalf("unknown barcode: want ok=false, got ok=%v err=%v", ok, err)
	}
}

// HasOtherActiveItems decides whether a freshly inserted row's response
// must also clear the empty-state placeholder: the placeholder is in the
// DOM exactly when the new item is the catalog's SOLE active item.
func TestHasOtherActiveItems(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "First", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Retired", BasePrice: 100, IsActive: false})

	// i1 is the only active item — nothing else counts.
	if ok, err := repo.HasOtherActiveItems(ctx, "i1"); err != nil || ok {
		t.Fatalf("sole active item: want false, got ok=%v err=%v", ok, err)
	}
	// From an inactive item's perspective, i1 IS another active item.
	if ok, err := repo.HasOtherActiveItems(ctx, "i2"); err != nil || !ok {
		t.Fatalf("other active item exists: want true, got ok=%v err=%v", ok, err)
	}

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i3", SKU: "S3", Name: "Second", BasePrice: 200, IsActive: true})
	if ok, err := repo.HasOtherActiveItems(ctx, "i1"); err != nil || !ok {
		t.Fatalf("with a second active item: want true, got ok=%v err=%v", ok, err)
	}
}

func TestHasActiveItems(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	// Empty catalog: false.
	if ok, err := repo.HasActiveItems(ctx); err != nil || ok {
		t.Fatalf("empty catalog: want false, got ok=%v err=%v", ok, err)
	}

	// An inactive item alone doesn't count.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Retired", BasePrice: 100, IsActive: false})
	if ok, err := repo.HasActiveItems(ctx); err != nil || ok {
		t.Fatalf("inactive-only catalog: want false, got ok=%v err=%v", ok, err)
	}

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Live", BasePrice: 100, IsActive: true})
	if ok, err := repo.HasActiveItems(ctx); err != nil || !ok {
		t.Fatalf("catalog with an active item: want true, got ok=%v err=%v", ok, err)
	}

	// After deactivating the last active item it flips back to false — the
	// exact decision the empty-state OOB fragment hangs off.
	if err := repo.DeactivateItem(ctx, "i2"); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.HasActiveItems(ctx); err != nil || ok {
		t.Fatalf("after deactivating the last item: want false, got ok=%v err=%v", ok, err)
	}
}
