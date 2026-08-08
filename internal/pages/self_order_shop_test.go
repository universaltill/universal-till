package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// setupSelfOrderShopDeps wires a real Engine against the real resolver
// chain (ui.PriceResolverAdapter, the same one production uses) rather
// than a hand-rolled stub — the kiosk grid computes each tile's "Code"
// from real item_barcodes/sku data, and this proves that code actually
// resolves through the same path a real scan would use, not just that it
// matches a stub keyed by coincidence.
func setupSelfOrderShopDeps(t *testing.T) (*common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "shop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	btnStore := ui.NewButtonStore(d.DB)
	resolver := ui.PriceResolverAdapter{Store: btnStore}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	dp := &common.Deps{
		Cfg:      &config.Config{Theme: "default", StoreName: "Task Runner Cafe"},
		Db:       d.DB,
		State:    common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Menu:     []common.MenuItem{},
		Settings: settings.NewStore(d.DB),
		Engine:   engine,
	}
	return dp, d
}

func seedShopItem(t *testing.T, d *db.DB, id, sku, barcode, name string, priceMinor int64) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES (?,?,?,?,1)`, id, sku, name, priceMinor); err != nil {
		t.Fatal(err)
	}
	if barcode != "" {
		if _, err := d.DB.Exec(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES (?,?,1)`, id, barcode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSelfOrderShop_BrowseGridShowsActiveItems(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-tea", "TEA", "5000002", "Tea", 250)
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-old','OLD','Discontinued',100,0)`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/grid", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Flat White") || !strings.Contains(body, "Tea") {
		t.Fatalf("grid missing active items: %s", body)
	}
	if strings.Contains(body, "Discontinued") {
		t.Fatal("grid must not show an inactive item")
	}
}

// ut-docs#419: the self-order kiosk is category-browsing only — there is no
// search affordance, and the grid endpoint (renamed from .../search to
// .../grid, since it no longer searches anything) always returns the full
// active catalog regardless of what a client sends it.
func TestSelfOrderShop_GridIgnoresQueryParam(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-tea", "TEA", "5000002", "Tea", 250)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/grid?q=flat", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "Flat White") || !strings.Contains(body, "Tea") {
		t.Fatalf("grid must ignore any q param and return the full catalog, got: %s", body)
	}
}

// ut-docs#419: the search input and the old /api/self-order/search path
// must be entirely gone from the rendered shop page — category chips are
// the sole find mechanism now.
func TestSelfOrderShop_PageHasNoSearchInput(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order/shop", nil))
	body := rec.Body.String()
	if strings.Contains(body, "selforder-search") {
		t.Fatalf("search input must be removed from the self-order shop page: %s", body)
	}
	if strings.Contains(body, "/api/self-order/search") {
		t.Fatalf("no reference to the retired search endpoint should remain: %s", body)
	}
}

