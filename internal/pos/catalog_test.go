package pos

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

func setupCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	return testsupport.NewCatalogTestDB(t)
}

func TestAddBarcode_XORValidation(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()

	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "123"}); err == nil {
		t.Fatalf("expected xor validation error")
	}

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Apple", BasePrice: 100, IsActive: true})
	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "123", ItemID: "itm1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAddBarcode_InactiveItemFails(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Apple", BasePrice: 100, IsActive: false})
	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "123", ItemID: "itm1"}); err == nil {
		t.Fatalf("expected inactive item failure")
	}
}

func TestAddBarcode_CrossAssignmentBlocked(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Apple", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "SKU2", Name: "Banana", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var1", ItemID: "itm1", Name: "500ml", Price: 150, IsActive: true})

	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "B1", ItemID: "itm1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "B1", ItemID: "itm1"}); err != nil {
		t.Fatalf("re-assign same barcode to same item should succeed: %v", err)
	}
	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "B1", ItemID: "itm2"}); err == nil {
		t.Fatalf("expected failure when moving barcode to different item")
	}
	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "B1", VariantID: "var1"}); err == nil {
		t.Fatalf("expected failure when moving barcode to variant")
	}
}

func TestCreateVariantAndBarcode(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Apple", BasePrice: 100, IsActive: true})

	varID, err := CreateVariant(ctx, db, VariantInput{ItemID: "itm1", Name: "500ml", Price: 150, IsActive: true})
	if err != nil {
		t.Fatalf("CreateVariant error: %v", err)
	}
	if varID == "" {
		t.Fatalf("expected variant ID")
	}
	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "VAR123", VariantID: varID, IsPrimary: true}); err != nil {
		t.Fatalf("AddBarcode variant error: %v", err)
	}
}

func TestUpdateItemAndVariant(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Apple", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var1", ItemID: "itm1", SKU: "VSKU", Name: "Old", Price: 150, IsActive: true})

	desc := "Fresh apples"
	cat := "cat1"
	brand := "brand1"
	tax := "tax1"
	err := UpdateItem(ctx, db, ItemInput{
		ID:          "itm1",
		SKU:         "SKU-NEW",
		Name:        "Apple Updated",
		Description: desc,
		BasePrice:   250,
		CategoryID:  &cat,
		BrandID:     &brand,
		TaxCodeID:   &tax,
		Unit:        "each",
		IsWeighed:   true,
		IsActive:    true,
	})
	if err != nil {
		t.Fatalf("UpdateItem error: %v", err)
	}
	var name, sku, descOut, catOut, brandOut, taxOut string
	var price int64
	var weighed int
	_ = db.QueryRow(`SELECT name, sku, description, category_id, brand_id, tax_code_id, base_price, is_weighed FROM items WHERE id='itm1'`).Scan(&name, &sku, &descOut, &catOut, &brandOut, &taxOut, &price, &weighed)
	if name != "Apple Updated" || sku != "SKU-NEW" || descOut != desc || catOut != cat || brandOut != brand || taxOut != tax || price != 250 || weighed != 1 {
		t.Fatalf("item update not applied as expected")
	}

	cost := int64(42)
	err = UpdateVariant(ctx, db, VariantInput{
		ID:        "var1",
		ItemID:    "itm1",
		SKU:       "VSKU-NEW",
		Name:      "New Variant",
		Price:     500,
		CostPrice: &cost,
		IsActive:  true,
	})
	if err != nil {
		t.Fatalf("UpdateVariant error: %v", err)
	}
	var vname, vsku string
	var vprice int64
	var vcost sql.NullInt64
	_ = db.QueryRow(`SELECT name, sku, price, cost_price FROM item_variants WHERE id='var1'`).Scan(&vname, &vsku, &vprice, &vcost)
	if vname != "New Variant" || vsku != "VSKU-NEW" || vprice != 500 || !vcost.Valid || vcost.Int64 != 42 {
		t.Fatalf("variant update not applied as expected")
	}
}

func TestDeactivateItemAndVariants(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Apple", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var1", ItemID: "itm1", Name: "500ml", Price: 150, IsActive: true})

	if err := DeactivateItem(ctx, db, "itm1"); err != nil {
		t.Fatalf("DeactivateItem error: %v", err)
	}
	var active int
	_ = db.QueryRow(`SELECT is_active FROM items WHERE id='itm1'`).Scan(&active)
	if active != 0 {
		t.Fatalf("expected item inactive")
	}
	_ = db.QueryRow(`SELECT is_active FROM item_variants WHERE id='var1'`).Scan(&active)
	if active != 0 {
		t.Fatalf("expected variant inactive")
	}
}

func TestDeactivateVariant(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Apple", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var1", ItemID: "itm1", Name: "500ml", Price: 150, IsActive: true})
	if err := DeactivateVariant(ctx, db, "var1"); err != nil {
		t.Fatalf("DeactivateVariant error: %v", err)
	}
	var active int
	_ = db.QueryRow(`SELECT is_active FROM item_variants WHERE id='var1'`).Scan(&active)
	if active != 0 {
		t.Fatalf("expected variant inactive")
	}
}
