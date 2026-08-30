package data_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/data"

	_ "modernc.org/sqlite"
)

// ut-docs#512: the speedy kasse Products table can carry two tax-rate
// columns (TaxPercentage = dine-in, TaxPercentage2 = takeaway) — this is
// the actual real-world source ut-docs#512's own issue text describes
// (a real café's catalog conversion). ReadBkpProducts must surface them
// when present, and must not error when an older backup.db lacks them
// entirely (nobody on this project has seen every real export, per this
// file's own existing bkpScanString doc comment).
func openTempSQLite(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp("", "bkp-products-*.db")
	if err != nil {
		t.Fatalf("create temp db file: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(path) })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestReadBkpProducts_TaxColumnsPresent(t *testing.T) {
	db := openTempSQLite(t)
	if _, err := db.Exec(`CREATE TABLE Products (
		ProductNumber TEXT, ProductTextShort TEXT, SalesPrice REAL,
		ProductGroupText TEXT, Status INTEGER, ProductType INTEGER,
		TaxPercentage REAL, TaxPercentage2 REAL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO Products
		(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType, TaxPercentage, TaxPercentage2)
		VALUES ('1','Cappuccino',3.50,'Coffee',1,1,19,7)`); err != nil {
		t.Fatal(err)
	}

	rows, err := data.ReadBkpProducts(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadBkpProducts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].TaxPercentageRaw != "19" || rows[0].TaxPercentage2Raw != "7" {
		t.Errorf("tax columns = (%q,%q), want (19,7)", rows[0].TaxPercentageRaw, rows[0].TaxPercentage2Raw)
	}
}

func TestReadBkpProducts_MissingTaxColumnsIsBackwardCompatible(t *testing.T) {
	db := openTempSQLite(t)
	// Older-schema backup.db: no TaxPercentage/TaxPercentage2 columns at
	// all — must not error, must just leave the tax fields blank.
	if _, err := db.Exec(`CREATE TABLE Products (
		ProductNumber TEXT, ProductTextShort TEXT, SalesPrice REAL,
		ProductGroupText TEXT, Status INTEGER, ProductType INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO Products
		(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType)
		VALUES ('1','Espresso',2.20,'Coffee',1,1)`); err != nil {
		t.Fatal(err)
	}

	rows, err := data.ReadBkpProducts(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadBkpProducts must not error on a tax-columnless schema: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].TaxPercentageRaw != "" || rows[0].TaxPercentage2Raw != "" {
		t.Errorf("tax columns should be blank on an older schema, got (%q,%q)", rows[0].TaxPercentageRaw, rows[0].TaxPercentage2Raw)
	}
	if rows[0].ProductTextShort != "Espresso" {
		t.Errorf("non-tax columns must still read correctly, got %+v", rows[0])
	}
}

// ut-docs#1223: the speedy kasse Products table can carry a
// ProductImagePath column referencing a photo inside the .bkp archive's
// documents.zip — ReadBkpProducts must surface it, and must not error when
// an older backup.db lacks the column entirely (same optional-column
// pattern as the tax columns above).
func TestReadBkpProducts_ProductImagePathPresent(t *testing.T) {
	db := openTempSQLite(t)
	if _, err := db.Exec(`CREATE TABLE Products (
		ProductNumber TEXT, ProductTextShort TEXT, SalesPrice REAL,
		ProductGroupText TEXT, Status INTEGER, ProductType INTEGER,
		ProductImagePath TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO Products
		(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType, ProductImagePath)
		VALUES ('1','Cappuccino',3.50,'Coffee',1,1,'images/abc-123.jpg')`); err != nil {
		t.Fatal(err)
	}

	rows, err := data.ReadBkpProducts(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadBkpProducts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].ProductImagePath != "images/abc-123.jpg" {
		t.Errorf("ProductImagePath = %q, want %q", rows[0].ProductImagePath, "images/abc-123.jpg")
	}
}

func TestReadBkpProducts_MissingProductImagePathIsBackwardCompatible(t *testing.T) {
	db := openTempSQLite(t)
	// Older-schema backup.db: no ProductImagePath column at all — must not
	// error, must just leave the field blank.
	if _, err := db.Exec(`CREATE TABLE Products (
		ProductNumber TEXT, ProductTextShort TEXT, SalesPrice REAL,
		ProductGroupText TEXT, Status INTEGER, ProductType INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO Products
		(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType)
		VALUES ('1','Espresso',2.20,'Coffee',1,1)`); err != nil {
		t.Fatal(err)
	}

	rows, err := data.ReadBkpProducts(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadBkpProducts must not error on an image-path-columnless schema: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].ProductImagePath != "" {
		t.Errorf("ProductImagePath should be blank on an older schema, got %q", rows[0].ProductImagePath)
	}
}

// ut-docs#968 (independent review): buildBkpProductsQuery introspects the
// category DISPLAY column on ProductGroups, but the join predicate it emits
// is "ON g.ProductGroupID = p.ProductGroupID" — so a ProductGroups table
// that carries the category text under a differently-named key column made
// the whole query fail with "no such column: g.ProductGroupID", killing the
// entire import instead of degrading to an empty category.
//
// That is precisely the failure mode this card exists to eliminate: the
// file's own contract is "every column is optional, and a backup missing
// any of them reads narrower rather than failing". Introspecting the join
// key on both sides is what makes that contract true.
func TestReadBkpProducts_ProductGroupsWithoutJoinKeyStillImports(t *testing.T) {
	db := openTempSQLite(t)
	if _, err := db.Exec(`CREATE TABLE Products (
		ProductNumber TEXT, ProductTextShort TEXT, ProductGroupID INTEGER,
		SalesPrice REAL, Status INTEGER, ProductType INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	// Carries the category text, but its key column is NOT ProductGroupID.
	if _, err := db.Exec(`CREATE TABLE ProductGroups (
		GroupID INTEGER PRIMARY KEY, ProductGroupText TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ProductGroups (GroupID, ProductGroupText) VALUES (1,'Kaffee')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO Products
		(ProductNumber, ProductTextShort, ProductGroupID, SalesPrice, Status, ProductType)
		VALUES ('30033','Latte Macchiato',1,5.00,0,0)`); err != nil {
		t.Fatal(err)
	}

	rows, err := data.ReadBkpProducts(context.Background(), db)
	if err != nil {
		t.Fatalf("an unjoinable ProductGroups must degrade to no category, not fail the import: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].ProductTextShort != "Latte Macchiato" || rows[0].SalesPriceRaw != "5" {
		t.Errorf("the product itself must still read correctly, got %+v", rows[0])
	}
	if rows[0].ProductGroupText != "" {
		t.Errorf("category = %q, want empty — the join key is absent so there is nothing to join on", rows[0].ProductGroupText)
	}
}
