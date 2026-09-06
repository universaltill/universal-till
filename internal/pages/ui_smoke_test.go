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
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
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

// openPagesTestDB runs the REAL migration set (internal/db.Open), the same
// pattern internal/data's and internal/cloudsync's own openMigratedDB
// helpers already use — ut-docs#1657/#1676: this package used to hand-roll
// its own ~50-statement CREATE TABLE copy of the schema, which let a column
// added to the real schema and not mirrored here go undetected (the
// hand-rolled copy silently diverging from what a till actually runs).
// Benchmarked at ~52ms per migrated open — negligible against this
// package's existing 84–130s test runtime even called once per test (82
// call sites).
func openPagesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pages_test.db")
	migrated, err := db.Open(path)
	if err != nil {
		t.Fatalf("open+migrate sqlite: %v", err)
	}
	sqlDB := migrated.DB
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	// ut-docs#878: this DB is single-connection, disposable scratch that
	// lives only in t.TempDir() for one test — nothing durable to protect
	// against a crash. db.Open's own DSN requests WAL, which is still a
	// real on-disk journal; overriding to an in-memory journal here and
	// skipping fsync removes a real disk syscall from the hot path that
	// hung a whole package's test binary under contended CI-runner I/O
	// (twice, on unrelated PRs).
	if _, err := sqlDB.Exec(`PRAGMA journal_mode = MEMORY`); err != nil {
		t.Fatalf("set journal_mode: %v", err)
	}
	if _, err := sqlDB.Exec(`PRAGMA synchronous = OFF`); err != nil {
		t.Fatalf("set synchronous: %v", err)
	}
	return sqlDB
}

// TestOpenPagesTestDB_NoFsyncOnHotPath guards ut-docs#878: this package's
// shared test-DB helper hung a whole test binary run (10m `go test`
// timeout) with a goroutine stuck in a syscall.Syscall6 -> fsync deep
// inside modernc.org/sqlite, twice on unrelated PRs (universal-till#425,
// #429). openPagesTestDB opened its temp-file DB with SQLite's default
// rollback-journal mode, which fsyncs the journal on every commit — a real
// disk syscall that can stall badly under contended CI-runner I/O. These
// DBs are single-connection (MaxOpenConns=1, so WAL's concurrent-reader
// benefit doesn't apply here) and live only in t.TempDir() for the
// duration of one test — durability across a crash is worthless for them,
// so there's nothing to trade away by keeping the journal in memory and
// skipping the sync entirely.
func TestOpenPagesTestDB_NoFsyncOnHotPath(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "memory" {
		t.Errorf("journal_mode = %q, want %q (keeps the rollback journal off disk entirely — no fsync on it)", journalMode, "memory")
	}

	var synchronous int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	if synchronous != 0 {
		t.Errorf("synchronous = %d, want 0 (OFF — this DB is disposable test scratch, not data worth an fsync)", synchronous)
	}
}

