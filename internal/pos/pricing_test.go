package pos

// These tests exercise POSRepo.ResolveCurrentPrice directly against a
// minimal price_history schema. They used to go through this package's
// pricing.go — a PricingRepo interface plus ResolveCurrentPrice /
// AppendPriceHistory* delegating wrappers and a test-only testPricingRepo
// that itself just called data.NewPOSRepo — none of which had a production
// caller (the live reader is POSRepo.ResolveCurrentPrice, called directly
// from internal/pages/ai_api.go and from POSRepo itself; the live writers
// are the ut-docs#2314 execer twins in internal/data/catalog_repo.go). The
// whole layer was removed by the ut-docs#1566 dead-code burn-down; the
// tests were kept and pointed at the repo they were always exercising.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
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

	repo := data.NewPOSRepo(db)
	price, err := repo.ResolveCurrentPrice(ctx, "itm1", "")
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

	repo := data.NewPOSRepo(db)
	price, err := repo.ResolveCurrentPrice(ctx, "itm1", "")
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

	repo := data.NewPOSRepo(db)
	price, err := repo.ResolveCurrentPrice(ctx, "itm1", "")
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

	repo := data.NewPOSRepo(db)
	price, err := repo.ResolveCurrentPrice(ctx, "", "var1")
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
	repo := data.NewPOSRepo(db)
	if _, err := repo.ResolveCurrentPrice(ctx, "itm1", ""); err == nil {
		t.Fatalf("expected error for inactive item")
	}
}

func TestResolveCurrentPrice_InvalidArgs(t *testing.T) {
	ctx := context.Background()
	db := setupPriceDB(t)
	defer db.Close()
	repo := data.NewPOSRepo(db)
	if _, err := repo.ResolveCurrentPrice(ctx, "", ""); err == nil {
		t.Fatalf("expected error when neither item nor variant provided")
	}
	if _, err := repo.ResolveCurrentPrice(ctx, "itm1", "var1"); err == nil {
		t.Fatalf("expected error when both item and variant provided")
	}
}

func TestResolveCurrentPrice_InactiveVariantErrors(t *testing.T) {
	ctx := context.Background()
	db := setupPriceDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO item_variants(id, item_id, price, is_active) VALUES('var1','itm1', 500, 0)`)
	repo := data.NewPOSRepo(db)
	if _, err := repo.ResolveCurrentPrice(ctx, "", "var1"); err == nil {
		t.Fatalf("expected error for inactive variant")
	}
}
