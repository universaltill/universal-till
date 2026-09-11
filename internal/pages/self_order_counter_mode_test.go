package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
	"github.com/universaltill/universal-till/internal/settings"
)

// TestSelfOrderShop_CounterMode_CheckoutCreatesCounterOrderNoSale is the
// core ut-docs#582 acceptance criterion: in "counter" payment mode,
// checkout must create a kiosk_counter_orders row and NOTHING in
// sales/payments/payment_methods — direct DB assertions, not just an HTTP
// status.
func TestSelfOrderShop_CounterMode_CheckoutCreatesCounterOrderNoSale(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	dp.State.KioskPaymentMode = common.KioskPaymentModeCounter
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

	var salesBefore, paymentsBefore, methodsBefore int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesBefore)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&paymentsBefore)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM payment_methods`).Scan(&methodsBefore)

	rec := post("/api/self-order/checkout", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST checkout (counter mode): want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Order placed") {
		t.Fatalf("expected the confirmation screen, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "payment-picker-method") {
		t.Fatalf("counter-mode checkout must never render payment-picker markup: %s", rec.Body.String())
	}

	var salesAfter, paymentsAfter, methodsAfter int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesAfter)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&paymentsAfter)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM payment_methods`).Scan(&methodsAfter)
	if salesAfter != salesBefore {
		t.Fatalf("counter-mode checkout must create zero sales rows: before=%d after=%d", salesBefore, salesAfter)
	}
	if paymentsAfter != paymentsBefore {
		t.Fatalf("counter-mode checkout must create zero payments rows: before=%d after=%d", paymentsBefore, paymentsAfter)
	}
	if methodsAfter != methodsBefore {
		t.Fatalf("counter-mode checkout must never create a payment_methods row: before=%d after=%d", methodsBefore, methodsAfter)
	}

	var count int
	var displayNo, orderType, linesJSON string
	if err := d.DB.QueryRow(`SELECT display_no, order_type, lines_json FROM kiosk_counter_orders`).Scan(&displayNo, &orderType, &linesJSON); err != nil {
		t.Fatalf("expected exactly one kiosk_counter_orders row: %v", err)
	}
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM kiosk_counter_orders`).Scan(&count)
	if count != 1 {
		t.Fatalf("want exactly 1 kiosk_counter_orders row, got %d", count)
	}
	if !strings.HasPrefix(displayNo, "C-") {
		t.Fatalf("counter order display_no %q must be C--prefixed, never confusable with a sale display number", displayNo)
	}
	if !strings.Contains(linesJSON, "Flat White") {
		t.Fatalf("counter order lines_json missing the ordered item: %s", linesJSON)
	}

	if len(dp.KioskEngine.Basket().Lines) != 0 {
		t.Fatal("basket should be reset after a completed counter-mode checkout")
	}
}

// Review finding (ut-docs#582): the cart's own call-to-action must not
// promise payment in counter mode. "selforder.checkout" is a neutral
// "Checkout" in English but translates to a literal "Pay" in ar/fa/tr, so
// counter mode renders "selforder.counter.place_order" instead. Kiosk mode
// must keep the original label byte-for-byte.
func TestSelfOrderShop_CartButtonLabelFollowsPaymentMode(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      string
		wantKey   string
		refuseKey string
	}{
		{"counter", common.KioskPaymentModeCounter, "selforder.counter.place_order", "selforder.checkout"},
		{"kiosk", common.KioskPaymentModeKiosk, "selforder.checkout", "selforder.counter.place_order"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dp, d := setupSelfOrderShopDeps(t)
			dp.State.KioskPaymentMode = tc.mode
			seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

			mux := http.NewServeMux()
			registerSelfOrderShop(mux, dp)

			req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			mux.ServeHTTP(httptest.NewRecorder(), req)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/cart", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET cart: want 200, got %d: %s", rec.Code, rec.Body.String())
			}
			// Compare the rendered English copy for each key, so this pins
			// the visible text rather than the key name.
			want := httpx.T("en", tc.wantKey)
			refuse := httpx.T("en", tc.refuseKey)
			body := rec.Body.String()
			if !strings.Contains(body, want) {
				t.Fatalf("%s mode: cart button must read %q, got: %s", tc.name, want, body)
			}
			if strings.Contains(body, refuse) {
				t.Fatalf("%s mode: cart button must never read %q: %s", tc.name, refuse, body)
			}
		})
	}
}

// Kiosk (default/explicit "kiosk") mode must be entirely unaffected by this
// card — a strict regression pin.
func TestSelfOrderShop_KioskMode_CheckoutUnchanged(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	dp.State.KioskPaymentMode = common.KioskPaymentModeKiosk // explicit, not just the zero value
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
		t.Fatalf("kiosk mode must still offer the payment picker: %s", rec.Body.String())
	}

	rec2 := post("/api/self-order/checkout", "method=card")
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST checkout: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Order placed") {
		t.Fatalf("expected the confirmation screen, got: %s", rec2.Body.String())
	}

	var salesCount, counterOrderCount int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE status = 'completed'`).Scan(&salesCount)
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM kiosk_counter_orders`).Scan(&counterOrderCount)
	if salesCount != 1 {
		t.Fatalf("kiosk-mode checkout must still create a completed sale, got %d", salesCount)
	}
	if counterOrderCount != 0 {
		t.Fatalf("kiosk-mode checkout must never create a kiosk_counter_orders row, got %d", counterOrderCount)
	}
}

// GET checkout in counter mode must render the counter-confirm partial, not
// the payment picker — and vice versa in kiosk mode. HTTP-level, markup-based.
func TestSelfOrderShop_GetCheckout_ModeSelectsCorrectPartial(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mode         string
		wantMarkup   string
		refuseMarkup string
	}{
		// name="method" is the payment-picker's own distinguishing markup
		// (each payment button submits method=<id>) — the counter-confirm
		// partial has exactly one button and it carries no such attribute.
		{"counter", common.KioskPaymentModeCounter, "Place order", `name="method"`},
		{"kiosk", common.KioskPaymentModeKiosk, `name="method"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dp, d := setupSelfOrderShopDeps(t)
			dp.State.KioskPaymentMode = tc.mode
			seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)

			mux := http.NewServeMux()
			registerSelfOrderShop(mux, dp)

			req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			mux.ServeHTTP(httptest.NewRecorder(), req)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/self-order/checkout", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET checkout: want 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantMarkup) {
				t.Fatalf("%s mode: expected markup %q, got: %s", tc.name, tc.wantMarkup, rec.Body.String())
			}
			if tc.refuseMarkup != "" && strings.Contains(rec.Body.String(), tc.refuseMarkup) {
				t.Fatalf("%s mode must never render %q: %s", tc.name, tc.refuseMarkup, rec.Body.String())
			}
		})
	}
}

