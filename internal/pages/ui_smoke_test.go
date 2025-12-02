package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	_ "modernc.org/sqlite"
)

type stubResolver map[string]pos.BasketLine

func (s stubResolver) Resolve(code string) (pos.BasketLine, bool) {
	if line, ok := s[code]; ok {
		return line, true
	}
	return pos.BasketLine{}, false
}

// chdir to repo root so templates resolve during tests.
func chdirRoot(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
}

func TestIndexAndBasketRender(t *testing.T) {
	chdirRoot(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	seedForPages(t, db)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 100},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	pm, err := plugins.Init(t.Context(), &config.Config{}, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}})
	dp := &common.Deps{
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerBasket(mux, dp)
	registerPOSAPI(mux, dp)
	registerSettings(mux, dp)
	registerPluginsPage(mux, dp)
	catalog.Register(mux, dp)

	expectOK := func(method, path string) string {
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s failed: code %d body %s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	expectOK(http.MethodGet, "/")
	expectOK(http.MethodGet, "/ui/basket")
	expectOK(http.MethodGet, "/settings")
	expectOK(http.MethodGet, "/plugins")
	expectOK(http.MethodGet, "/catalog")

	// Scan item into basket
	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader("code=ABC&qty=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan render failed: code %d body %s", rec.Code, rec.Body.String())
	}
	// Update qty to 2 and ensure rendered qty sticks
	req = httptest.NewRequest(http.MethodPost, "/api/pos/line", strings.NewReader("code=ABC&qty=2&discount=0"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update line failed: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="2.00"`) {
		t.Fatalf("expected qty 2.00 in basket render, got: %s", body)
	}
}

// seedForPages creates minimal tables/rows for page rendering.
func seedForPages(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT);`,
		`CREATE TABLE plugin_catalog (id TEXT PRIMARY KEY, version TEXT, name TEXT, description TEXT, runtime TEXT, entrypoint TEXT, package_url TEXT, sha256 TEXT, author TEXT, website TEXT, tags_json TEXT, is_deprecated INTEGER DEFAULT 0);`,
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, is_active INTEGER DEFAULT 1);`,
		`CREATE TABLE plugin_entries (plugin_id TEXT, key TEXT, route TEXT, label TEXT, menu_group TEXT, type TEXT, is_active INTEGER DEFAULT 1, sort_order INTEGER DEFAULT 0);`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT UNIQUE, name TEXT NOT NULL, description TEXT, category_id TEXT, brand_id TEXT, unit TEXT NOT NULL DEFAULT 'each', base_price INTEGER NOT NULL, tax_code_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, sku TEXT UNIQUE, name TEXT NOT NULL, price INTEGER NOT NULL, cost_price INTEGER, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, name TEXT NOT NULL, rate_basis_points INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE brands (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed ddl failed: %v", err)
		}
	}
	// minimal data
	_, _ = db.Exec(`INSERT INTO plugin_catalog(id,version,name,description,runtime,entrypoint,package_url,sha256,author,website,tags_json,is_deprecated) VALUES('p1','1.0','Plugin','desc','go','entry','url','sha','auth','site','[]',0)`)
	_, _ = db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('p1','Plugin','1.0',1)`)
	_, _ = db.Exec(`INSERT INTO plugin_entries(plugin_id,key,route,label,menu_group,type,is_active,sort_order) VALUES('p1','k1','/p1','P1','main','page',1,0)`)
	_, _ = db.Exec(`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tax_std','Standard',2000,1)`)
	_, _ = db.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm1','ABC','Apple',100,'tax_std',1)`)
	_, _ = db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('ABC','itm1',1)`)
}
