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
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT UNIQUE, name TEXT NOT NULL, description TEXT, category_id TEXT, brand_id TEXT, unit TEXT NOT NULL DEFAULT 'each', base_price INTEGER NOT NULL, cost_price INTEGER, tax_code_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0, updated_at TEXT);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, sku TEXT UNIQUE, name TEXT NOT NULL, price INTEGER NOT NULL, cost_price INTEGER, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, path TEXT NOT NULL, role TEXT DEFAULT 'thumbnail', sort_order INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE shortcut_buttons (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, label TEXT NOT NULL, image_path TEXT, sort_order INTEGER NOT NULL DEFAULT 0);`,
		// No stock_locations table on purpose — TestCreateItem_SucceedsWithoutStockLocationsTable
		// guards CreateItem/CreateVariant's best-effort inventory-row creation
		// against exactly this schema shape. inventory itself is still here
		// (unqualified by a location) so read-only queries like ExportRows'
		// stock subquery don't fail on a missing table.
		`CREATE TABLE inventory (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, quantity REAL NOT NULL DEFAULT 0, reorder_level REAL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, receipt_no TEXT NOT NULL UNIQUE, status TEXT NOT NULL DEFAULT 'completed', subtotal INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE sale_lines (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_no INTEGER NOT NULL, item_id TEXT, name_snapshot TEXT NOT NULL, quantity REAL NOT NULL DEFAULT 1, unit_price INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE related_items (item_id TEXT NOT NULL, related_item_id TEXT NOT NULL, support INTEGER NOT NULL, score REAL NOT NULL, updated_at TEXT NOT NULL DEFAULT (datetime('now')), PRIMARY KEY (item_id, related_item_id));`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT, CHECK ((item_id IS NOT NULL AND variant_id IS NULL) OR (item_id IS NULL AND variant_id IS NOT NULL)));`,
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE brands (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, name TEXT NOT NULL, rate_basis_points INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1, takeaway_rate_basis_points INTEGER);`,
		`CREATE TABLE item_modifier_groups (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, name TEXT NOT NULL, required INTEGER NOT NULL DEFAULT 0, min_select INTEGER NOT NULL DEFAULT 0, max_select INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_modifier_options (id TEXT PRIMARY KEY, group_id TEXT NOT NULL, name TEXT NOT NULL, price_delta_minor INTEGER NOT NULL DEFAULT 0, sort_order INTEGER NOT NULL DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
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

// SeedVariantBarcode attaches a barcode to a variant.
func SeedVariantBarcode(t *testing.T, db *sql.DB, barcode, variantID string, primary bool) {
	t.Helper()
	p := 0
	if primary {
		p = 1
	}
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode,variant_id,is_primary) VALUES(?,?,?)`, barcode, variantID, p); err != nil {
		t.Fatalf("seed variant barcode: %v", err)
	}
}

// SeedImage inserts a thumbnail image path.
func SeedImage(t *testing.T, db *sql.DB, id, itemID, path string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO item_images(id,item_id,path,role) VALUES(?,?,?, 'thumbnail')`, id, itemID, path); err != nil {
		t.Fatalf("seed image: %v", err)
	}
}

// SeedCompletedSale inserts a completed sale whose lines are the given item
// ids — the minimal shape the related-items co-occurrence rebuild reads.
func SeedCompletedSale(t *testing.T, db *sql.DB, saleID string, itemIDs ...string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sales(id, receipt_no, status) VALUES(?,?, 'completed')`, saleID, "R-"+saleID); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	for i, itemID := range itemIDs {
		if _, err := db.Exec(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot) VALUES(?,?,?,?,?)`,
			saleID+"-l"+string(rune('a'+i)), saleID, i+1, itemID, itemID); err != nil {
			t.Fatalf("seed sale line: %v", err)
		}
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
