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
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT UNIQUE, name TEXT NOT NULL, description TEXT, category_id TEXT, brand_id TEXT, unit TEXT NOT NULL DEFAULT 'each', base_price INTEGER NOT NULL, cost_price INTEGER, tax_code_id TEXT, lead_time_days INTEGER NOT NULL DEFAULT 0, reorder_level INTEGER NOT NULL DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0, is_sample_data INTEGER NOT NULL DEFAULT 0, stock_untracked INTEGER NOT NULL DEFAULT 0, color TEXT, updated_at TEXT);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, sku TEXT UNIQUE, name TEXT NOT NULL, price INTEGER NOT NULL, cost_price INTEGER, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, path TEXT NOT NULL, role TEXT DEFAULT 'thumbnail', sort_order INTEGER NOT NULL DEFAULT 0);`,
		// Mirrors migration 016 (ux_item_images_thumbnail_once, ut-docs#1871):
		// SetItemThumbnail/EnsureDefaultThumbnail's ON CONFLICT(item_id, role)
		// upserts need this constraint to actually exist, or SQLite rejects
		// the ON CONFLICT clause outright ("does not match any PRIMARY KEY
		// or UNIQUE constraint") rather than silently not enforcing it.
		`CREATE UNIQUE INDEX ux_item_images_thumbnail_once ON item_images (item_id, role);`,
		`CREATE TABLE shortcut_buttons (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, label TEXT NOT NULL, image_path TEXT, sort_order INTEGER NOT NULL DEFAULT 0);`,
		// No stock_locations table on purpose — TestCreateItem_SucceedsWithoutStockLocationsTable
		// guards CreateItem/CreateVariant's best-effort inventory-row creation
		// against exactly this schema shape. inventory itself is still here
		// (unqualified by a location) so read-only queries like ExportRows'
		// stock subquery don't fail on a missing table.
		`CREATE TABLE inventory (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, quantity REAL NOT NULL DEFAULT 0, reorder_level REAL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, receipt_no TEXT NOT NULL UNIQUE, status TEXT NOT NULL DEFAULT 'completed', subtotal INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE sale_lines (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_no INTEGER NOT NULL, item_id TEXT, variant_id TEXT, name_snapshot TEXT NOT NULL, quantity REAL NOT NULL DEFAULT 1, unit_price INTEGER NOT NULL DEFAULT 0, order_type TEXT NOT NULL DEFAULT '');`,
		`CREATE TABLE related_items (item_id TEXT NOT NULL, related_item_id TEXT NOT NULL, support INTEGER NOT NULL, score REAL NOT NULL, updated_at TEXT NOT NULL DEFAULT (datetime('now')), PRIMARY KEY (item_id, related_item_id));`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT, CHECK ((item_id IS NOT NULL AND variant_id IS NULL) OR (item_id IS NULL AND variant_id IS NOT NULL)));`,
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, parent_id TEXT, sort_order INTEGER NOT NULL DEFAULT 0, color TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE brands (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, name TEXT NOT NULL, rate_basis_points INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1, takeaway_rate_basis_points INTEGER);`,
		`CREATE TABLE item_modifier_groups (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, name TEXT NOT NULL, required INTEGER NOT NULL DEFAULT 0, min_select INTEGER NOT NULL DEFAULT 0, max_select INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_modifier_options (id TEXT PRIMARY KEY, group_id TEXT NOT NULL, name TEXT NOT NULL, price_delta_minor INTEGER NOT NULL DEFAULT 0, sort_order INTEGER NOT NULL DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
		// Mirrors migration 025 (ADR-0090, ut-docs#2013): which items use a
		// modifier group — ModifierRepo reads membership through this table,
		// so a fixture that inserts a group row directly must add its link
		// row too (SeedModifierGroup does both).
		`CREATE TABLE item_modifier_group_links (item_id TEXT NOT NULL, group_id TEXT NOT NULL, sort_order INTEGER NOT NULL DEFAULT 0, FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE, FOREIGN KEY (group_id) REFERENCES item_modifier_groups (id) ON DELETE CASCADE, PRIMARY KEY (item_id, group_id));`,
		// Mirrors migration 017 (ut-docs#1900): reusable option sets and the
		// links that make the variant generator idempotent.
		`CREATE TABLE option_sets (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE option_set_values (id TEXT PRIMARY KEY, option_set_id TEXT NOT NULL, value TEXT NOT NULL, sort_order INTEGER NOT NULL DEFAULT 0, FOREIGN KEY (option_set_id) REFERENCES option_sets (id) ON DELETE CASCADE, UNIQUE (option_set_id, value));`,
		`CREATE TABLE item_option_sets (item_id TEXT NOT NULL, option_set_id TEXT NOT NULL, axis_order INTEGER NOT NULL DEFAULT 0, FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE, FOREIGN KEY (option_set_id) REFERENCES option_sets (id) ON DELETE CASCADE, PRIMARY KEY (item_id, option_set_id));`,
		`CREATE TABLE item_variant_options (variant_id TEXT NOT NULL, option_set_value_id TEXT NOT NULL, FOREIGN KEY (variant_id) REFERENCES item_variants (id) ON DELETE CASCADE, FOREIGN KEY (option_set_value_id) REFERENCES option_set_values (id) ON DELETE CASCADE, PRIMARY KEY (variant_id, option_set_value_id));`,
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

// SeedModifierGroup inserts a modifier group row the way a pre-ADR-0090
// fixture used to (directly into item_modifier_groups) PLUS the
// item_modifier_group_links row migration 025 backfills for it — the shape
// every ModifierRepo read path now expects (membership is read through the
// link table, sort_order off the link row).
func SeedModifierGroup(t *testing.T, db *sql.DB, id, itemID, name string, required bool, minSelect, maxSelect, sortOrder int, active bool) {
	t.Helper()
	req, act := 0, 0
	if required {
		req = 1
	}
	if active {
		act = 1
	}
	if _, err := db.Exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order, is_active) VALUES (?,?,?,?,?,?,?,?)`,
		id, itemID, name, req, minSelect, maxSelect, sortOrder, act); err != nil {
		t.Fatalf("seed modifier group: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES (?,?,?)`, itemID, id, sortOrder); err != nil {
		t.Fatalf("seed modifier group link: %v", err)
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

// SeedCategoryTree inserts a category with an optional parent, sort order,
// and display color — for tests that exercise the nested/color-coded
// sale-screen category grid (SeedCategory above stays parent-less/colorless
// for existing simple-lookup callers).
func SeedCategoryTree(t *testing.T, db *sql.DB, id, name, parentID string, sortOrder int, color string) {
	t.Helper()
	var parent, col any
	if parentID != "" {
		parent = parentID
	}
	if color != "" {
		col = color
	}
	if _, err := db.Exec(`INSERT INTO categories(id,name,parent_id,sort_order,color,is_active) VALUES(?,?,?,?,?,1)`,
		id, name, parent, sortOrder, col); err != nil {
		t.Fatalf("seed category tree: %v", err)
	}
}

// SeedInactiveCategoryTree is SeedCategoryTree but is_active=0 — for tests
// that need a DEACTIVATED parent with an otherwise-active child (ut-docs#2140:
// a category can be deactivated while an active child still points at it,
// since SetCategoryActive only blocks on DIRECT items, not descendants).
func SeedInactiveCategoryTree(t *testing.T, db *sql.DB, id, name, parentID string, sortOrder int, color string) {
	t.Helper()
	var parent, col any
	if parentID != "" {
		parent = parentID
	}
	if color != "" {
		col = color
	}
	if _, err := db.Exec(`INSERT INTO categories(id,name,parent_id,sort_order,color,is_active) VALUES(?,?,?,?,?,0)`,
		id, name, parent, sortOrder, col); err != nil {
		t.Fatalf("seed inactive category tree: %v", err)
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

// SeedCompletedSaleVariant inserts a completed sale whose lines reference
// variants (not items) directly — the shape a variant checkout persists
// (sale_lines.variant_id set, item_id NULL, per the 001_init.sql CHECK
// constraint) — for tests exercising queries that must resolve a variant
// line back to its parent item (e.g. related-items co-occurrence,
// ut-docs#752).
func SeedCompletedSaleVariant(t *testing.T, db *sql.DB, saleID string, variantIDs ...string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sales(id, receipt_no, status) VALUES(?,?, 'completed')`, saleID, "R-"+saleID); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	for i, variantID := range variantIDs {
		if _, err := db.Exec(`INSERT INTO sale_lines(id, sale_id, line_no, variant_id, name_snapshot) VALUES(?,?,?,?,?)`,
			saleID+"-l"+string(rune('a'+i)), saleID, i+1, variantID, variantID); err != nil {
			t.Fatalf("seed variant sale line: %v", err)
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
