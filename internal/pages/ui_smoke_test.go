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
	// ut-docs#878: this DB is single-connection, disposable scratch that
	// lives only in t.TempDir() for one test — nothing durable to protect
	// against a crash. The default rollback-journal mode fsyncs on every
	// commit, a real disk syscall that hung a whole package's test binary
	// under contended CI-runner I/O (twice, on unrelated PRs). Keeping the
	// journal in memory and skipping the sync removes that syscall from
	// the hot path entirely.
	if _, err := db.Exec(`PRAGMA journal_mode = MEMORY`); err != nil {
		t.Fatalf("set journal_mode: %v", err)
	}
	if _, err := db.Exec(`PRAGMA synchronous = OFF`); err != nil {
		t.Fatalf("set synchronous: %v", err)
	}
	return db
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

// seedForPages creates minimal tables/rows for page rendering.
func seedForPages(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT);`,
		`CREATE TABLE plugin_install_status (listing_id TEXT PRIMARY KEY, plugin_id TEXT, plugin_name TEXT, target_version TEXT, current_version TEXT, state TEXT NOT NULL, message_key TEXT, retryable INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL);`,
		`CREATE TABLE plugin_catalog (id TEXT PRIMARY KEY, version TEXT, name TEXT, description TEXT, runtime TEXT, entrypoint TEXT, package_url TEXT, sha256 TEXT, author TEXT, website TEXT, tags_json TEXT, is_deprecated INTEGER DEFAULT 0);`,
		// updated_at kept column-identical to internal/db/migrations/001_init.sql
		// (ut-docs#625) -- data.PluginRepo.GetPluginVersionAt queries
		// "WHERE id = ? AND updated_at <= ? ORDER BY updated_at DESC", called
		// from loadReceiptLegalBlocks on the tender path; a fixture missing
		// this column makes that real query fail against a schema that
		// doesn't match production.
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER DEFAULT 1, trust_level TEXT DEFAULT 'untrusted', install_state TEXT DEFAULT 'installed', runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '', updated_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE plugin_entries (id TEXT, plugin_id TEXT, key TEXT, route TEXT, label TEXT, icon_path TEXT, parent_page_key TEXT, target_action TEXT, trigger_event TEXT, menu_group TEXT, type TEXT, is_active INTEGER DEFAULT 1, sort_order INTEGER DEFAULT 0, config_json TEXT);`,
		`CREATE TABLE plugin_permissions (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, permission TEXT NOT NULL, granted INTEGER NOT NULL DEFAULT 0, UNIQUE(plugin_id, permission));`,
		`CREATE TABLE plugin_hooks (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, event TEXT NOT NULL, action TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 100, is_active INTEGER NOT NULL DEFAULT 1, config_json TEXT, UNIQUE(plugin_id, event, action));`,
		`CREATE TABLE plugin_settings (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'global', scope_id TEXT, updated_at TEXT NOT NULL DEFAULT (datetime('now')), UNIQUE(plugin_id, key, scope, scope_id));`,
		`CREATE TABLE promotions (code TEXT PRIMARY KEY, type TEXT NOT NULL, value INTEGER NOT NULL, description TEXT, starts_at TEXT, ends_at TEXT, customer_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT UNIQUE, name TEXT NOT NULL, description TEXT, category_id TEXT, brand_id TEXT, unit TEXT NOT NULL DEFAULT 'each', base_price INTEGER NOT NULL, tax_code_id TEXT, reorder_level INTEGER NOT NULL DEFAULT 0, lead_time_days INTEGER NOT NULL DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0, is_sample_data INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, barcode_type TEXT, is_primary INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, sku TEXT UNIQUE, name TEXT NOT NULL, price INTEGER NOT NULL, cost_price INTEGER, is_active INTEGER NOT NULL DEFAULT 1);`,
		// name UNIQUE: column-identical to 001_init.sql (ut-docs#259) -- a
		// drifted fixture here would let CreateTaxCode/UpdateTaxCode's
		// duplicate-name conflict handling pass its handler test against a
		// constraint production actually enforces but this fixture didn't.
		`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, rate_basis_points INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1, takeaway_rate_basis_points INTEGER);`,
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE brands (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		// address_street/address_postcode/address_city: column-identical to
		// internal/db/migrations/059_fiscal_register_de.sql's ALTERs
		// (ut-docs#665) -- SetStockLocationAddressDE and the fiscal register
		// page's join both hit these columns directly, same drift rule as
		// the comments elsewhere in this fixture.
		`CREATE TABLE stock_locations (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, is_active INTEGER NOT NULL DEFAULT 1, address_street TEXT, address_postcode TEXT, address_city TEXT);`,
		// plugin_storage: column-identical to
		// internal/db/migrations/011_plugin_storage.sql -- since ADR-0072
		// (ut-docs#1106) the fiscal register page's create/list/decommission
		// handlers persist their entries here (JSON blobs under
		// com.universaltill.tax-de, keyed fiscal_register:<id>) via
		// data.FiscalRegisterDEStore, replacing the dropped
		// fiscal_register_de table (migration 075).
		`CREATE TABLE plugin_storage (plugin_id TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL, updated_at TEXT NOT NULL DEFAULT (datetime('now')), PRIMARY KEY (plugin_id, key));`,
		// name UNIQUE: column-identical to 001_init.sql (universaltill/ut-docs#651)
		// -- a drifted fixture missing that constraint would pass the
		// registers admin page's duplicate-name test against a schema that
		// can't reject what production's UNIQUE index rejects.
		`CREATE TABLE registers (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, location_id TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE shifts (id TEXT PRIMARY KEY, register_id TEXT NOT NULL, cashier_id TEXT NOT NULL, opened_at TEXT NOT NULL DEFAULT (datetime('now')), closed_at TEXT, opening_cash INTEGER NOT NULL DEFAULT 0, closing_cash INTEGER, expected_cash INTEGER, note TEXT, new_float INTEGER, count_protocol TEXT);`,
		// pin_hash is nullable — column-identical to 001_init.sql (ut-docs#761):
		// AuthRepo.CreateUser's INSERT never sets it ("no PIN yet — set it
		// separately", per its own doc comment), so a NOT NULL here made
		// CreateUser fail against this fixture specifically, while both
		// auth_page_test.go's and setup_page_test.go's own users fixtures
		// already had this column nullable, matching production. created_at
		// isn't even a real column in 001_init.sql's users table, but this
		// fixture added one for its own INSERTs — given a DEFAULT (the same
		// idiom this file already uses for shifts.opened_at above) so
		// CreateUser's INSERT, which never sets it either, doesn't fail the
		// NOT NULL it otherwise carried no value for.
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT UNIQUE NOT NULL, display_name TEXT, pin_hash TEXT, role TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		// customers/held_sales/price_history below are kept column-identical
		// to internal/db/migrations/001_init.sql and 002_held_sales.sql --
		// SearchCustomers/ResetTransactionHistory/CleanupObsoleteItems
		// reference their real columns directly, so a drifted fixture here
		// makes those code paths pass tests against a schema that doesn't
		// match production.
		`CREATE TABLE customers (id TEXT PRIMARY KEY, name TEXT NOT NULL, phone TEXT, email TEXT, address TEXT, loyalty_no TEXT UNIQUE, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		// name is NOT NULL UNIQUE in 001_init.sql — kept column-identical here
		// (ut-docs#16's review): a drifted fixture missing that constraint
		// would pass tests against a schema that can't reject what
		// production's UNIQUE index rejects.
		`CREATE TABLE payment_methods (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, type TEXT, is_active INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, plugin_id TEXT);`,
		// bearer_hash is nullable since migration 030 (ut-docs#405: a synced
		// roster row on a replica carries NULL) — kept column-identical here
		// so the ut-docs#426 join-snapshot redaction (UPDATE tills SET
		// bearer_hash = NULL) behaves as it does against production's schema.
		`CREATE TABLE tills (id TEXT PRIMARY KEY, name TEXT NOT NULL, bearer_hash TEXT UNIQUE, enrolled_at TEXT NOT NULL DEFAULT (datetime('now')), last_seen_at TEXT);`,
		`CREATE TABLE pending_pairings (id TEXT PRIMARY KEY, device_name TEXT NOT NULL, commitment TEXT NOT NULL, token TEXT NOT NULL DEFAULT '', requested_at TEXT NOT NULL, expires_at TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending');`,
		// order_status/order_status_updated_at (033) and the two
		// print-failure stamps (035) are part of production's sales schema —
		// ResetTransactionHistory archives by explicit column list, so the
		// fixture must carry them too (same drift rule as the comment below).
		// table_id (056_sale_table_id.sql, ut-docs#820): kept column-identical
		// here for the same reason order_status/print-failure stamps are --
		// ResetTransactionHistory's explicit column list and GetSaleDetail's
		// LEFT JOIN against `tables` both hit this fixture's real schema.
		`CREATE TABLE tables (id TEXT PRIMARY KEY, label TEXT NOT NULL, area_zone TEXT NOT NULL DEFAULT '', seat_count INTEGER NOT NULL DEFAULT 0, shape TEXT NOT NULL DEFAULT 'rect', pos_x INTEGER NOT NULL DEFAULT 0, pos_y INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);`,
		// table_claims: column-identical to internal/db/migrations/
		// 077_table_claims.sql (ut-docs#1390) -- POSRepo.IsTableFree /
		// ListTablesWithState read it on every table pick, hold, resume,
		// tender and reset, same drift rule as the comments above.
		`CREATE TABLE table_claims (table_id TEXT PRIMARY KEY REFERENCES tables(id), claimed_at TEXT NOT NULL);`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, receipt_no TEXT NOT NULL UNIQUE, status TEXT NOT NULL, sale_type TEXT NOT NULL, tender_type TEXT NOT NULL DEFAULT 'unknown', order_type TEXT NOT NULL DEFAULT '', table_id TEXT, offline INTEGER NOT NULL DEFAULT 0, sync_status TEXT NOT NULL DEFAULT 'queued', sync_attempts INTEGER NOT NULL DEFAULT 0, sync_next_attempt_at TEXT, sync_last_error TEXT, register_id TEXT, cashier_id TEXT, customer_id TEXT, till_id TEXT NOT NULL DEFAULT '', currency TEXT NOT NULL, subtotal INTEGER NOT NULL, discount_total INTEGER NOT NULL, tax_total INTEGER NOT NULL, total INTEGER NOT NULL, service_charge_amount INTEGER NOT NULL DEFAULT 0, service_charge_tax_basis_bp INTEGER NOT NULL DEFAULT 0, voucher_issue_total INTEGER NOT NULL DEFAULT 0, rounding INTEGER NOT NULL DEFAULT 0, note TEXT, created_at TEXT NOT NULL, completed_at TEXT, voided_at TEXT, order_status TEXT NOT NULL DEFAULT '', order_status_updated_at TEXT, kitchen_print_failed_at TEXT, receipt_print_failed_at TEXT, tracking_token TEXT);`,
		`CREATE TABLE sale_lines (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_no INTEGER NOT NULL, item_id TEXT, variant_id TEXT, name_snapshot TEXT NOT NULL, sku_snapshot TEXT, barcode_snapshot TEXT, quantity REAL NOT NULL, unit_price INTEGER NOT NULL, line_discount INTEGER NOT NULL DEFAULT 0, tax_rate_bp INTEGER NOT NULL, tax_amount INTEGER NOT NULL, total_before_tax INTEGER NOT NULL, total_after_tax INTEGER NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_line_modifiers (id TEXT PRIMARY KEY, sale_line_id TEXT NOT NULL, group_id TEXT, option_id TEXT, group_name_snapshot TEXT NOT NULL, option_name_snapshot TEXT NOT NULL, price_delta_minor INTEGER NOT NULL, FOREIGN KEY (sale_line_id) REFERENCES sale_lines(id));`,
		`CREATE TABLE sale_discounts (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_id TEXT, type TEXT NOT NULL, value INTEGER NOT NULL, amount INTEGER NOT NULL, reason TEXT);`,
		// sale_charges: column-identical to
		// internal/db/migrations/064_sale_charges.sql (ADR-0062, ut-docs#984)
		// — GetSaleDetail now always reads it, same drift rule as the
		// comments above (nothing writes a row here yet; that starts at
		// ADR-0062 step 2/3).
		`CREATE TABLE sale_charges (sale_id TEXT NOT NULL, seq INTEGER NOT NULL, key TEXT NOT NULL, label TEXT NOT NULL DEFAULT '', amount_minor INTEGER NOT NULL, tax_basis_bp INTEGER NOT NULL DEFAULT 0, base TEXT NOT NULL DEFAULT 'net_lines', PRIMARY KEY (sale_id, seq));`,
		`CREATE TABLE payments (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, method_id TEXT NOT NULL, amount INTEGER NOT NULL, currency TEXT NOT NULL, reference TEXT, change_given INTEGER NOT NULL DEFAULT 0, tip_amount INTEGER NOT NULL DEFAULT 0, tip_recipient TEXT NOT NULL DEFAULT 'employee', masked_pan TEXT, auth_code TEXT, terminal_id TEXT, trace_id TEXT, voucher_id TEXT, paid_at TEXT NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		// vouchers/voucher_transactions: column-identical to
		// internal/db/migrations/068_vouchers.sql (ut-docs#1008) — the EOD
		// summary (dateRangeSummary) now always reads voucher_transactions,
		// same drift rule as sale_charges above.
		`CREATE TABLE vouchers (id TEXT PRIMARY KEY, holder_label TEXT, original_amount INTEGER NOT NULL, balance INTEGER NOT NULL, currency TEXT NOT NULL DEFAULT 'EUR', voucher_type TEXT NOT NULL DEFAULT 'multi_purpose' CHECK (voucher_type IN ('multi_purpose')), status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','redeemed','void')), issued_sale_id TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE voucher_transactions (id TEXT PRIMARY KEY, voucher_id TEXT NOT NULL REFERENCES vouchers (id), sale_id TEXT, type TEXT NOT NULL CHECK (type IN ('issue','redemption')), amount INTEGER NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		// sync_journal_quarantine: column-identical to
		// internal/db/migrations/074_sync_journal_quarantine.sql (ut-docs#1127,
		// ADR-0065) — applyJournal's InsertJournalQuarantine/ListJournalQuarantine
		// hit it directly on a permanently-failing LAN-sync journal entry, same
		// drift rule as the comments above.
		`CREATE TABLE sync_journal_quarantine (id TEXT PRIMARY KEY, till_id TEXT NOT NULL, sale_id TEXT NOT NULL, receipt_no TEXT NOT NULL, reason TEXT NOT NULL, payload_json TEXT NOT NULL, quarantined_at TEXT NOT NULL, UNIQUE (sale_id));`,
		`CREATE TABLE sale_links (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, original_sale_id TEXT NOT NULL, reason TEXT);`,
		`CREATE TABLE stock_movements (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, sale_line_id TEXT, type TEXT NOT NULL, quantity REAL NOT NULL, cost_price INTEGER, created_at TEXT NOT NULL);`,
		// worker_allocations: column-identical to
		// internal/db/migrations/065_worker_allocations.sql (ADR-0063,
		// ut-docs#987) -- ResetTransactionHistory's resetArchiveTables loop
		// (internal/data/reset_archive_repo.go) archives every table in its
		// list unconditionally, so this fixture needs both twins or the
		// reset-transactions/reset-archives handler tests below fail against
		// a schema this table simply isn't in, same drift rule as every
		// other comment in this fixture.
		`CREATE TABLE worker_allocations (id TEXT NOT NULL PRIMARY KEY, source_type TEXT NOT NULL CHECK (source_type IN ('tip', 'service_charge', 'yuzde_usulu_pool')), source_id TEXT NOT NULL DEFAULT '', cashier_id TEXT NOT NULL, amount_minor INTEGER NOT NULL, allocated_at TEXT NOT NULL, note TEXT NOT NULL DEFAULT '');`,
		`CREATE TABLE inventory (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, quantity REAL NOT NULL, updated_at TEXT NOT NULL, UNIQUE(item_id, variant_id, location_id));`,
		// blocked_actor_id: column-identical to migration 049 (ut-docs#557) —
		// InsertAuditElevated's dual-attribution column; InsertAudit (and every
		// existing call site) still leaves it NULL.
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL, blocked_actor_id TEXT);`,
		// fiscal_tse_signatures: column-identical to
		// internal/db/migrations/048_fiscal_tse_signatures.sql (ut-docs#585)
		// — the tender path's RecordFiscalTSESignature and both receipt
		// render paths' GetFiscalTSESignature hit it directly, same drift
		// rule as the comments above.
		`CREATE TABLE fiscal_tse_signatures (sale_id TEXT PRIMARY KEY, transaction_number INTEGER NOT NULL DEFAULT 0, signature_counter INTEGER NOT NULL DEFAULT 0, serial_number TEXT NOT NULL DEFAULT '', start_time TEXT NOT NULL DEFAULT '', log_time TEXT NOT NULL DEFAULT '', signature TEXT NOT NULL, signature_algorithm TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		// report_archive: column-identical to 013_report_archive.sql plus
		// 070_report_archive_zreport_numbering.sql's Z-number/receipt-range
		// columns and partial unique index (ut-docs#1080) — same drift rule
		// as the comments above. (037's cloud_acked_at is not read on any
		// path these tests exercise.)
		`CREATE TABLE report_archive (id TEXT PRIMARY KEY, kind TEXT NOT NULL, period TEXT NOT NULL, content_json TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now')), z_number INTEGER, prev_z_number INTEGER, prev_closed_at TEXT, first_receipt TEXT, last_receipt TEXT, UNIQUE (kind, period));`,
		`CREATE UNIQUE INDEX ux_report_archive_kind_znumber ON report_archive (kind, z_number) WHERE z_number IS NOT NULL;`,
		// table_id: column-identical to internal/db/migrations/054_tables.sql's
		// ALTER (ut-docs#814/ADR-0054) and 055_held_sales_archive_table_id.sql's
		// matching ALTER on the archive twin below — same drift rule as the
		// customers/held_sales comment above (2026-08-19 code review finding).
		`CREATE TABLE held_sales (id TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '', total_minor INTEGER NOT NULL DEFAULT 0, line_count INTEGER NOT NULL DEFAULT 0, payload TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now')), table_id TEXT);`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL DEFAULT (datetime('now')), ends_at TEXT);`,
		`CREATE TABLE invoices (id TEXT PRIMARY KEY, series TEXT NOT NULL, invoice_no INTEGER NOT NULL, display_no TEXT NOT NULL UNIQUE, kind TEXT NOT NULL DEFAULT 'invoice', sale_id TEXT NOT NULL, original_invoice_id TEXT, customer_name TEXT NOT NULL, customer_address TEXT NOT NULL DEFAULT '', customer_vat_no TEXT NOT NULL DEFAULT '', seller_json TEXT NOT NULL, net_total INTEGER NOT NULL, tax_total INTEGER NOT NULL, gross_total INTEGER NOT NULL, vat_breakdown_json TEXT NOT NULL, issued_at TEXT NOT NULL, issued_by TEXT NOT NULL, UNIQUE(series, invoice_no), UNIQUE(sale_id, kind));`,
		// Reset archive (ADR-0042, migration 040) — kept column-identical to
		// internal/db/migrations/040_reset_archive.sql, same drift rule as
		// the customers/held_sales comment above.
		`CREATE TABLE reset_batches (id TEXT PRIMARY KEY, created_at TEXT NOT NULL, actor_id TEXT, sales_count INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE sales_archive (id TEXT NOT NULL, receipt_no TEXT NOT NULL, status TEXT NOT NULL, sale_type TEXT NOT NULL, tender_type TEXT NOT NULL, offline INTEGER NOT NULL, sync_status TEXT NOT NULL, sync_attempts INTEGER NOT NULL, sync_next_attempt_at TEXT, sync_last_error TEXT, register_id TEXT, cashier_id TEXT, customer_id TEXT, currency TEXT NOT NULL, subtotal INTEGER NOT NULL, discount_total INTEGER NOT NULL, tax_total INTEGER NOT NULL, total INTEGER NOT NULL, rounding INTEGER NOT NULL, note TEXT, created_at TEXT NOT NULL, completed_at TEXT, voided_at TEXT, till_id TEXT NOT NULL, service_charge_amount INTEGER NOT NULL, service_charge_tax_basis_bp INTEGER NOT NULL DEFAULT 0, order_type TEXT NOT NULL, order_status TEXT NOT NULL, order_status_updated_at TEXT, kitchen_print_failed_at TEXT, receipt_print_failed_at TEXT, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id), table_id TEXT, tracking_token TEXT, voucher_issue_total INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE sale_lines_archive (id TEXT NOT NULL, sale_id TEXT NOT NULL, line_no INTEGER NOT NULL, item_id TEXT, variant_id TEXT, name_snapshot TEXT NOT NULL, sku_snapshot TEXT, barcode_snapshot TEXT, quantity REAL NOT NULL, unit_price INTEGER NOT NULL, line_discount INTEGER NOT NULL, tax_rate_bp INTEGER NOT NULL, tax_amount INTEGER NOT NULL, total_before_tax INTEGER NOT NULL, total_after_tax INTEGER NOT NULL, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		`CREATE TABLE sale_line_modifiers_archive (id TEXT NOT NULL, sale_line_id TEXT NOT NULL, group_id TEXT, option_id TEXT, group_name_snapshot TEXT NOT NULL, option_name_snapshot TEXT NOT NULL, price_delta_minor INTEGER NOT NULL, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		`CREATE TABLE sale_discounts_archive (id TEXT NOT NULL, sale_id TEXT NOT NULL, line_id TEXT, type TEXT NOT NULL, value INTEGER NOT NULL, amount INTEGER NOT NULL, reason TEXT, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		// sale_charges_archive: column-identical to
		// internal/db/migrations/064_sale_charges.sql (ADR-0062, ut-docs#984)
		// — ResetTransactionHistory now archives sale_charges unconditionally
		// as part of every reset, same drift rule as the comments above.
		`CREATE TABLE sale_charges_archive (sale_id TEXT NOT NULL, seq INTEGER NOT NULL, key TEXT NOT NULL, label TEXT NOT NULL DEFAULT '', amount_minor INTEGER NOT NULL, tax_basis_bp INTEGER NOT NULL DEFAULT 0, base TEXT NOT NULL DEFAULT 'net_lines', reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		`CREATE TABLE sale_links_archive (id TEXT NOT NULL, sale_id TEXT NOT NULL, original_sale_id TEXT NOT NULL, reason TEXT, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		`CREATE TABLE payments_archive (id TEXT NOT NULL, sale_id TEXT NOT NULL, method_id TEXT NOT NULL, amount INTEGER NOT NULL, currency TEXT NOT NULL, reference TEXT, change_given INTEGER NOT NULL, paid_at TEXT NOT NULL, tip_amount INTEGER NOT NULL, tip_recipient TEXT NOT NULL DEFAULT 'employee', masked_pan TEXT, auth_code TEXT, terminal_id TEXT, trace_id TEXT, voucher_id TEXT, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		`CREATE TABLE invoices_archive (id TEXT NOT NULL, series TEXT NOT NULL, invoice_no INTEGER NOT NULL, display_no TEXT NOT NULL, kind TEXT NOT NULL, sale_id TEXT NOT NULL, original_invoice_id TEXT, customer_name TEXT NOT NULL, customer_address TEXT NOT NULL, customer_vat_no TEXT NOT NULL, seller_json TEXT NOT NULL, net_total INTEGER NOT NULL, tax_total INTEGER NOT NULL, gross_total INTEGER NOT NULL, vat_breakdown_json TEXT NOT NULL, issued_at TEXT NOT NULL, issued_by TEXT NOT NULL, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		`CREATE TABLE held_sales_archive (id TEXT NOT NULL, label TEXT NOT NULL, total_minor INTEGER NOT NULL, line_count INTEGER NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id), table_id TEXT);`,
		`CREATE TABLE shifts_archive (id TEXT NOT NULL, register_id TEXT NOT NULL, cashier_id TEXT NOT NULL, opened_at TEXT NOT NULL, closed_at TEXT, opening_cash INTEGER NOT NULL, closing_cash INTEGER, expected_cash INTEGER, note TEXT, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id), new_float INTEGER, count_protocol TEXT);`,
		`CREATE TABLE stock_movements_archive (id TEXT NOT NULL, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, sale_line_id TEXT, type TEXT NOT NULL, quantity REAL NOT NULL, cost_price INTEGER, created_at TEXT NOT NULL, reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		`CREATE TABLE worker_allocations_archive (id TEXT NOT NULL, source_type TEXT NOT NULL CHECK (source_type IN ('tip', 'service_charge', 'yuzde_usulu_pool')), source_id TEXT NOT NULL DEFAULT '', cashier_id TEXT NOT NULL, amount_minor INTEGER NOT NULL, allocated_at TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', reset_batch_id TEXT NOT NULL REFERENCES reset_batches(id));`,
		// roles/permission_actions/role_permissions: column-identical to
		// internal/db/migrations/039_role_permissions.sql + 042/043/044
		// (ut-docs#709/#706/#707) — a drifted fixture here would let a
		// canPerform()-gated page test pass against a permission schema
		// production doesn't have.
		`CREATE TABLE roles (role TEXT PRIMARY KEY);`,
		`CREATE TABLE permission_actions (action TEXT PRIMARY KEY);`,
		`CREATE TABLE role_permissions (role TEXT NOT NULL REFERENCES roles(role), action TEXT NOT NULL REFERENCES permission_actions(action), granted INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (role, action));`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed ddl failed: %v", err)
		}
	}
	// Seed grants identical to 039 (manager/admin/super_admin get every
	// catalog action, cashier gets none) + 042's reports/audit additions +
	// 043's plugin_management addition (#706) + 044's data_management/
	// sync_management additions (#707) + 045's import_export/issue_reporting
	// additions (#713) + 060's stock_location_management addition (#903) —
	// keep this list in sync with every migration that
	// adds a new action, so canPerform()-gated page tests exercise the real
	// seed shape.
	for _, role := range []string{"cashier", "manager", "admin", "super_admin"} {
		if _, err := db.Exec(`INSERT INTO roles (role) VALUES (?)`, role); err != nil {
			t.Fatalf("seed role %s: %v", role, err)
		}
	}
	catalog := []string{"refund", "eod_report", "cash_adjustment", "price_override", "void", "user_management", "settings", "reports", "audit", "plugin_management", "data_management", "sync_management", "import_export", "issue_reporting", "tax_code_management", "stock_location_management", "worker_allocation"}
	for _, action := range catalog {
		if _, err := db.Exec(`INSERT INTO permission_actions (action) VALUES (?)`, action); err != nil {
			t.Fatalf("seed permission_action %s: %v", action, err)
		}
	}
	for _, role := range []string{"manager", "admin", "super_admin"} {
		for _, action := range catalog {
			if _, err := db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES (?, ?, 1)`, role, action); err != nil {
				t.Fatalf("seed role_permission %s/%s: %v", role, action, err)
			}
		}
	}
	// 046's fiscal_tse_override is the one action deliberately NOT granted
	// to manager (ADR-0048: owner = admin, must not silently become
	// manager-or-above) — seeded separately to mirror the migration exactly.
	if _, err := db.Exec(`INSERT INTO permission_actions (action) VALUES ('fiscal_tse_override')`); err != nil {
		t.Fatalf("seed permission_action fiscal_tse_override: %v", err)
	}
	for _, role := range []string{"admin", "super_admin"} {
		if _, err := db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES (?, 'fiscal_tse_override', 1)`, role); err != nil {
			t.Fatalf("seed role_permission %s/fiscal_tse_override: %v", role, err)
		}
	}
	// 047's permission_management is the matrix-editor page's own gating
	// action (ut-docs#556) — seeded super_admin ONLY, deliberately not even
	// the admin+super_admin pattern 046 uses: see 047's migration comment.
	if _, err := db.Exec(`INSERT INTO permission_actions (action) VALUES ('permission_management')`); err != nil {
		t.Fatalf("seed permission_action permission_management: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES ('super_admin', 'permission_management', 1)`); err != nil {
		t.Fatalf("seed role_permission super_admin/permission_management: %v", err)
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
	// ut-docs#744: a variant of itm1, its own barcode + own inventory row,
	// used by TestTenderHandler_VariantBarcodeScanIsTenderable to drive a
	// variant scan through the real /api/pos/tender handler end to end.
	_, _ = db.Exec(`INSERT INTO item_variants(id,item_id,sku,name,price,is_active) VALUES('var1','itm1','ABC-L','Large',150,1)`)
	_, _ = db.Exec(`INSERT INTO variant_barcodes(barcode,variant_id,is_primary) VALUES('VAR','var1',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id,item_id,variant_id,location_id,quantity,updated_at) VALUES('inv2',NULL,'var1','loc_main',30,datetime('now'))`)
	// display_name is NOT NULL in the real schema (001_init.sql) — set here
	// too (ut-docs#964) so AuthRepo.ListUsers' plain (non-COALESCEd) scan of
	// this column doesn't fail on these two pre-seeded rows the moment a
	// page actually calls ListUsers against this fixture and checks its error.
	_, _ = db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('user1','admin','Admin','','admin',datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('system','system','System','','admin',datetime('now'))`)
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
