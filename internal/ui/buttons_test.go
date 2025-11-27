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
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT, name TEXT, base_price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, role TEXT NOT NULL, path TEXT NOT NULL);`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestPriceResolverAdapter_FallbackSkuSearch(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','ABC123','Apple Juice', 500, 1)`)
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
