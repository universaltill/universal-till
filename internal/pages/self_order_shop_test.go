package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
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
		Settings: nil,
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/search", nil))
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

func TestSelfOrderShop_SearchFiltersbyName(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-tea", "TEA", "5000002", "Tea", 250)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/search?q=flat", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "Flat White") {
		t.Fatal("search for 'flat' should match Flat White")
	}
	if strings.Contains(body, ">Tea<") {
		t.Fatal("search for 'flat' should not match Tea")
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/search", nil))
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

	for _, path := range []string{"/self-order/shop", "/api/self-order/search", "/api/self-order/cart"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("anonymous GET %s = %d, want 200 (must be auth-exempt)", path, rec.Code)
		}
	}
}
