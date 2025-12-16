package testsupport

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// NewCatalogTestDB creates an in-memory SQLite database with minimal catalog tables.
func NewCatalogTestDB(t *testing.T) *sql.DB {
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
		`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, path TEXT NOT NULL, role TEXT DEFAULT 'thumbnail');`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT, CHECK ((item_id IS NOT NULL AND variant_id IS NULL) OR (item_id IS NULL AND variant_id IS NOT NULL)));`,
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE brands (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, name TEXT NOT NULL, rate_basis_points INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

type ItemSeed struct {
	ID        string
	SKU       string
	Name      string
	BasePrice int64
	TaxCodeID string
	IsActive  bool
}

// VariantSeed defines the inputs for seeding a variant.
type VariantSeed struct {
	ID       string
	ItemID   string
	SKU      string
	Name     string
	Price    int64
	Cost     *int64
	IsActive bool
}

// SeedItem inserts a minimal item row.
func SeedItem(t *testing.T, db *sql.DB, seed ItemSeed) {
	t.Helper()
	active := 0
	if seed.IsActive {
		active = 1
	}
	if _, err := db.Exec(`INSERT INTO items(id, sku, name, base_price, tax_code_id, is_active) VALUES(?,?,?,?,?,?)`,
		seed.ID, seed.SKU, seed.Name, seed.BasePrice, seed.TaxCodeID, active); err != nil {
		t.Fatalf("seed item: %v", err)
	}
}

// SeedTaxCode inserts a tax code.
func SeedTaxCode(t *testing.T, db *sql.DB, id, name string, rateBP int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES(?,?,?,1)`, id, name, rateBP); err != nil {
		t.Fatalf("seed tax code: %v", err)
	}
}

// SeedCategory inserts a category.
func SeedCategory(t *testing.T, db *sql.DB, id, name string, active bool) {
	t.Helper()
	a := 0
	if active {
		a = 1
	}
	if _, err := db.Exec(`INSERT INTO categories(id,name,is_active) VALUES(?,?,?)`, id, name, a); err != nil {
		t.Fatalf("seed category: %v", err)
	}
}

// SeedBrand inserts a brand.
func SeedBrand(t *testing.T, db *sql.DB, id, name string, active bool) {
	t.Helper()
	a := 0
	if active {
		a = 1
	}
	if _, err := db.Exec(`INSERT INTO brands(id,name,is_active) VALUES(?,?,?)`, id, name, a); err != nil {
		t.Fatalf("seed brand: %v", err)
	}
}

// SeedBarcode inserts a primary barcode for an item.
func SeedBarcode(t *testing.T, db *sql.DB, barcode, itemID string, primary bool) {
	t.Helper()
	p := 0
	if primary {
		p = 1
	}
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES(?,?,?)`, barcode, itemID, p); err != nil {
		t.Fatalf("seed barcode: %v", err)
	}
}

// SeedImage inserts a thumbnail image path.
func SeedImage(t *testing.T, db *sql.DB, id, itemID, path string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO item_images(id,item_id,path,role) VALUES(?,?,?, 'thumbnail')`, id, itemID, path); err != nil {
		t.Fatalf("seed image: %v", err)
	}
}

// SeedVariant inserts a variant row tied to an item.
func SeedVariant(t *testing.T, db *sql.DB, seed VariantSeed) {
	t.Helper()
	active := 0
	if seed.IsActive {
		active = 1
	}
	_, err := db.Exec(`INSERT INTO item_variants(id, item_id, sku, name, price, cost_price, is_active) VALUES(?,?,?,?,?,?,?)`,
		seed.ID, seed.ItemID, seed.SKU, seed.Name, seed.Price, seed.Cost, active)
	if err != nil {
		t.Fatalf("seed variant: %v", err)
	}
}
