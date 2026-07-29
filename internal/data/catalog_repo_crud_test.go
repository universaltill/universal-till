package data_test

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestBarcodeExists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('123456','i1',1)`); err != nil {
		t.Fatal(err)
	}

	if ok, err := repo.BarcodeExists(ctx, "123456"); err != nil || !ok {
		t.Fatalf("expected barcode to exist, got ok=%v err=%v", ok, err)
	}
	if ok, err := repo.BarcodeExists(ctx, "nope"); err != nil || ok {
		t.Fatalf("expected barcode not to exist, got ok=%v err=%v", ok, err)
	}

	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES('999','v1',1)`); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.BarcodeExists(ctx, "999"); err != nil || !ok {
		t.Fatalf("expected variant barcode to exist, got ok=%v err=%v", ok, err)
	}
}

func TestSKUExists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "ABC", Name: "Item", BasePrice: 100, IsActive: true})

	if ok, err := repo.SKUExists(ctx, "ABC"); err != nil || !ok {
		t.Fatalf("expected sku to exist, got ok=%v err=%v", ok, err)
	}
	if ok, err := repo.SKUExists(ctx, "ZZZ"); err != nil || ok {
		t.Fatalf("expected sku not to exist, got ok=%v err=%v", ok, err)
	}
}

func TestEnsureCategory(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id1, err := repo.EnsureCategory(ctx, "Drinks")
	if err != nil || id1 == "" {
		t.Fatalf("expected a new category id, got %q err=%v", id1, err)
	}

	// Re-running with the same name (even different case) must return the
	// SAME id, not create a duplicate — imports call this per row.
	id2, err := repo.EnsureCategory(ctx, "drinks")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Fatalf("expected EnsureCategory to be idempotent (case-insensitive), got %q then %q", id1, id2)
	}

	// Blank name is a no-op, not an error and not a row.
	id3, err := repo.EnsureCategory(ctx, "   ")
	if err != nil {
		t.Fatal(err)
	}
	if id3 != "" {
		t.Fatalf("expected empty id for a blank category name, got %q", id3)
	}
}

func TestGetItemLabel(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "SKU1", Name: "Latte", BasePrice: 320, IsActive: true})

	// No barcode yet: falls back to the SKU as the printable code.
	l, ok, err := repo.GetItemLabel(ctx, "i1")
	if err != nil || !ok {
		t.Fatalf("expected label, got ok=%v err=%v", ok, err)
	}
	if l.Name != "Latte" || l.PriceMinor != 320 || l.Code != "SKU1" {
		t.Fatalf("unexpected label: %+v", l)
	}

	// With a primary barcode, the barcode wins over the SKU.
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('5000001','i1',1)`); err != nil {
		t.Fatal(err)
	}
	l, ok, err = repo.GetItemLabel(ctx, "i1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if l.Code != "5000001" {
		t.Fatalf("expected primary barcode to win over SKU, got %q", l.Code)
	}

	if _, ok, err := repo.GetItemLabel(ctx, "missing"); err != nil || ok {
		t.Fatalf("expected ok=false for a missing item, got ok=%v err=%v", ok, err)
	}
}

func TestItemExists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})

	if ok, err := repo.ItemExists(ctx, "i1"); err != nil || !ok {
		t.Fatalf("expected item to exist, got ok=%v err=%v", ok, err)
	}
	if ok, err := repo.ItemExists(ctx, "missing"); err != nil || ok {
		t.Fatalf("expected item not to exist, got ok=%v err=%v", ok, err)
	}
}

func TestListItems_OnlyActiveOrderedByName(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Zebra", BasePrice: 200, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Apple", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i3", SKU: "S3", Name: "Retired", BasePrice: 300, IsActive: false})

	items, err := repo.ListItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 active items, got %d: %+v", len(items), items)
	}
	if items[0].Name != "Apple" || items[1].Name != "Zebra" {
		t.Fatalf("expected alphabetical order Apple, Zebra — got %s, %s", items[0].Name, items[1].Name)
	}
}

func TestGetVariantLabel(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	l, ok, err := repo.GetVariantLabel(ctx, "v1")
	if err != nil || !ok {
		t.Fatalf("expected label, got ok=%v err=%v", ok, err)
	}
	if l.Name != "Latte Large" {
		t.Fatalf("expected composed name 'Latte Large', got %q", l.Name)
	}
	if l.PriceMinor != 350 {
		t.Fatalf("expected variant's own price 350, got %d", l.PriceMinor)
	}
	if l.Code != "S1-L" {
		t.Fatalf("expected fallback to variant SKU, got %q", l.Code)
	}

	if _, ok, err := repo.GetVariantLabel(ctx, "missing"); err != nil || ok {
		t.Fatalf("expected ok=false for a missing variant, got ok=%v err=%v", ok, err)
	}
}

func TestVariantsForItem_IncludesInactiveWithBarcodes(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "i1", SKU: "S1-S", Name: "Small", Price: 250, IsActive: false})
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES('111','v1',1)`); err != nil {
		t.Fatal(err)
	}

	out, err := repo.VariantsForItem(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected both active and inactive variants (for reactivation), got %d: %+v", len(out), out)
	}
	// Active variants sort first.
	if !out[0].IsActive || out[0].ID != "v1" {
		t.Fatalf("expected active variant first, got %+v", out[0])
	}
	if len(out[0].Barcodes) != 1 || out[0].Barcodes[0] != "111" {
		t.Fatalf("expected v1's barcode attached, got %+v", out[0].Barcodes)
	}
	if len(out[1].Barcodes) != 0 {
		t.Fatalf("expected v2 to have no barcodes, got %+v", out[1].Barcodes)
	}
}

