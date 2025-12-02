package pos

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func setupCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT UNIQUE, name TEXT NOT NULL, description TEXT, category_id TEXT, brand_id TEXT, unit TEXT NOT NULL DEFAULT 'each', base_price INTEGER NOT NULL, tax_code_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, sku TEXT UNIQUE, name TEXT NOT NULL, price INTEGER NOT NULL, cost_price INTEGER, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestAddBarcode_XORValidation(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()

	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "123"}); err == nil {
		t.Fatalf("expected xor validation error")
	}

	_, _ = db.Exec(`INSERT INTO items(id, name, base_price, is_active) VALUES('itm1','Apple',100,1)`)
	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "123", ItemID: "itm1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAddBarcode_InactiveItemFails(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO items(id, name, base_price, is_active) VALUES('itm1','Apple',100,0)`)
	if err := AddBarcode(ctx, db, BarcodeInput{Barcode: "123", ItemID: "itm1"}); err == nil {
		t.Fatalf("expected inactive item failure")
	}
}

func TestAddBarcode_CrossAssignmentBlocked(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO items(id, name, base_price, is_active) VALUES('itm1','Apple',100,1)`)
	_, _ = db.Exec(`INSERT INTO items(id, name, base_price, is_active) VALUES('itm2','Banana',100,1)`)
	_, _ = db.Exec(`INSERT INTO item_variants(id, item_id, name, price, is_active) VALUES('var1','itm1','500ml',150,1)`)

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
	_, _ = db.Exec(`INSERT INTO items(id, name, base_price, is_active) VALUES('itm1','Apple',100,1)`)

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
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple',100,1)`)
	_, _ = db.Exec(`INSERT INTO item_variants(id, item_id, sku, name, price, is_active) VALUES('var1','itm1','VSKU','Old',150,1)`)

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
	_, _ = db.Exec(`INSERT INTO items(id, name, base_price, is_active) VALUES('itm1','Apple',100,1)`)
	_, _ = db.Exec(`INSERT INTO item_variants(id, item_id, name, price, is_active) VALUES('var1','itm1','500ml',150,1)`)

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
	_, _ = db.Exec(`INSERT INTO items(id, name, base_price, is_active) VALUES('itm1','Apple',100,1)`)
	_, _ = db.Exec(`INSERT INTO item_variants(id, item_id, name, price, is_active) VALUES('var1','itm1','500ml',150,1)`)
	if err := DeactivateVariant(ctx, db, "var1"); err != nil {
		t.Fatalf("DeactivateVariant error: %v", err)
	}
	var active int
	_ = db.QueryRow(`SELECT is_active FROM item_variants WHERE id='var1'`).Scan(&active)
	if active != 0 {
		t.Fatalf("expected variant inactive")
	}
}
