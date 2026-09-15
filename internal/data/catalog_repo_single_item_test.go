package data_test

// Single-item counterparts to the whole-catalog listing methods
// (ut-docs#1363): after a catalog mutation the handlers re-render only the
// ONE affected row, so they need per-item fetches that return exactly what
// ListItems/ItemBarcodes/ItemVariants would have said about that item —
// same columns, same ordering — without touching any other row.

import (
	"context"
	"testing"
	"time"

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

// TestItemVariantsForSale_UsesActivePriceHistoryRow is ut-docs#2228: the
// sale-screen/kiosk picker must show the variant's CURRENT price, not its
// configured item_variants.price, whenever an active price_history row
// overrides it — same contract, ordering and active-only filter as
// ItemVariantsFor, which this test also uses side-by-side to prove the two
// methods genuinely diverge (ItemVariantsFor must keep returning the raw
// configured price for the catalog admin grid — see its own doc comment).
func TestItemVariantsForSale_UsesActivePriceHistoryRow(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 120, IsActive: true})
	// v1 has an active promotional price_history row (250 instead of its
	// configured 310) — the exact £3.10-vs-£2.50 shape from the ticket.
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-A", Name: "Regular", Price: 310, IsActive: true})
	// v2 has no price_history row at all — must fall back to its configured price.
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "i1", SKU: "S1-B", Name: "Large", Price: 350, IsActive: true})
	// v3 has an EXPIRED price_history row — must NOT apply, falls back too.
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v3", ItemID: "i1", SKU: "S1-C", Name: "Small", Price: 250, IsActive: true})

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	expiredStart := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	expiredEnd := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, variant_id, price, starts_at) VALUES('ph1','v1',250,?)`, past); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO price_history(id, variant_id, price, starts_at, ends_at) VALUES('ph2','v3',999,?,?)`, expiredStart, expiredEnd); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ItemVariantsForSale(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 active variants, got %d: %+v", len(got), got)
	}
	byName := map[string]data.VariantView{}
	for _, v := range got {
		byName[v.Name] = v
	}
	if byName["Regular"].PriceMinor != 250 {
		t.Fatalf("expected Regular's active price_history override 250, got %d", byName["Regular"].PriceMinor)
	}
	if byName["Large"].PriceMinor != 350 {
		t.Fatalf("expected Large's configured price 350 (no price_history row), got %d", byName["Large"].PriceMinor)
	}
	if byName["Small"].PriceMinor != 250 {
		t.Fatalf("expected Small's configured price 250 (price_history row EXPIRED), got %d", byName["Small"].PriceMinor)
	}

	// ItemVariantsFor must be completely unaffected — the catalog admin
	// grid still needs the raw configured price to edit, never a
	// transient promotion (ut-docs#2228's own explicit non-goal).
	plain, err := repo.ItemVariantsFor(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	plainByName := map[string]data.VariantView{}
	for _, v := range plain {
		plainByName[v.Name] = v
	}
	if plainByName["Regular"].PriceMinor != 310 {
		t.Fatalf("ItemVariantsFor must keep returning the CONFIGURED price 310 for admin editing, got %d", plainByName["Regular"].PriceMinor)
	}

	if got, err := repo.ItemVariantsForSale(ctx, "missing"); err != nil || len(got) != 0 {
		t.Fatalf("expected no variants for an unknown item, got %v err=%v", got, err)
	}
}

// TestItemVariantsForSale_MatchesPOSRepoResolveCurrentPrice is the direct
// proof of the ticket's acceptance criterion: the price shown in the
// picker equals the price the basket line receives. ItemVariantsForSale
// and POSRepo.ResolveCurrentPrice are two independent code paths (picker
// display vs. actual basket pricing) that must never disagree under the
// same price_history state.
func TestItemVariantsForSale_MatchesPOSRepoResolveCurrentPrice(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	catalogRepo := data.NewCatalogRepo(db)
	posRepo := data.NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 120, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-A", Name: "Regular", Price: 310, IsActive: true})

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO price_history(id, variant_id, price, starts_at) VALUES('ph1','v1',250,?)`, past); err != nil {
		t.Fatal(err)
	}

	sale, err := catalogRepo.ItemVariantsForSale(ctx, "i1")
	if err != nil || len(sale) != 1 {
		t.Fatalf("ItemVariantsForSale(i1) = %v, %v", sale, err)
	}
	basketPrice, err := posRepo.ResolveCurrentPrice(ctx, "", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if sale[0].PriceMinor != basketPrice {
		t.Fatalf("picker price %d != basket line price %d — same price_history state must resolve identically", sale[0].PriceMinor, basketPrice)
	}
	if sale[0].PriceMinor != 250 {
		t.Fatalf("sanity: expected the active override 250, got %d", sale[0].PriceMinor)
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