func TestBarcodesForItem(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('111','i1',0),('222','i1',1)`); err != nil {
		t.Fatal(err)
	}

	out, err := repo.BarcodesForItem(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 barcodes, got %d", len(out))
	}
	if out[0].Barcode != "222" || !out[0].IsPrimary {
		t.Fatalf("expected the primary barcode first, got %+v", out[0])
	}
}

func TestDeleteBarcode(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('111','i1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES('222','v1',1)`); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteBarcode(ctx, "111"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := repo.BarcodeExists(ctx, "111"); ok {
		t.Fatal("expected item barcode to be gone")
	}

	// DeleteBarcode doesn't know which table a barcode lives in — it must
	// try both, so a variant barcode is removed the same way.
	if err := repo.DeleteBarcode(ctx, "222"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := repo.BarcodeExists(ctx, "222"); ok {
		t.Fatal("expected variant barcode to be gone")
	}

	// Deleting a barcode that was never attached is a no-op, not an error.
	if err := repo.DeleteBarcode(ctx, "never-existed"); err != nil {
		t.Fatalf("expected no error deleting an unknown barcode, got %v", err)
	}
}

func TestExportRows(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 320, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('999','i1',1)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Retired Item", BasePrice: 100, IsActive: false})

	rows, err := repo.ExportRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both active and inactive items exported, got %d", len(rows))
	}
	// Active first.
	if rows[0].Name != "Latte" || rows[0].Barcode != "999" || !rows[0].IsActive {
		t.Fatalf("unexpected first export row: %+v", rows[0])
	}
	if rows[1].IsActive {
		t.Fatal("expected the retired item to report IsActive=false")
	}
}

func TestReadLookup(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedCategory(t, db, "c1", "Drinks", true)
	testsupport.SeedCategory(t, db, "c2", "Retired Category", false)

	out, err := repo.ReadLookup(ctx, "categories")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Name != "Drinks" {
		t.Fatalf("expected only the active category, got %+v", out)
	}
}

func TestValidateLookup(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedCategory(t, db, "c1", "Drinks", true)

	if err := repo.ValidateLookup(ctx, "categories", "c1", true); err != nil {
		t.Fatalf("expected a valid id to pass, got %v", err)
	}
	if err := repo.ValidateLookup(ctx, "categories", "", false); err != nil {
		t.Fatalf("expected an empty optional id to pass, got %v", err)
	}
	if err := repo.ValidateLookup(ctx, "categories", "", true); err == nil {
		t.Fatal("expected an empty required id to fail")
	}
	if err := repo.ValidateLookup(ctx, "categories", "does-not-exist", true); err == nil {
		t.Fatal("expected an unknown id to fail")
	}
}

func TestDeactivateItem_CascadesToVariants(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})

	if err := repo.DeactivateItem(ctx, "i1"); err != nil {
		t.Fatal(err)
	}

	items, _ := repo.ListItems(ctx)
	if len(items) != 0 {
		t.Fatalf("expected the item to no longer be active, got %+v", items)
	}
	variants, err := repo.VariantsForItem(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 1 || variants[0].IsActive {
		t.Fatalf("expected the variant to be deactivated along with its item, got %+v", variants)
	}

	if err := repo.DeactivateItem(ctx, ""); err == nil {
		t.Fatal("expected an error for an empty item id")
	}
}

