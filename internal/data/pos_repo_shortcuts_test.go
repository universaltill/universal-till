package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestSearchItemsForShortcuts_PaginationAndOrder(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "item-a", SKU: "A", Name: "Apple", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "item-b", SKU: "B", Name: "Banana", BasePrice: 200, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "item-c", SKU: "C", Name: "Cherry", BasePrice: 300, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "item-x", SKU: "X", Name: "Inactive", BasePrice: 400, IsActive: false})

	testsupport.SeedBarcode(t, db, "BAR-A", "item-a", true)
	testsupport.SeedBarcode(t, db, "BAR-B", "item-b", true)
	testsupport.SeedBarcode(t, db, "BAR-C", "item-c", true)
	testsupport.SeedBarcode(t, db, "BAR-X", "item-x", true)

	repo := NewPOSRepo(db)
	ctx := context.Background()

	firstPage, err := repo.SearchItemsForShortcuts(ctx, "", 0, 2)
	if err != nil {
		t.Fatalf("search shortcuts first page: %v", err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("expected 2 results, got %d", len(firstPage))
	}
	if firstPage[0].Name != "Apple" || firstPage[1].Name != "Banana" {
		t.Fatalf("unexpected ordering on first page: %+v", firstPage)
	}

	secondPage, err := repo.SearchItemsForShortcuts(ctx, "", 2, 2)
	if err != nil {
		t.Fatalf("search shortcuts second page: %v", err)
	}
	if len(secondPage) != 1 {
		t.Fatalf("expected 1 result on second page, got %d", len(secondPage))
	}
	if secondPage[0].Name != "Cherry" {
		t.Fatalf("unexpected ordering on second page: %+v", secondPage)
	}

	for _, res := range append(firstPage, secondPage...) {
		if res.Name == "Inactive" {
			t.Fatalf("inactive item should not be returned")
		}
		if res.Barcode == "" {
			t.Fatalf("expected barcode for %s", res.Name)
		}
	}
}

// TestSearchItemsForShortcuts_SKUOnlyItemCarriesSKU (ut-docs#1220): an item
// with no barcode row (loose produce, services) is still findable — its SKU
// matches the WHERE clause's "i.sku LIKE ?" arm — but the SELECT never
// projected i.sku, so a barcode-less result came back with Barcode == "" and
// no other identifier the Designer's add-as-button flow could fall back to,
// silently breaking add for exactly this kind of item.
func TestSearchItemsForShortcuts_SKUOnlyItemCarriesSKU(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "item-sku-only", SKU: "SKU-ONLY-1", Name: "Loose Screw", BasePrice: 5, IsActive: true})

	repo := NewPOSRepo(db)
	ctx := context.Background()

	res, err := repo.SearchItemsForShortcuts(ctx, "SKU-ONLY-1", 0, 10)
	if err != nil {
		t.Fatalf("search shortcuts sku-only: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(res), res)
	}
	if res[0].Barcode != "" {
		t.Fatalf("expected no barcode for a barcode-less item, got %q", res[0].Barcode)
	}
	if res[0].SKU != "SKU-ONLY-1" {
		t.Fatalf("SKU = %q, want %q (SKU must be projected even when Barcode is empty)", res[0].SKU, "SKU-ONLY-1")
	}
}