// ut-docs#419: category-chip filtering is client-side JS keyed off each
// tile's data-cat attribute — prove the grid still stamps it on every
// tile now that it's the sole find mechanism.
func TestSelfOrderShop_TilesCarryCategoryForChipFiltering(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	if _, err := d.DB.Exec(`INSERT INTO categories (id, name) VALUES ('cat-drinks','Drinks')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('itm-coffee','COFFEE','Flat White',320,1,'cat-drinks')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('itm-coffee','5000001',1)`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/grid", nil))
	if !strings.Contains(rec.Body.String(), `data-cat="cat-drinks"`) {
		t.Fatalf("tile must carry its category in data-cat for client-side chip filtering, got: %s", rec.Body.String())
	}
}

// ut-docs#419: found driving the kiosk in a real browser, not from
// rendered-HTML assertions alone — .selforder-tile's own `display:flex`
// has equal-or-later specificity over the UA stylesheet's
// `[hidden] { display:none }`, so toggling a tile's `hidden` IDL property
// (what the chip-filter script does) silently had NO visual effect until
// this override rule was added. A Go test can't see the rendered pixels,
// but it can pin the CSS rule that fixes it so a future edit to the tile
// styles doesn't silently drop the override again.
func TestSelfOrderShop_HiddenTileRuleOverridesTileDisplay(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order/shop", nil))
	body := rec.Body.String()
	if !strings.Contains(body, ".selforder-tile[hidden]") || !strings.Contains(body, "display:none") {
		t.Fatalf("expected a .selforder-tile[hidden] { display:none } override so the chip filter's hidden=true actually hides the tile, got: %s", body)
	}
}

func TestSelfOrderShop_ScanAddsPlainItem(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Name != "Flat White" {
		t.Fatalf("expected Flat White added, got %+v", b.Lines)
	}
}

// The cashier's /api/pos/scan redirects to /refund/{code} when the scanned
// code matches an existing receipt number — a real feature for a cashier,
// but it must NEVER be reachable from the anonymous kiosk surface. This is
// the specific behavior the kiosk scan handler was deliberately NOT
// copied wholesale to avoid.
func TestSelfOrderShop_ScanNeverTriggersRefundRedirect(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	// Seed a sale whose receipt_no looks like a scannable code.
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s1','R-0001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=R-0001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if loc := rec.Header().Get("HX-Redirect"); loc != "" {
		t.Fatalf("kiosk scan must never HX-Redirect to a refund screen, got %q", loc)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestSelfOrderShop_ModifierFlow(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	if _, err := d.DB.Exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order) VALUES ('g1','itm-coffee','Extras',0,0,2,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order) VALUES ('o1','g1','Extra shot',50,1)`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	// The grid should route this item to the modifier picker.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/grid", nil))
	if !strings.Contains(rec.Body.String(), "/api/self-order/modifiers?item=itm-coffee") {
		t.Fatalf("grid should route the modifier-bearing item to the picker: %s", rec.Body.String())
	}

	// Picker renders.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/self-order/modifiers?item=itm-coffee&code=5000001", nil))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "Extra shot") {
		t.Fatalf("picker: want 200 with Extra shot, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Submitting adds the folded-price line.
	form := url.Values{"code": {"5000001"}, "itemId": {"itm-coffee"}, "mod_g1": {"o1"}}
	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan-with-modifiers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req)
	if rec3.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec3.Code, rec3.Body.String())
	}
	b := dp.Engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].PriceCents != 370 {
		t.Fatalf("want 1 line at 370 (320+50), got %+v", b.Lines)
	}
}

func TestSelfOrderShop_QtyStepperAndRemove(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	post("/api/self-order/scan", "code=5000001")
	key := dp.Engine.Basket().Lines[0].LineKey

	rec := post("/api/self-order/line", "key="+key+"&delta=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("delta+1: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().Lines[0].Qty; got != 2 {
		t.Fatalf("want qty 2 after +1, got %v", got)
	}

	post("/api/self-order/line", "key="+key+"&delta=-1")
	if got := dp.Engine.Basket().Lines[0].Qty; got != 1 {
		t.Fatalf("want qty 1 after -1, got %v", got)
	}

	rec2 := post("/api/self-order/remove", "key="+key)
	if rec2.Code != http.StatusOK {
		t.Fatalf("remove: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("expected line removed")
	}
}

// A manual "qty" delta cannot be used to smuggle in a free-text discount —
// this endpoint has no discount field at all, unlike the cashier's
// /api/pos/line, so there's nothing to test there beyond confirming the
// form param simply doesn't exist in the handler's vocabulary (it's
// silently ignored if sent).
func TestSelfOrderShop_LineEndpointIgnoresDiscountParam(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	key := dp.Engine.Basket().Lines[0].LineKey

	req2 := httptest.NewRequest(http.MethodPost, "/api/self-order/line", strings.NewReader("key="+key+"&delta=0&discount=999999"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req2)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	b := dp.Engine.Basket()
	if b.Lines[0].LineTotal != b.Lines[0].PriceCents {
		t.Fatalf("a smuggled discount param must have no effect: line total %v != price %v", b.Lines[0].LineTotal, b.Lines[0].PriceCents)
	}
}

// Landing on /self-order clears any in-progress basket — otherwise the
// next customer would see whatever the previous one abandoned.
func TestSelfOrder_LandingResetsBasket(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

	shopMux := http.NewServeMux()
	registerSelfOrderShop(shopMux, dp)
	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	shopMux.ServeHTTP(httptest.NewRecorder(), req)
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatal("setup: expected 1 line before landing-page reset")
	}

	landingMux := http.NewServeMux()
	registerSelfOrder(landingMux, dp)
	landingMux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/self-order", nil))

	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("expected the abandoned cart to be cleared on landing-page revisit")
	}
}

// seedStock gives an item enough on-hand quantity at the default stock
// location to pass CompleteSale's insufficient-stock check (real migrations
// enforce it, unlike the hand-rolled test schema some other packages use).
func seedStock(t *testing.T, d *db.DB, itemID string, qty float64) {
	t.Helper()
	locID, err := data.NewPOSRepo(d.DB).EnsureStockLocation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES (?,?,?,?)`,
		"inv-"+itemID, itemID, locID, qty); err != nil {
		t.Fatal(err)
	}
}

// The built-in seed (001_init.sql) gives every fresh shop 'card' and 'gift'
// (voucher) as active non-cash methods, plus 'cash' which must never appear
// at a kiosk (ADR-0020: no cash drawer). This is the happy path: pick a
// listed method, complete, and see the basket reset + a confirmation.
func TestSelfOrderShop_CheckoutHappyPath(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	post("/api/self-order/scan", "code=5000001")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/checkout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET checkout: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `value="card"`) {
		t.Fatalf("payment picker should offer the built-in 'card' method: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `value="cash"`) {
		t.Fatalf("payment picker must never offer 'cash' at a kiosk: %s", rec.Body.String())
	}

	rec2 := post("/api/self-order/checkout", "method=card")
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST checkout: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Order placed") {
		t.Fatalf("expected an order-confirmation screen, got: %s", rec2.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("basket should be reset after a completed checkout")
	}

	var total int64
	if err := d.DB.QueryRow(`SELECT total FROM sales WHERE status = 'completed'`).Scan(&total); err != nil {
		t.Fatalf("expected a completed sale row: %v", err)
	}
	// 320 + 20% tax (setupSelfOrderShopDeps: TaxRatePct 20, TaxInclusive false).
	if total != 384 {
		t.Fatalf("want sale total 384 (320 + 20%% tax), got %d", total)
	}
	var cashierID string
	if err := d.DB.QueryRow(`SELECT cashier_id FROM sales WHERE status = 'completed'`).Scan(&cashierID); err != nil {
		t.Fatal(err)
	}
	if cashierID != "kiosk" {
		t.Fatalf("kiosk sale must be attributed to the seeded 'kiosk' operator, got %q", cashierID)
	}
}

// The kiosk must never let a client tender 'cash' — even though it's a
// perfectly valid, active payment_methods row — since ADR-0020 v1 has no
// cash drawer at a kiosk. Only ListActiveNonCashPaymentMethods' own list is
// trusted, never a client-submitted method string, however plausible.
func TestSelfOrderShop_CheckoutRejectsCashMethod(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	req2 := httptest.NewRequest(http.MethodPost, "/api/self-order/checkout", strings.NewReader("method=cash"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req2)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 rejecting cash, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatal("a rejected checkout must not touch the basket")
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n)
	if n != 0 {
		t.Fatal("a rejected checkout must not create a sale")
	}
}

// An arbitrary/unknown method string must be rejected outright — critically,
// it must NOT fall back to EnsurePaymentMethod (as the cashier tender
// handler's quick-tender-button fallback does), which would silently insert
// a new type='cash' payment_methods row for whatever a client sends on this
// anonymous surface.
func TestSelfOrderShop_CheckoutRejectsUnknownMethod(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	var before int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM payment_methods`).Scan(&before)

	req2 := httptest.NewRequest(http.MethodPost, "/api/self-order/checkout", strings.NewReader("method=bitcoin"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req2)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 rejecting an unknown method, got %d: %s", rec.Code, rec.Body.String())
	}

	var after int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM payment_methods`).Scan(&after)
	if after != before {
		t.Fatalf("an unknown method must not create a payment_methods row: before=%d after=%d", before, after)
	}
}