func TestDeactivateVariant(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})

	if err := repo.DeactivateVariant(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	variants, err := repo.VariantsForItem(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if variants[0].IsActive {
		t.Fatal("expected the variant to be inactive")
	}

	if err := repo.DeactivateVariant(ctx, ""); err == nil {
		t.Fatal("expected an error for an empty variant id")
	}
}

func TestItemCostPrice(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})

	// cost_price unset → 0, not an error.
	cost, err := repo.ItemCostPrice(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if cost != 0 {
		t.Fatalf("expected unset cost price to read as 0, got %d", cost)
	}

	if err := repo.SetItemCostPrice(ctx, "i1", 85); err != nil {
		t.Fatal(err)
	}
	cost, err = repo.ItemCostPrice(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if cost != 85 {
		t.Fatalf("expected cost price 85, got %d", cost)
	}

	if err := repo.SetItemCostPrice(ctx, "", 10); err == nil {
		t.Fatal("expected an error for an empty item id")
	}
}

func TestSetItemPrice_FallsBackFromItemToVariant(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})

	if err := repo.SetItemPrice(ctx, "i1", 199); err != nil {
		t.Fatal(err)
	}
	items, _ := repo.ListItems(ctx)
	if items[0].BasePrice != 199 {
		t.Fatalf("expected item price updated to 199, got %d", items[0].BasePrice)
	}

	// The id may be a variant's id too (ADR-0018 remote price directive) —
	// falls through to item_variants when the items UPDATE affects 0 rows.
	if err := repo.SetItemPrice(ctx, "v1", 299); err != nil {
		t.Fatal(err)
	}
	variants, _ := repo.VariantsForItem(ctx, "i1")
	if variants[0].PriceMinor != 299 {
		t.Fatalf("expected variant price updated to 299, got %d", variants[0].PriceMinor)
	}

	if err := repo.SetItemPrice(ctx, "", 100); err == nil {
		t.Fatal("expected an error for an empty id")
	}
	if err := repo.SetItemPrice(ctx, "does-not-exist", 100); err == nil {
		t.Fatal("expected an error when neither an item nor a variant matches")
	}
}

func TestFindActiveItemByName(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Latte", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Old Item", BasePrice: 100, IsActive: false})

	id, ok, err := repo.FindActiveItemByName(ctx, "Latte")
	if err != nil || !ok || id != "i1" {
		t.Fatalf("expected to find i1, got id=%q ok=%v err=%v", id, ok, err)
	}

	// An inactive item with the name must NOT satisfy the idempotency check.
	if _, ok, err := repo.FindActiveItemByName(ctx, "Old Item"); err != nil || ok {
		t.Fatalf("expected inactive item not to match, got ok=%v err=%v", ok, err)
	}
	if _, ok, err := repo.FindActiveItemByName(ctx, "Nonexistent"); err != nil || ok {
		t.Fatalf("expected no match, got ok=%v err=%v", ok, err)
	}
}

func TestSetItemName_FallsBackFromItemToVariant(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})

	if err := repo.SetItemName(ctx, "i1", "Renamed Item"); err != nil {
		t.Fatal(err)
	}
	items, _ := repo.ListItems(ctx)
	if items[0].Name != "Renamed Item" {
		t.Fatalf("expected item renamed, got %q", items[0].Name)
	}

	if err := repo.SetItemName(ctx, "v1", "Renamed Variant"); err != nil {
		t.Fatal(err)
	}
	variants, _ := repo.VariantsForItem(ctx, "i1")
	if variants[0].Name != "Renamed Variant" {
		t.Fatalf("expected variant renamed, got %q", variants[0].Name)
	}

	if err := repo.SetItemName(ctx, "i1", ""); err == nil {
		t.Fatal("expected an error for a blank name")
	}
	if err := repo.SetItemName(ctx, "", "x"); err == nil {
		t.Fatal("expected an error for an empty id")
	}
	if err := repo.SetItemName(ctx, "does-not-exist", "x"); err == nil {
		t.Fatal("expected an error when neither an item nor a variant matches")
	}
}

