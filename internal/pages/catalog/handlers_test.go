package catalog

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func setupCatalogPageDB(t *testing.T) *sql.DB {
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
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT, CHECK ((item_id IS NOT NULL AND variant_id IS NULL) OR (item_id IS NULL AND variant_id IS NOT NULL)));`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestCatalogPage_FiltersInactive(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('active1','A1','Active Item',100,1)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('inactive1','I1','Inactive Item',200,0)`)

	mux := http.NewServeMux()
	Register(mux, db, "default", nil)

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Active Item") {
		t.Fatalf("expected active item in response")
	}
	if strings.Contains(body, "Inactive Item") {
		t.Fatalf("expected inactive item to be filtered out")
	}
}

func TestCatalogCreateAndDeactivate(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	mux := http.NewServeMux()
	Register(mux, db, "default", nil)

	// create item
	form := strings.NewReader("name=Test+Item&price=123&sku=T1&taxCode=tax_std&isActive=1")
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on create, got %d: %s", rec.Code, rec.Body.String())
	}
	// deactivate
	deact := strings.NewReader("id=" + extractItemID(t, db))
	req = httptest.NewRequest(http.MethodPost, "/api/catalog/item/deactivate", deact)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on deactivate, got %d: %s", rec.Code, rec.Body.String())
	}
}

func extractItemID(t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM items LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("extract id: %v", err)
	}
	return id
}

// chdirToRepoRoot makes template paths resolve correctly in tests.
func chdirToRepoRoot(t *testing.T) {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
}
