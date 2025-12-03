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

func openPagesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pages_test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatalf("set busy_timeout: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	return db
}

func TestIndexAndBasketRender(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
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

	home := expectOK(http.MethodGet, "/")
	if !strings.Contains(home, "split-tender-form") {
		t.Fatalf("split tender form missing from home page")
	}
	if !strings.Contains(home, "split-tender-add") {
		t.Fatalf("split tender controls missing from home page")
	}
	if !strings.Contains(home, "split-tender-method") {
		t.Fatalf("split tender method select missing")
	}
	if !strings.Contains(home, `option value="cash"`) {
		t.Fatalf("expected cash payment method option")
	}
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
	// Apply promo barcode
	req = httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader("code=PROMO50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("promo scan failed: code %d body %s", rec.Code, rec.Body.String())
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

func TestPOSTenderSplitPayments(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 250, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	if _, err := engine.Scan("ABC"); err != nil {
		t.Fatalf("scan seed item: %v", err)
	}

	setStore := settings.NewStore(db)
	state := common.LoadState(t.Context(), setStore, &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}})
	pm, err := plugins.Init(t.Context(), &config.Config{}, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	dp := &common.Deps{
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: setStore,
	}

	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method":"cash","amount":150},{"method":"card","amount":150}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("split tender failed: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "receipt-payments") {
		t.Fatalf("receipt is missing payment breakdown: %s", body)
	}

	var paymentsCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&paymentsCount); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if paymentsCount != 2 {
		t.Fatalf("expected 2 payments, got %d", paymentsCount)
	}

	var receipt string
	if err := db.QueryRow(`SELECT receipt_no FROM sales LIMIT 1`).Scan(&receipt); err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	if receipt == "" {
		t.Fatalf("expected receipt number to be set")
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
		`CREATE TABLE promotions (code TEXT PRIMARY KEY, type TEXT NOT NULL, value INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT UNIQUE, name TEXT NOT NULL, description TEXT, category_id TEXT, brand_id TEXT, unit TEXT NOT NULL DEFAULT 'each', base_price INTEGER NOT NULL, tax_code_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, sku TEXT UNIQUE, name TEXT NOT NULL, price INTEGER NOT NULL, cost_price INTEGER, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, name TEXT NOT NULL, rate_basis_points INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE brands (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE stock_locations (id TEXT PRIMARY KEY, name TEXT NOT NULL);`,
		`CREATE TABLE registers (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT, display_name TEXT, role TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE customers (id TEXT PRIMARY KEY, name TEXT, loyalty_no TEXT, phone TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE payment_methods (id TEXT PRIMARY KEY, name TEXT, type TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, receipt_no TEXT NOT NULL UNIQUE, status TEXT NOT NULL, sale_type TEXT NOT NULL, register_id TEXT, cashier_id TEXT, customer_id TEXT, currency TEXT NOT NULL, subtotal INTEGER NOT NULL, discount_total INTEGER NOT NULL, tax_total INTEGER NOT NULL, total INTEGER NOT NULL, rounding INTEGER NOT NULL DEFAULT 0, note TEXT, created_at TEXT NOT NULL, completed_at TEXT, voided_at TEXT);`,
		`CREATE TABLE sale_lines (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_no INTEGER NOT NULL, item_id TEXT, variant_id TEXT, name_snapshot TEXT NOT NULL, sku_snapshot TEXT, barcode_snapshot TEXT, quantity REAL NOT NULL, unit_price INTEGER NOT NULL, line_discount INTEGER NOT NULL DEFAULT 0, tax_rate_bp INTEGER NOT NULL, tax_amount INTEGER NOT NULL, total_before_tax INTEGER NOT NULL, total_after_tax INTEGER NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_discounts (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_id TEXT, type TEXT NOT NULL, value INTEGER NOT NULL, amount INTEGER NOT NULL, reason TEXT);`,
		`CREATE TABLE payments (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, method_id TEXT NOT NULL, amount INTEGER NOT NULL, currency TEXT NOT NULL, reference TEXT, change_given INTEGER NOT NULL DEFAULT 0, paid_at TEXT NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_links (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, original_sale_id TEXT NOT NULL, reason TEXT);`,
		`CREATE TABLE stock_movements (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, sale_line_id TEXT, type TEXT NOT NULL, quantity REAL NOT NULL, created_at TEXT NOT NULL);`,
		`CREATE TABLE inventory (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, quantity REAL NOT NULL, updated_at TEXT NOT NULL, UNIQUE(item_id, variant_id, location_id));`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL);`,
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
	_, _ = db.Exec(`INSERT INTO promotions(code,type,value,description,is_active) VALUES('PROMO50','amount',50,'50p off',1)`)
	_, _ = db.Exec(`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tax_std','Standard',2000,1)`)
	_, _ = db.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm1','ABC','Apple',100,'tax_std',1)`)
	_, _ = db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('ABC','itm1',1)`)
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc_main','Main')`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id,item_id,variant_id,location_id,quantity,updated_at) VALUES('inv1','itm1',NULL,'loc_main',50,datetime('now'))`)
}
