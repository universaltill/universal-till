package pos

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupPriceDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`CREATE TABLE items (id TEXT PRIMARY KEY, base_price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT, CHECK ((item_id IS NOT NULL AND variant_id IS NULL) OR (item_id IS NULL AND variant_id IS NOT NULL)));`,
		`PRAGMA foreign_keys = ON;`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestResolveCurrentPrice_ItemHistoryPreferred(t *testing.T) {
	ctx := context.Background()
	db := setupPriceDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO items(id, base_price, is_active) VALUES('itm1', 1000, 1)`)
	_, _ = db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph1','itm1',1500,datetime('now','-1 day'))`)

	price, err := ResolveCurrentPrice(ctx, db, "itm1", "")
	if err != nil {
		t.Fatalf("ResolveCurrentPrice error: %v", err)
	}
	if price != 1500 {
		t.Fatalf("expected history price 1500, got %d", price)
	}
}

func TestResolveCurrentPrice_FallbackToBase(t *testing.T) {
	ctx := context.Background()
	db := setupPriceDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO items(id, base_price, is_active) VALUES('itm1', 999, 1)`)

	price, err := ResolveCurrentPrice(ctx, db, "itm1", "")
	if err != nil {
		t.Fatalf("ResolveCurrentPrice error: %v", err)
	}
	if price != 999 {
		t.Fatalf("expected base price 999, got %d", price)
	}
}

func TestResolveCurrentPrice_FuturePriceNotActive(t *testing.T) {
	ctx := context.Background()
	db := setupPriceDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO items(id, base_price, is_active) VALUES('itm1', 1000, 1)`)
	future := time.Now().Add(time.Hour)
	_, _ = db.Exec(`INSERT INTO price_history(id, item_id, price, starts_at) VALUES('phf','itm1',2000,?)`, future)

	price, err := ResolveCurrentPrice(ctx, db, "itm1", "")
	if err != nil {
		t.Fatalf("ResolveCurrentPrice error: %v", err)
	}
	if price != 1000 {
		t.Fatalf("expected base price 1000 due to future price, got %d", price)
	}
}

func TestResolveCurrentPrice_VariantHistoryPreferred(t *testing.T) {
	ctx := context.Background()
	db := setupPriceDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO item_variants(id, item_id, price, is_active) VALUES('var1','itm1', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO price_history(id, variant_id, price, starts_at) VALUES('phv1','var1',800,datetime('now','-1 hour'))`)

	price, err := ResolveCurrentPrice(ctx, db, "", "var1")
	if err != nil {
		t.Fatalf("ResolveCurrentPrice error: %v", err)
	}
	if price != 800 {
		t.Fatalf("expected variant history price 800, got %d", price)
	}
}

func TestResolveCurrentPrice_InactiveErrors(t *testing.T) {
	ctx := context.Background()
	db := setupPriceDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO items(id, base_price, is_active) VALUES('itm1', 100, 0)`)
	if _, err := ResolveCurrentPrice(ctx, db, "itm1", ""); err == nil {
		t.Fatalf("expected error for inactive item")
	}
}
