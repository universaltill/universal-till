package pos

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	_ "modernc.org/sqlite"
)

func setupCatalogSearchDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT UNIQUE, name TEXT NOT NULL, description TEXT, category_id TEXT, brand_id TEXT, unit TEXT NOT NULL DEFAULT 'each', base_price INTEGER NOT NULL, tax_code_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, sku TEXT UNIQUE, name TEXT NOT NULL, price INTEGER NOT NULL, cost_price INTEGER, is_active INTEGER NOT NULL DEFAULT 1);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestSearchActiveItems_FiltersInactive(t *testing.T) {
	db := setupCatalogSearchDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('a1','SKU1','Active',100,1)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','SKU2','Inactive',200,0)`)
	_, _ = db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('111','a1',1)`) // ensure barcode search works

	repo := data.NewPOSRepo(db)
	cs := NewCatalogSearcher(repo)
	results, err := cs.SearchActiveItems(ctx, "SKU", 0, 10)
	if err != nil {
		t.Fatalf("SearchActiveItems error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "a1" {
		t.Fatalf("expected only active item, got %+v", results)
	}
	results, err = cs.SearchActiveItems(ctx, "111", 0, 10)
	if err != nil || len(results) != 1 || results[0].ID != "a1" {
		t.Fatalf("barcode search failed: %+v (err=%v)", results, err)
	}
}

// TestSearchActiveItems_NullSKUDoesNotError is ut-docs#1176's regression:
// an item with no real SKU now stores sku = NULL (not its own UUID, see
// CatalogRepo.CreateItem), so this query must tolerate a NULL sku column —
// a bare `i.sku` scanned into a non-nullable string field would error on
// every such row. It must also come back as "", never a UUID.
func TestSearchActiveItems_NullSKUDoesNotError(t *testing.T) {
	db := setupCatalogSearchDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('noSku1', NULL, 'No SKU Item', 150, 1)`)

	repo := data.NewPOSRepo(db)
	cs := NewCatalogSearcher(repo)
	results, err := cs.SearchActiveItems(ctx, "No SKU", 0, 10)
	if err != nil {
		t.Fatalf("SearchActiveItems must tolerate a NULL sku column, got: %v", err)
	}
	if len(results) != 1 || results[0].ID != "noSku1" {
		t.Fatalf("expected the no-SKU item, got %+v", results)
	}
	if results[0].SKU != "" {
		t.Fatalf("expected empty SKU for an item with no real SKU, got %q", results[0].SKU)
	}
}

func TestLookupActiveVariant(t *testing.T) {
	db := setupCatalogSearchDB(t)
	defer db.Close()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO item_variants(id,item_id,sku,name,price,is_active) VALUES('v1','i1','VSKU','Var',500,1)`)
	_, _ = db.Exec(`INSERT INTO item_variants(id,item_id,sku,name,price,is_active) VALUES('v2','i1','VSK2','InactiveVar',500,0)`)

	cs := NewCatalogSearcher(data.NewPOSRepo(db))
	v, err := cs.LookupActiveVariant(ctx, "v1")
	if err != nil || v.ID != "v1" {
		t.Fatalf("expected variant v1, got %v err=%v", v, err)
	}
	if _, err := cs.LookupActiveVariant(ctx, "v2"); err == nil {
		t.Fatalf("expected error for inactive variant")
	}
	if _, err := cs.LookupActiveVariant(ctx, "missing"); err == nil {
		t.Fatalf("expected error for missing variant")
	}
}
