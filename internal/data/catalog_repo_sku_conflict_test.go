package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// items.sku and item_variants.sku are each UNIQUE (001_init.sql) --
// Create/UpdateItem and Create/UpdateVariant must surface a duplicate as
// the distinguishable data.ErrSKUExists, not a raw 500-shaped driver
// error, so the handler layer (ut-docs#316's review) can show the
// operator a specific "that SKU is already in use" message instead of a
// generic one that names nothing. Mirrors
// TestCreateTaxCode_DuplicateNameConflict's ErrTaxCodeNameExists pattern.

func TestCreateItem_DuplicateSKUIsErrSKUExists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "DUP", Name: "First", BasePrice: 100, IsActive: true})

	if _, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Second", SKU: "DUP", BasePrice: 200, IsActive: true,
	}); !errors.Is(err, data.ErrSKUExists) {
		t.Fatalf("expected ErrSKUExists, got %v", err)
	}
}

func TestUpdateItem_DuplicateSKUIsErrSKUExists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "A", Name: "One", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "B", Name: "Two", BasePrice: 100, IsActive: true})

	err := repo.UpdateItem(ctx, catalogtypes.ItemInput{
		ID: "itm2", Name: "Two", SKU: "A", BasePrice: 100, IsActive: true,
	})
	if !errors.Is(err, data.ErrSKUExists) {
		t.Fatalf("expected ErrSKUExists, got %v", err)
	}
}

func TestCreateVariant_DuplicateSKUIsErrSKUExists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	if _, err := repo.CreateVariant(ctx, catalogtypes.VariantInput{
		ItemID: "itm1", SKU: "S1-L", Name: "Large 2", Price: 350, IsActive: true,
	}); !errors.Is(err, data.ErrSKUExists) {
		t.Fatalf("expected ErrSKUExists, got %v", err)
	}
}

func TestUpdateVariant_DuplicateSKUIsErrSKUExists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "itm1", SKU: "S1-M", Name: "Medium", Price: 300, IsActive: true})

	err := repo.UpdateVariant(ctx, catalogtypes.VariantInput{
		ID: "v2", Name: "Medium", SKU: "S1-L", Price: 300, IsActive: true,
	})
	if !errors.Is(err, data.ErrSKUExists) {
		t.Fatalf("expected ErrSKUExists, got %v", err)
	}
}