// An empty basket cannot be checked out, whether the customer never added
// anything or emptied their cart after opening the payment picker.
func TestSelfOrderShop_CheckoutRejectsEmptyBasket(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/checkout", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET checkout on empty basket: want 400, got %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/checkout", strings.NewReader("method=card"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("POST checkout on empty basket: want 400, got %d", rec2.Code)
	}
}

// A plugin-provided payment method whose owning plugin declines the
// blocking payment.<key>.authorize event must stop the kiosk sale exactly
// like it stops a cashier's — proving the kiosk checkout goes through the
// same completeTender gate, not a bypassed copy.
func TestSelfOrderShop_CheckoutDeclinedByPluginGateNeverCompletesSale(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	if _, err := d.DB.Exec(`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', '0.1.0', '1', '2026-07-17')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.payment-demo', 'Demo Pay', '1.0.0', 'demo.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.payment-demo', 'demopay', 'Demo Pay', 'payment', 'payment.demopay.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.payment-demo', 'payment.demopay.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := data.NewPluginRepo(d.DB).SyncPluginPaymentMethods(context.Background()); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(d.DB)
	t.Cleanup(bus.ResetSubscribers) // process-global singleton — don't leak this always-decline handler to later tests
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return nil, errors.New("demopay: card declined")
		}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	req2 := httptest.NewRequest(http.MethodPost, "/api/self-order/checkout", strings.NewReader("method=demopay"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req2)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("want 402 on a declined plugin gate, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatal("a declined checkout must not reset/lose the basket")
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n)
	if n != 0 {
		t.Fatal("a declined checkout must not create a sale")
	}
}

// A card-terminal plugin (e.g. ut-plugin-payment-sumup's reader flow) can
// report the tip it captured on its blocking payment.<key>.authorize
// response; completeTender must apply it to the persisted payment even
// though the tender request itself carried no tip — proving the response
// actually reaches the sale, not just that the gate approves/declines.
func TestSelfOrderShop_CheckoutAppliesPluginReportedTipFromAuthorizeResponse(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	if _, err := d.DB.Exec(`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', '0.1.0', '1', '2026-07-17')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.payment-demo', 'Demo Pay', '1.0.0', 'demo.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.payment-demo', 'demopay', 'Demo Pay', 'payment', 'payment.demopay.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.payment-demo', 'payment.demopay.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.payment-demo', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := data.NewPluginRepo(d.DB).SyncPluginPaymentMethods(context.Background()); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(d.DB)
	// SharedBus is a process-global singleton (wasm_runtime.go), so a stale
	// subscriber left registered under the same plugin id/event by an
	// earlier test in this package (e.g. the decline test right above,
	// which also uses "com.universaltill.payment-demo"/"demopay") would
	// otherwise still fire here too — reset for a clean slate.
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers) // don't leak this test's always-approve handler to whatever runs after it
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"provider":"demopay","outcome":"approved","tip_amount":150}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	req2 := httptest.NewRequest(http.MethodPost, "/api/self-order/checkout", strings.NewReader("method=demopay"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req2)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var tip int64
	if err := d.DB.QueryRow(`SELECT tip_amount FROM payments p JOIN sales s ON s.id = p.sale_id WHERE s.status = 'completed'`).Scan(&tip); err != nil {
		t.Fatalf("expected a completed sale's payment row: %v", err)
	}
	if tip != 150 {
		t.Fatalf("want tip_amount 150 (from the plugin's authorize response), got %d", tip)
	}
}