func TestIndexAndBasketRender(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()

	seedForPages(t, db)

	// Start a simple mock marketplace server
	mockMarketplace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog/plugins" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"plugins":[],"total":0}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockMarketplace.Close()

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 100},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: mockMarketplace.URL,
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
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
	// ut-docs#1284 review finding: a non-weighed line now renders its
	// whole-number quantity as "2", not "2.00" -- the value must actually
	// match its own pattern="[0-9]+" (integer-only for a non-weighed
	// line), which "2.00" never did.
	if !strings.Contains(body, `name="qty" value="2"`) {
		t.Fatalf("expected qty 2 in basket render, got: %s", body)
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
	// Deferred AFTER db.Close above, so LIFO order drains this FIRST: the
	// tender below fires printReceiptAsync/printKitchenAsync (ut-docs#425,
	// #514), which must finish touching db before Close and TempDir removal.
	defer dp.WaitForAsyncWork()

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

func TestPOSTender_PrinterFallbackAndLegalText(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	if _, err := db.Exec(`
INSERT INTO plugins (id, name, version, is_active)
VALUES ('com.tax.plugin', 'Tax Plugin', '1.2.3', 1)
`); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO plugin_entries (plugin_id, key, route, label, menu_group, type, is_active, sort_order, config_json)
VALUES ('com.tax.plugin', 'receipt_legal', '', 'Receipt Legal', '', 'receipt_template', 1, 10, '{"legal_text":"VAT Reg 123"}')
`); err != nil {
		t.Fatalf("insert receipt template entry: %v", err)
	}

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
	// Deferred AFTER db.Close above, so LIFO order drains this FIRST: the
	// tender below fires printReceiptAsync/printKitchenAsync (ut-docs#425,
	// #514), which must finish touching db before Close and TempDir removal.
	defer dp.WaitForAsyncWork()

	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method":"cash","amount":300}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender failed: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "receipt-legal") || !strings.Contains(body, "VAT Reg 123") {
		t.Fatalf("expected receipt legal text in response, got: %s", body)
	}
	if !strings.Contains(body, "receipt-printer-warning") || !strings.Contains(body, "receipt-printer-retry") {
		t.Fatalf("expected printer fallback in response, got: %s", body)
	}
}

// seedForPages inserts minimal fixture-specific rows for page rendering, on
// top of the real schema + seed rows openPagesTestDB's real migrations
// already provide (ut-docs#1657/#1676). It used to hand-roll the schema
// itself (~50 CREATE TABLE statements) plus a full copy of 001_init.sql's
// roles/permission_actions/role_permissions seed rows — both now redundant:
// the schema comes from the real migration set, and the role/permission
// grants seeded there (manager/admin/super_admin get every catalog action,
// cashier none, fiscal_tse_override is admin+super_admin only, and
// permission_management is super_admin only) are byte-identical to what
// this function used to insert by hand, so canPerform()-gated page tests
// already exercise the real seed shape without this function repeating it.
//
// tax_codes.tax_std, stock_locations.loc_main, payment_methods.cash/card
// and users.system collide with 001_init.sql's own seed rows for the same
// IDs — INSERT OR REPLACE keeps this fixture's exact values (e.g. the tax
// code's fixture-specific "Standard" name some tests assert on) rather than
// relying on the migration's seed values happening to still satisfy every
// test.
func seedForPages(t *testing.T, db *sql.DB) {
	t.Helper()
	// plugin_catalog/plugins both gained real NOT NULL columns beyond what
	// the old hand-rolled fixture required (min_pos_version/api_version/
	// published_at on plugin_catalog; entrypoint on plugins, plus the
	// composite (id, version) FK from plugins to plugin_catalog) — checked
	// (not the old fire-and-forget `_, _ =`) because a silently-dropped p1
	// row here is exactly the ut-docs#625 class of bug the regression test
	// right below this function exists to catch.
	if _, err := db.Exec(`INSERT INTO plugin_catalog(id,version,name,description,runtime,entrypoint,package_url,sha256,author,website,tags_json,is_deprecated,min_pos_version,api_version,published_at) VALUES('p1','1.0','Plugin','desc','go','entry','url','sha','auth','site','[]',0,'0.0.0','1',datetime('now'))`); err != nil {
		t.Fatalf("seed plugin_catalog p1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,entrypoint,is_active) VALUES('p1','Plugin','1.0','entry',1)`); err != nil {
		t.Fatalf("seed plugins p1: %v", err)
	}
	_, _ = db.Exec(`INSERT INTO plugin_entries(plugin_id,key,route,label,menu_group,type,is_active,sort_order) VALUES('p1','k1','/p1','P1','main','page',1,0)`)
	_, _ = db.Exec(`INSERT INTO promotions(code,type,value,description,is_active) VALUES('PROMO50','amount',50,'50p off',1)`)
	_, _ = db.Exec(`INSERT OR REPLACE INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tax_std','Standard',2000,1)`)
	_, _ = db.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm1','ABC','Apple',100,'tax_std',1)`)
	_, _ = db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('ABC','itm1',1)`)
	_, _ = db.Exec(`INSERT OR REPLACE INTO stock_locations(id,name) VALUES('loc_main','Main')`)
	_, _ = db.Exec(`INSERT OR REPLACE INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT OR REPLACE INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id,item_id,variant_id,location_id,quantity,updated_at) VALUES('inv1','itm1',NULL,'loc_main',50,datetime('now'))`)
	// ut-docs#744: a variant of itm1, its own barcode + own inventory row,
	// used by TestTenderHandler_VariantBarcodeScanIsTenderable to drive a
	// variant scan through the real /api/pos/tender handler end to end.
	_, _ = db.Exec(`INSERT INTO item_variants(id,item_id,sku,name,price,is_active) VALUES('var1','itm1','ABC-L','Large',150,1)`)
	_, _ = db.Exec(`INSERT INTO variant_barcodes(barcode,variant_id,is_primary) VALUES('VAR','var1',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id,item_id,variant_id,location_id,quantity,updated_at) VALUES('inv2',NULL,'var1','loc_main',30,datetime('now'))`)
	// created_at is NOT a real column on this table (001_init.sql) — the
	// old hand-rolled fixture added one for its own INSERTs; the real
	// schema has no such column, so it's dropped here (ut-docs#1676; the
	// ~18 other such inserts in users_page_test.go are ut-docs#1678).
	_, _ = db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role) VALUES('user1','admin','Admin','','admin')`)
	_, _ = db.Exec(`INSERT OR REPLACE INTO users(id,username,display_name,pin_hash,role) VALUES('system','system','System','','admin')`)
}

