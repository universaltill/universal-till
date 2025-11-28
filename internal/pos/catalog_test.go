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
