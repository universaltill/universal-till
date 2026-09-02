package data

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestPOSRepo_SearchActiveItems_OrderAndPagination(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "apple", SKU: "SKU-1", Name: "Apple", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "banana", SKU: "SKU-2", Name: "Banana", BasePrice: 120, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "cherry", SKU: "SKU-3", Name: "Cherry", BasePrice: 130, IsActive: true})
	testsupport.SeedBarcode(t, db, "12345", "cherry", true)

	// Pagination: offset=1 limit=1 should return the second item alphabetically ("Banana").
	items, err := repo.SearchActiveItems(ctx, "", 1, 1)
	if err != nil {
		t.Fatalf("SearchActiveItems: %v", err)
	}
	if len(items) != 1 || items[0].Name != "Banana" {
		t.Fatalf("expected Banana as second item, got %+v", items)
	}

	// Barcode search should include barcode matches.
	items, err = repo.SearchActiveItems(ctx, "12345", 0, 5)
	if err != nil {
		t.Fatalf("SearchActiveItems barcode: %v", err)
	}
	if len(items) != 1 || items[0].ID != "cherry" {
		t.Fatalf("expected barcode match for cherry, got %+v", items)
	}
}

func TestPOSRepo_LookupActiveVariant_ValidatesInput(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	// Should error on empty variant ID
	if _, err := repo.LookupActiveVariant(ctx, ""); err == nil {
		t.Fatal("expected error for empty variantID")
	}

	// Inactive variant should error
	itemID := uuid.NewString()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: itemID, SKU: "SKU-10", Name: "Item", BasePrice: 100, IsActive: true})
	variantID := uuid.NewString()
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: variantID, ItemID: itemID, SKU: "VAR-1", Name: "Var", Price: 150, IsActive: false})

	if _, err := repo.LookupActiveVariant(ctx, variantID); err == nil {
		t.Fatal("expected error for inactive variant")
	}
}

// TestPOSRepo_LookupActiveVariant_TolerantOfNullSKU is ut-docs#1205 (same
// landmine class as ut-docs#1176): item_variants.sku is a nullable UNIQUE
// column, and CatalogRepo.CreateVariant stores NULL for a variant created
// with no SKU. Before the COALESCE(sku, ”) fix, calling LookupActiveVariant
// on such a variant failed with "sql: Scan error … converting NULL to
// string is unsupported" instead of returning the variant with SKU == "".
func TestPOSRepo_LookupActiveVariant_TolerantOfNullSKU(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	catalogRepo := NewCatalogRepo(db)
	posRepo := NewPOSRepo(db)
	ctx := context.Background()

	itemID := uuid.NewString()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: itemID, SKU: "SKU-20", Name: "Item", BasePrice: 100, IsActive: true})

	variantID, err := catalogRepo.CreateVariant(ctx, catalogtypes.VariantInput{
		ItemID: itemID, Name: "No-SKU Variant", Price: 250, IsActive: true, // SKU left blank → stored as NULL
	})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	var sku sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT sku FROM item_variants WHERE id = ?`, variantID).Scan(&sku); err != nil {
		t.Fatalf("query sku: %v", err)
	}
	if sku.Valid {
		t.Fatalf("test setup: expected a NULL sku for a variant created with no SKU, got %q", sku.String)
	}

	v, err := posRepo.LookupActiveVariant(ctx, variantID)
	if err != nil {
		t.Fatalf("LookupActiveVariant on a NULL-SKU variant: %v", err)
	}
	if v.SKU != "" {
		t.Fatalf("expected NULL sku to read as \"\", got %q", v.SKU)
	}
}