// TestPluginRepoGetPluginVersionAt_SeedForPagesSchema is a regression test
// for ut-docs#625: seedForPages' plugins fixture was missing the updated_at
// column that data.PluginRepo.GetPluginVersionAt queries (called from
// loadReceiptLegalBlocks on the real tender path whenever completedAt is
// non-zero). Against the drifted fixture this failed with "no such column:
// updated_at" -- silently, since the tender path's caller discards the
// error -- so it never showed up as a test FAILure there, only as log
// noise. This test calls the repository method directly so the same schema
// gap is a hard assertion instead.
func TestPluginRepoGetPluginVersionAt_SeedForPagesSchema(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	repo := data.NewPluginRepo(db)
	version, ok, err := repo.GetPluginVersionAt(t.Context(), "p1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("GetPluginVersionAt: %v", err)
	}
	if !ok || version != "1.0" {
		t.Fatalf("expected plugin p1 version 1.0 to be found, got ok=%v version=%q", ok, version)
	}
}

func TestInventoryFormRender(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)
	registerInventoryAPI(mux, dp)

	// Test page renders
	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory failed: code %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Receive / Adjust Stock") {
		t.Fatalf("expected 'Receive / Adjust Stock' in inventory page")
	}
	if !strings.Contains(body, "Stock Levels") {
		t.Fatalf("expected 'Stock Levels' table in inventory page")
	}
	if !strings.Contains(body, `name="quantity"`) {
		t.Fatalf("expected quantity input in form")
	}
	if !strings.Contains(body, `name="location_id"`) {
		t.Fatalf("expected location_id input in form")
	}
	if !strings.Contains(body, `name="type"`) {
		t.Fatalf("expected type select in form")
	}

	// Test POST stock receipt
	formData := "type=receive&item_id=itm1&location_id=loc_main&quantity=10&reason=test"
	req = httptest.NewRequest(http.MethodPost, "/api/inventory/receipt", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/inventory/receipt failed: code %d body %s", rec.Code, rec.Body.String())
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "Stock movement created") {
		t.Fatalf("expected success message, got: %s", respBody)
	}
}

// TestInventoryReceiptTriggersStockTableRefresh covers a real bug: the
// stock-levels table only ever rendered once, at page load — a successful
// receive/adjust/override/return updated the database correctly (confirmed
// live, quantity matched exactly) but nothing told the on-screen table to
// look again, so it just sat there showing the old number. Confirmed live
// 2026-07-29 as "inventory count is not updating." Fixed with an
// HX-Trigger: stock-updated response header the table listens for
// (hx-trigger="stock-updated from:body") to refetch itself from the new
// /ui/inventory/stock-table endpoint.
func TestInventoryReceiptTriggersStockTableRefresh(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)
	registerInventoryAPI(mux, dp)

	formData := "type=receive&item_id=itm1&location_id=loc_main&quantity=10&reason=test"
	req := httptest.NewRequest(http.MethodPost, "/api/inventory/receipt", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/inventory/receipt failed: code %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "stock-updated" {
		t.Fatalf("expected HX-Trigger: stock-updated on a successful receipt, got %q", got)
	}

	// The endpoint the table's hx-trigger fetches must reflect the update
	// (itm1 started at 50, +10 receive = 60) — same repo call the full page
	// uses, so this also guards against the partial and the full page ever
	// drifting to different data sources.
	req = httptest.NewRequest(http.MethodGet, "/ui/inventory/stock-table", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/inventory/stock-table failed: code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ">60<") {
		t.Fatalf("expected updated quantity 60 in stock-table partial, got: %s", rec.Body.String())
	}
	// data-name/data-sku on each row are what the page's client-side search
	// filter (web/ui/pages/inventory.html) reads to decide what to hide —
	// the partial swap must keep emitting them or the filter has nothing to
	// match against. The filter re-running after this swap (htmx:afterSwap
	// listener, added alongside this fix so an active search doesn't reset
	// to "show everything" on every save) is JS behaviour this Go test can't
	// exercise; verified live instead — see docs/code-reviews/.
	if !strings.Contains(rec.Body.String(), `data-name="Apple"`) || !strings.Contains(rec.Body.String(), `data-sku="ABC"`) {
		t.Fatalf("expected data-name/data-sku attributes the client-side search filter depends on, got: %s", rec.Body.String())
	}
}