// Every new kiosk route is genuinely reachable anonymously, through the
// real auth middleware — the prefix exemption already covers /self-order/
// and /api/self-order/, but this proves the SPECIFIC new paths, not just
// the prefix rule in isolation.
func TestSelfOrderShop_RoutesAreAuthExempt(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	authSvc := auth.NewService(d.DB)
	h := auth.Middleware(mux, authSvc)

	for _, path := range []string{"/self-order/shop", "/api/self-order/grid", "/api/self-order/cart"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("anonymous GET %s = %d, want 200 (must be auth-exempt)", path, rec.Code)
		}
	}
}

// ut-docs#208: the browse/cart/checkout screen (self_order_shop.html covers
// all three — cart and checkout are rendered into it, not separate pages)
// has no way back to till settings today. A discreet exit control must be
// reachable here, linking to the real /login flow — not a new auth
// mechanism — so a valid manager/cashier PIN there returns to the normal
// gated till surface, and an invalid one is rejected by that existing flow
// with no access granted.
func TestSelfOrderShop_HasPinGatedExitLink(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order/shop", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order/shop = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/login?next=kiosk"`) {
		t.Fatalf("self-order shop screen missing an exit link to /login?next=kiosk: %s", body)
	}
	if !strings.Contains(body, "selforder-exit") {
		t.Fatalf("self-order shop screen missing the discreet exit affordance styling: %s", body)
	}
}
