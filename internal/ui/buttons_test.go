package ui

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT, name TEXT, base_price INTEGER NOT NULL, tax_code_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, role TEXT NOT NULL, path TEXT NOT NULL);`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, is_primary INTEGER DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, is_primary INTEGER DEFAULT 0);`,
		`CREATE TABLE shortcut_buttons (barcode TEXT PRIMARY KEY, label TEXT, item_id TEXT, image_path TEXT, sort_order INTEGER NOT NULL DEFAULT 0);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, rate_basis_points INTEGER NOT NULL, takeaway_rate_basis_points INTEGER);`); err != nil {
		t.Fatalf("setup tax_codes failed: %v", err)
	}
	return db
}

func TestPriceResolverAdapter_FallbackSkuSearch(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','ABC123','Apple Juice', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('ABC123','itm1',1)`)
	store := NewButtonStore(db)
	resolver := PriceResolverAdapter{Store: store}

	line, ok := resolver.Resolve("ABC123")
	if !ok {
		t.Fatalf("expected fallback SKU resolve to succeed")
	}
	if line.PriceCents != 500 || line.Name != "Apple Juice" {
		t.Fatalf("unexpected line %+v", line)
	}
}

func TestPriceResolverAdapter_FallbackNameSearch(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Orange Soda', 250, 1)`)
	_, _ = db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('SKU1','itm1',1)`)
	_, _ = db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph1','itm1',300, datetime('now'))`)

	store := NewButtonStore(db)
	resolver := PriceResolverAdapter{Store: store}

	line, ok := resolver.Resolve("Orange")
	if !ok {
		t.Fatalf("expected fallback name resolve to succeed")
	}
	if line.PriceCents != 300 {
		t.Fatalf("expected history price 300, got %d", line.PriceCents)
	}
	if line.Name != "Orange Soda" {
		t.Fatalf("unexpected name %s", line.Name)
	}
}

func TestButtonStoreAdd_ValidatesActiveItem(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewButtonStore(db)
	if err := store.Add(Button{Label: "Btn", Code: "B1", ItemID: "missing"}); err == nil {
		t.Fatalf("expected error for missing item")
	}
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Orange Soda', 250, 1)`)
	if err := store.Add(Button{Label: "Btn", Code: "B1", ItemID: "itm1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestPriceResolverAdapter_TaxCodeID verifies the resolver reads an item's
// tax_code_id through to BasketLine — a tax plugin (internal/pos.
// TaxRateAsker) uses it to tell item categories apart without core
// interpreting it itself.
func TestPriceResolverAdapter_TaxCodeID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO tax_codes(id, rate_basis_points) VALUES('tax-drink', 1900)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, tax_code_id, is_active) VALUES('itm1','DRINK1','Coffee', 1000, 'tax-drink', 1)`)
	_, _ = db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('DRINK1','itm1',1)`)
	store := NewButtonStore(db)
	resolver := PriceResolverAdapter{Store: store}

	line, ok := resolver.Resolve("DRINK1")
	if !ok {
		t.Fatalf("expected resolve to succeed")
	}
	if line.TaxRateBP != 1900 {
		t.Fatalf("expected TaxRateBP 1900, got %d", line.TaxRateBP)
	}
	if line.TaxCodeID != "tax-drink" {
		t.Fatalf("expected TaxCodeID tax-drink, got %q", line.TaxCodeID)
	}
}