// printCounterOrderTicketAsync must build the SAME shape ticket a plain
// print.KitchenTicket/print.RenderKitchenTicket call would for the order's
// own fields — mirrors TestPrintKitchen_ZeroStations_ByteIdenticalLegacyTicket's
// byte-comparison pattern, adapted to a counter order (which has no
// data.SaleDetail to build from).
func TestPrintCounterOrderTicketAsync_MatchesDirectKitchenTicketRender(t *testing.T) {
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "counter-ticket.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbase.Close()

	dp := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB)}
	printerFile := filepath.Join(t.TempDir(), "kitchen.prn")
	if err := os.WriteFile(printerFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(t.Context(), keyPrinterKitchen, printerFile); err != nil {
		t.Fatal(err)
	}

	order := data.KioskCounterOrder{
		ID:        "co-1",
		DisplayNo: "C-1",
		OrderType: "takeaway",
		CreatedAt: "2026-09-11T10:00:00Z",
		Lines: []data.KioskCounterOrderLine{
			{Name: "Flat White", Qty: "2", Modifiers: []string{"Oat milk"}},
			{Name: "Croissant", Qty: "1"},
		},
	}
	printCounterOrderTicketAsync(dp, order)
	dp.WaitForAsyncWork()

	got, err := os.ReadFile(printerFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected a rendered kitchen ticket to have been printed")
	}

	cfg := printerConfig(t.Context(), dp)
	locale := "en"
	want := print.RenderKitchenTicket(print.KitchenTicket{
		Station:    kitchenTicketText(locale, cfg.Charset, "kitchen.ticket.station_default"),
		OrderNo:    "C-1",
		OrderLabel: kitchenTicketText(locale, cfg.Charset, "kitchen.ticket.order_label"),
		OrderType:  kitchenOrderTypeLabel(locale, cfg.Charset, "takeaway"),
		Timestamp:  "2026-09-11T10:00:00Z",
		Charset:    cfg.Charset,
		Items: []print.KitchenItem{
			{Qty: "2", Name: "Flat White", Modifiers: []string{"Oat milk"}},
			{Qty: "1", Name: "Croissant"},
		},
	})
	if string(got) != string(want) {
		t.Fatalf("counter order kitchen ticket bytes mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// A completed counter order must contribute NOTHING to end-of-day totals —
// it is never a sale, so report aggregation should never see it at all.
// Written as an explicit assertion even though it should "fall out for
// free" from the schema (no FK, no money columns).
func TestGenerateEOD_CounterOrderContributesNothing(t *testing.T) {
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "counter-eod.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbase.Close()

	dp := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB)}
	repo := data.NewKioskCounterOrdersRepo(dbase.DB)
	if _, err := repo.Create(t.Context(), data.KioskCounterOrder{
		OrderType: "takeaway",
		Lines:     []data.KioskCounterOrderLine{{Name: "Flat White", Qty: "1"}},
	}); err != nil {
		t.Fatalf("Create counter order: %v", err)
	}

	rep, _, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil {
		t.Fatalf("generateEOD: %v", err)
	}
	if rep.SalesCount != 0 {
		t.Fatalf("SalesCount = %d, want 0 (a counter order is never a sale)", rep.SalesCount)
	}
	if rep.Gross != 0 || rep.Net != 0 {
		t.Fatalf("Gross=%d Net=%d, want both 0", rep.Gross, rep.Net)
	}
}
