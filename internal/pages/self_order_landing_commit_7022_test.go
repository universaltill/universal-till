package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#7022: the kiosk landing page's walk-up reset is an abandon
// operation (ut-docs#6923): skipped while a tender holds the commit point,
// still the #6602 escape hatch while a tender authorizes.

const startScreenMarker = `href="/self-order/shop"`

func kioskPost(mux http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func kioskGet(mux http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// landingDuringSignAsk runs a kiosk card checkout and, at the commit point
// (immediately before fiscal.sign.ask), lands on /self-order.
func landingDuringSignAsk(t *testing.T) (dpLines func() int, mux *http.ServeMux, checkout *httptest.ResponseRecorder, landing *httptest.ResponseRecorder, attemptHeld bool, salesN func() int, cashierLines int) {
	t.Helper()
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	ctx := context.Background()
	const pluginID = "com.test.kiosk-landing-7022"
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('` + pluginID + `', '1.0.0', 'Kiosk Fiscal Sign', 'wasm', './plugin.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES ('` + pluginID + `', 'Kiosk Fiscal Sign', '1.0.0', 'installed', './plugin.wasm', 'wasm', 1, 'trusted')`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
VALUES ('hook-7022', '` + pluginID + `', 'fiscal.sign.ask', 'fiscal.sign', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
VALUES ('perm-7022', '` + pluginID + `', 'events:receive', 1)`,
	} {
		if _, err := d.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	bus := plugins.SharedBus(d.DB)
	t.Cleanup(bus.ResetSubscribers)
	if _, err := bus.SubscribeWithHandler(ctx, pluginID, []string{"fiscal.sign.ask"}, func(context.Context, plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(approvedSign), nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	mux = http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	kioskPost(mux, "/api/self-order/scan", "code=5000001")
	cashierLines = len(dp.Engine.Basket().Lines)

	tenderBeforeFiscalSignHook = func() {
		before := dp.KioskEngine.TenderAttemptID()
		landing = kioskGet(mux, "/self-order")
		// The landing must neither rotate the attempt nor wipe the basket.
		attemptHeld = before != "" && dp.KioskEngine.TenderAttemptIs(before) &&
			len(dp.KioskEngine.Basket().Lines) != 0
	}
	t.Cleanup(func() { tenderBeforeFiscalSignHook = nil })
	checkout = kioskPost(mux, "/api/self-order/checkout", "method=card")
	tenderBeforeFiscalSignHook = nil
	if len(dp.Engine.Basket().Lines) != cashierLines {
		t.Fatal("the cashier engine must be untouched")
	}
	dpLines = func() int { return len(dp.KioskEngine.Basket().Lines) }
	salesN = func() int { return countRows(t, dp, "SELECT COUNT(*) FROM sales") }
	return
}

func TestSelfOrderLanding_DuringSignAskSkipsResetAndSaleRecorded(t *testing.T) {
	_, _, checkout, landing, attemptHeld, salesN, _ := landingDuringSignAsk(t)
	if landing == nil || landing.Code != http.StatusOK || !strings.Contains(landing.Body.String(), startScreenMarker) {
		t.Fatalf("landing during the ask must render the start screen (200): %+v", landing)
	}
	if !attemptHeld {
		t.Fatal("the landing reset must be skipped while the commit point is held: attempt/basket were wiped")
	}
	if checkout.Code != http.StatusOK {
		t.Fatalf("checkout = %d, want 200 confirmation: %s", checkout.Code, checkout.Body.String())
	}
	if strings.Contains(checkout.Body.String(), "outcome_unknown") {
		t.Fatalf("checkout reported outcome_unknown: %s", checkout.Body.String())
	}
	if n := salesN(); n != 1 {
		t.Fatalf("sales = %d, want 1", n)
	}
}

// Regression guard for the next customer's basket after a commit; it also
// passes without the ut-docs#7022 fix (kiosk basket edits are refused during a
// tender, #6926), so the fix itself is pinned by
// TestSelfOrderLanding_DuringSignAskSkipsResetAndSaleRecorded.
func TestSelfOrderLanding_AfterCommitNextCustomerBasketKept(t *testing.T) {
	kioskLines, mux, _, _, _, _, _ := landingDuringSignAsk(t)
	if n := kioskLines(); n != 0 {
		t.Fatalf("kiosk basket after the sale = %d lines, want 0", n)
	}
	if rec := kioskPost(mux, "/api/self-order/scan", "code=5000001"); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d: %s", rec.Code, rec.Body.String())
	}
	cart := kioskGet(mux, "/api/self-order/cart")
	if cart.Code != http.StatusOK || !strings.Contains(cart.Body.String(), "Flat White") {
		t.Fatalf("next customer's item lost: %d %s", cart.Code, cart.Body.String())
	}
	if n := kioskLines(); n != 1 {
		t.Fatalf("kiosk basket = %d lines, want 1", n)
	}
}

func TestSelfOrderLanding_DuringAuthorizeStillAbandons(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)
	var landing *httptest.ResponseRecorder
	var linesAfterLanding = -1
	t.Cleanup(plugins.SharedBus(d.DB).ResetSubscribers)
	// The customer lands on /self-order while the card authorizes (#6602).
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string {
		landing = kioskGet(mux, "/self-order")
		linesAfterLanding = len(dp.KioskEngine.Basket().Lines)
		return approvedAnswer
	})
	if err := data.NewPluginRepo(d.DB).SyncPluginPaymentMethods(context.Background()); err != nil {
		t.Fatal(err)
	}
	kioskPost(mux, "/api/self-order/scan", "code=5000001")

	rec := kioskPost(mux, "/api/self-order/checkout", "method=demopay")
	if landing == nil || landing.Code != http.StatusOK {
		t.Fatalf("landing during authorize = %+v, want 200", landing)
	}
	if linesAfterLanding != 0 {
		t.Fatalf("landing during authorize must still abandon the basket, got %d lines", linesAfterLanding)
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("an abandoned tender must be refused, got 200: %s", rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if got := refunds(); len(got) != 1 {
		t.Fatalf("captured card leg reversals = %d, want exactly 1 (#6602)", len(got))
	}
}

func TestSelfOrderLanding_IdleResetsBasketAndLanguage(t *testing.T) {
	for _, target := range []string{"/self-order", "/self-order?table=no-such-table"} {
		dp, d := setupSelfOrderShopDeps(t)
		seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
		mux := http.NewServeMux()
		registerSelfOrder(mux, dp)
		registerSelfOrderShop(mux, dp)
		g := newSelfOrderGuest(t, mux)
		g.get("/self-order/language?code=fa")
		g.post("/api/self-order/scan", "code=5000001")
		if len(dp.KioskEngine.Basket().Lines) != 1 {
			t.Fatalf("%s: setup: want 1 line", target)
		}
		if rec := g.get(target); rec.Code != http.StatusOK {
			t.Fatalf("%s: = %d", target, rec.Code)
		}
		if n := len(dp.KioskEngine.Basket().Lines); n != 0 {
			t.Fatalf("%s: idle landing must reset the basket, got %d lines", target, n)
		}
		if got := g.selfOrderLang(); got != "" {
			t.Fatalf("%s: language cookie kept: %q", target, got)
		}
	}
}

func TestResetUnlessCommitting(t *testing.T) {
	eng := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, nil)
	eng.AddLineWithModifiers(pos.BasketLine{SKU: "COFFEE", Name: "Flat White", PriceCents: money.FromMinor(320)}, 1, nil)

	if !eng.BeginTenderCommit(eng.TenderAttemptID()) {
		t.Fatal("BeginTenderCommit refused")
	}
	if resetUnlessCommitting(eng) {
		t.Fatal("reset must be refused while the commit point is held")
	}
	if len(eng.Basket().Lines) != 1 {
		t.Fatal("basket must be kept while committing")
	}
	eng.EndTenderCommit()
	if !resetUnlessCommitting(eng) || len(eng.Basket().Lines) != 0 {
		t.Fatal("reset must run once the commit ended")
	}
	// Nests with another abandon (E4).
	if !eng.BeginAbandon() {
		t.Fatal("BeginAbandon refused")
	}
	if !resetUnlessCommitting(eng) {
		t.Fatal("must nest with another abandon")
	}
	eng.EndAbandon()
}