func TestUpdateItem(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})

	if err := repo.UpdateItem(ctx, catalogtypes.ItemInput{
		ID: "i1", Name: "Updated Name", BasePrice: 250, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}
	items, _ := repo.ListItems(ctx)
	if items[0].Name != "Updated Name" || items[0].BasePrice != 250 {
		t.Fatalf("update did not apply: %+v", items[0])
	}
	// SKU is COALESCE(NULLIF(?, ''), sku) — a blank SKU in the update must
	// NOT clobber the existing SKU.
	if items[0].SKU != "S1" {
		t.Fatalf("expected SKU to be preserved when update omits it, got %q", items[0].SKU)
	}

	// Deactivating via UpdateItem must actually remove it from ListItems.
	if err := repo.UpdateItem(ctx, catalogtypes.ItemInput{
		ID: "i1", Name: "Updated Name", BasePrice: 250, IsActive: false,
	}); err != nil {
		t.Fatal(err)
	}
	items, _ = repo.ListItems(ctx)
	if len(items) != 0 {
		t.Fatal("expected item to be inactive after UpdateItem with IsActive=false")
	}

	if err := repo.UpdateItem(ctx, catalogtypes.ItemInput{ID: "", Name: "x"}); err == nil {
		t.Fatal("expected an error for an empty id")
	}
	if err := repo.UpdateItem(ctx, catalogtypes.ItemInput{ID: "i1", Name: ""}); err == nil {
		t.Fatal("expected an error for a blank name")
	}
}

func TestUpdateVariant(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})

	if err := repo.UpdateVariant(ctx, catalogtypes.VariantInput{
		ID: "v1", Name: "Renamed", Price: 175, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}
	variants, _ := repo.VariantsForItem(ctx, "i1")
	if variants[0].Name != "Renamed" || variants[0].PriceMinor != 175 {
		t.Fatalf("update did not apply: %+v", variants[0])
	}
	if variants[0].SKU != "S1-V" {
		t.Fatalf("expected SKU preserved when update omits it, got %q", variants[0].SKU)
	}

	if err := repo.UpdateVariant(ctx, catalogtypes.VariantInput{ID: "", Name: "x"}); err == nil {
		t.Fatal("expected an error for an empty id")
	}
	if err := repo.UpdateVariant(ctx, catalogtypes.VariantInput{ID: "v1", Name: ""}); err == nil {
		t.Fatal("expected an error for a blank name")
	}
}

func TestAddBarcode_ItemAndVariant(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})

	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "111", ItemID: "i1", IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := repo.BarcodeExists(ctx, "111"); !ok {
		t.Fatal("expected item barcode attached")
	}

	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "222", VariantID: "v1", IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := repo.BarcodeExists(ctx, "222"); !ok {
		t.Fatal("expected variant barcode attached")
	}

	// Exactly one of item_id/variant_id required.
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "333"}); err == nil {
		t.Fatal("expected an error when neither item_id nor variant_id is set")
	}
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "333", ItemID: "i1", VariantID: "v1"}); err == nil {
		t.Fatal("expected an error when both item_id and variant_id are set")
	}
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "", ItemID: "i1"}); err == nil {
		t.Fatal("expected an error for a blank barcode")
	}

	// Unknown / inactive target.
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "444", ItemID: "does-not-exist"}); err == nil {
		t.Fatal("expected an error for an unknown item")
	}
	if err := repo.DeactivateItem(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "555", ItemID: "i1"}); err == nil {
		t.Fatal("expected an error attaching a barcode to an inactive item")
	}
}

func TestAddBarcode_RejectsCrossTargetReassignment(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item A", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Item B", BasePrice: 200, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant", Price: 150, IsActive: true})

	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "111", ItemID: "i1"}); err != nil {
		t.Fatal(err)
	}

	// Same barcode, same item again: allowed (re-scan / re-save is routine).
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "111", ItemID: "i1", IsPrimary: true}); err != nil {
		t.Fatalf("expected re-attaching the same barcode to the same item to succeed, got %v", err)
	}

	// Same barcode, a DIFFERENT item: rejected — must not silently move it.
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "111", ItemID: "i2"}); err == nil {
		t.Fatal("expected an error moving a barcode to a different item")
	} else if !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("expected an 'already assigned' error, got %v", err)
	}

	// Same barcode, a variant: also rejected (item vs variant collision).
	if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "111", VariantID: "v1"}); err == nil {
		t.Fatal("expected an error attaching an item's barcode to a variant")
	}
}