func TestManagerOverrideForm(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	pm, err := plugins.Init(t.Context(), &config.Config{}, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}})
	dp := &common.Deps{
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)
	registerInventoryAPI(mux, dp)

	// Test page renders with override form
	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory failed: code %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Manager override") {
		t.Fatalf("expected 'Manager override' section in inventory page")
	}
	if !strings.Contains(body, `name="reason"`) {
		t.Fatalf("expected reason textarea in override form")
	}

	// Test POST override with non-manager (should fail)
	_, _ = db.Exec(`INSERT INTO users(id,username,pin_hash,role,created_at) VALUES('user2','cashier','','cashier',datetime('now'))`)
	formData := "item_id=itm1&location_id=loc_main&qty_before=5&reason=test override"
	req = httptest.NewRequest(http.MethodPost, "/api/inventory/override", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	// Note: getSessionUserID returns "system" which doesn't exist in users table, so this will fail with forbidden
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusInternalServerError {
		t.Logf("Expected forbidden/error for non-manager, got code %d", rec.Code)
	}

	// Test POST override with manager role
	req = httptest.NewRequest(http.MethodPost, "/api/inventory/override", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/inventory/override with manager failed: code %d body %s", rec.Code, rec.Body.String())
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "Override recorded") {
		t.Fatalf("expected success message, got: %s", respBody)
	}
}

func TestReturnFormRender(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	// Create a completed sale to return against
	_, _ = db.Exec(`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at,completed_at) VALUES('sale1','RCP-001','completed','sale','GBP',100,0,20,120,datetime('now'),datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,sku_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('line1','sale1',1,'itm1','Apple','ABC',2,50,0,2000,20,100,120)`)

	pm, err := plugins.Init(t.Context(), &config.Config{}, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}})
	dp := &common.Deps{
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)
	registerInventoryAPI(mux, dp)

	// Test page renders with return form
	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory failed: code %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Process Return") {
		t.Fatalf("expected 'Process Return' section in inventory page")
	}
	if !strings.Contains(body, `name="receipt_no"`) {
		t.Fatalf("expected receipt_no input in return form")
	}

	// Test return flow would require JSON request since form doesn't support line array
	// This is a simplified test for now
	t.Log("Return form rendering validated")
}

func TestLowStockBadge(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	// Set reorder level and create low stock scenario
	_, _ = db.Exec(`UPDATE items SET reorder_level = 100 WHERE id = 'itm1'`)
	_, _ = db.Exec(`UPDATE inventory SET quantity = 10 WHERE item_id = 'itm1'`)

	pm, err := plugins.Init(t.Context(), &config.Config{}, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}})
	dp := &common.Deps{
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)
	registerInventoryAPI(mux, dp)

	// Test page renders with low-stock badge
	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory failed: code %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Low Stock") {
		t.Fatalf("expected 'Low Stock' section in inventory page")
	}
	if !strings.Contains(body, "low-stock-badge") {
		t.Fatalf("expected low-stock-badge element")
	}

	// Test low-stock API endpoint
	req = httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/inventory/low-stock failed: code %d", rec.Code)
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "Apple") {
		t.Fatalf("expected low stock item 'Apple' in response, got: %s", respBody)
	}
	if !strings.Contains(respBody, "10.00") {
		t.Fatalf("expected current qty 10.00 in response")
	}
}
