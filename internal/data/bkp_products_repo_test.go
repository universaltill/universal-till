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
