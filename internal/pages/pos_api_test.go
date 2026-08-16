package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// newPOSTestDeps wires a Deps with a real scan-resolving Engine (backed by
// the shared seedForPages fixture's itm1/ABC item), matching the
// journal_test.go pattern already used to exercise /api/pos/tender.
func newPOSTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
		// ut-docs#744: mirrors exactly what ui.PriceResolverAdapter produces
		// for a real variant barcode scan -- BOTH ItemID and VariantID set
		// (see internal/ui/resolver_test.go's TestResolve_VariantBarcode).
		"VAR": {SKU: "VAR", Name: "Apple - Large", Qty: 1, PriceCents: 150, ItemID: "itm1", VariantID: "var1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	setStore := settings.NewStore(db)
	state := common.LoadState(t.Context(), setStore, cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: setStore,
	}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	return mux, dp
}

func posPostForm(mux *http.ServeMux, path, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRemoveHandler_ByCodeAndByKey(t *testing.T) {
	mux, dp := newPOSTestDeps(t)

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	rec := posPostForm(mux, "/api/pos/remove", "code=ABC")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 0 {
		t.Fatalf("expected basket empty after remove by code, got %d lines", len(b.Lines))
	}

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("re-seed scan: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	rec = posPostForm(mux, "/api/pos/remove", "key="+key)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if b := dp.Engine.Basket(); len(b.Lines) != 0 {
		t.Fatalf("expected basket empty after remove by key, got %d lines", len(b.Lines))
	}
}

func TestRemoveHandler_RequiresKeyOrCode(t *testing.T) {
	mux, _ := newPOSTestDeps(t)
	rec := posPostForm(mux, "/api/pos/remove", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestLineHandler_UpdatesQtyAndDiscount(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/line", "code=ABC&qty=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	line := dp.Engine.Basket().Lines[0]
	if line.Qty != 3 {
		t.Fatalf("expected qty 3, got %v", line.Qty)
	}

	rec = posPostForm(mux, "/api/pos/line", "code=ABC&qty=3&discount=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	line = dp.Engine.Basket().Lines[0]
	if line.LineDiscount.Minor() != 20 {
		t.Fatalf("expected line discount 20, got %v", line.LineDiscount.Minor())
	}
}

func TestLineHandler_RequiresKeyOrCode(t *testing.T) {
	mux, _ := newPOSTestDeps(t)
	rec := posPostForm(mux, "/api/pos/line", "qty=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDiscountHandler_SetsSaleDiscount(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	rec := posPostForm(mux, "/api/pos/discount", "discount=15")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.SaleDiscount().Minor(); got != 15 {
		t.Fatalf("expected sale discount 15, got %v", got)
	}
}

func TestOrderTypeHandler_TogglesTakeawayAndBack(t *testing.T) {
	mux, dp := newPOSTestDeps(t)

	rec := posPostForm(mux, "/api/pos/order-type", "order_type="+pos.OrderTypeTakeaway)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().OrderType; got != pos.OrderTypeTakeaway {
		t.Fatalf("expected order type takeaway, got %q", got)
	}

	// Any other value -- including "" -- resets to dine-in/standard.
	rec = posPostForm(mux, "/api/pos/order-type", "order_type=")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().OrderType; got != "" {
		t.Fatalf("expected order type reset to dine-in (empty), got %q", got)
	}
}

func TestResetHandler_ClearsBasket(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if len(dp.Engine.Basket().Lines) == 0 {
		t.Fatalf("expected a seeded line before reset")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/reset", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatalf("expected basket empty after reset")
	}
}

func TestTenderHandler_JSONAcceptReturnsSaleSummary(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Envelope: { "data": { saleId, receiptNo, total, ... }, "error": null }
	// (universal-till/CLAUDE.md, ut-docs#387).
	var out struct {
		Data struct {
			SaleID    string `json:"saleId"`
			ReceiptNo string `json:"receiptNo"`
			Total     int64  `json:"total"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got: %v\nbody: %s", err, rec.Body.String())
	}
	if out.Error != nil {
		t.Fatalf("expected error:null on success, got %+v", out.Error)
	}
	if out.Data.SaleID == "" || out.Data.ReceiptNo == "" {
		t.Fatalf("expected saleId and receiptNo populated, got: %+v", out.Data)
	}
	if out.Data.Total != 120 {
		t.Fatalf("expected total 120, got %d", out.Data.Total)
	}
	var subtotal, taxTotal int64
	if err := dp.Db.QueryRow(`SELECT subtotal, tax_total FROM sales WHERE id = ?`, out.Data.SaleID).Scan(&subtotal, &taxTotal); err != nil {
		t.Fatalf("query sale totals: %v", err)
	}
	if subtotal != 100 || taxTotal != 20 {
		t.Fatalf("expected subtotal=100 tax_total=20, got subtotal=%d tax_total=%d", subtotal, taxTotal)
	}
}

// Independent review, ut-docs#543: pos.PaymentInput has no json tags at
// all, so its fields (including the card-present ones this ticket added)
// serialise as bare Go PascalCase in the tender endpoint's JSON response --
// against universal-till/CLAUDE.md's "JSON snake_case" rule, and a second
// wire spelling for the same concept data.SaleDetailPayment already
// correctly tags snake_case. Guard the wire shape directly.
func TestTenderHandler_JSONResponsePaymentsAreSnakeCase(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Payments []map[string]any `json:"payments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got: %v\nbody: %s", err, rec.Body.String())
	}
	if len(out.Data.Payments) != 1 {
		t.Fatalf("expected 1 payment, got %d: %s", len(out.Data.Payments), rec.Body.String())
	}
	p := out.Data.Payments[0]
	for _, key := range []string{"method_id", "amount", "change_given", "tip_amount"} {
		if _, ok := p[key]; !ok {
			t.Fatalf("expected snake_case key %q in payments[0], got keys: %v (body: %s)", key, p, rec.Body.String())
		}
	}
	for _, badKey := range []string{"MethodID", "Amount", "ChangeGiven", "TipAmount", "MaskedPAN", "AuthCode", "TerminalID", "TraceID"} {
		if _, ok := p[badKey]; ok {
			t.Fatalf("found PascalCase key %q in payments[0] JSON -- violates snake_case rule: %v", badKey, p)
		}
	}
}

// ut-docs#744: a variant scanned by barcode resolves with BOTH ItemID and
// VariantID set on the BasketLine (deliberately -- tax_hook.go's
// tax.rate.ask payload still needs ItemID for a variant line), and this
// handler copies both verbatim into pos.SaleLineInput. Before the fix,
// CompleteSale rejected any line with both set, so scanning a variant made
// it completely untenderable through the real /api/pos/tender route. This
// drives the real handler end to end (not just CompleteSale directly) to
// prove the full resolve -> basket -> tender chain works, and that only
// variant_id (never item_id) lands on the persisted sale_lines row.
func TestTenderHandler_VariantBarcodeScanIsTenderable(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("VAR"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	line := dp.Engine.Basket().Lines[0]
	if line.ItemID != "itm1" || line.VariantID != "var1" {
		t.Fatalf("fixture drifted from a real variant scan's shape: %+v", line)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":180}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Data struct {
			SaleID string `json:"saleId"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got: %v\nbody: %s", err, rec.Body.String())
	}
	if out.Error != nil {
		t.Fatalf("expected error:null on success, got %+v", out.Error)
	}

	var itemID, variantID sql.NullString
	if err := dp.Db.QueryRow(`SELECT item_id, variant_id FROM sale_lines WHERE sale_id = ?`, out.Data.SaleID).Scan(&itemID, &variantID); err != nil {
		t.Fatalf("query sale_line: %v", err)
	}
	if itemID.Valid {
		t.Fatalf("expected item_id NULL for a variant line, got %q", itemID.String)
	}
	if !variantID.Valid || variantID.String != "var1" {
		t.Fatalf("expected variant_id = var1, got %+v", variantID)
	}

	var qty float64
	if err := dp.Db.QueryRow(`SELECT quantity FROM inventory WHERE variant_id = 'var1' AND location_id = 'loc_main'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 29 {
		t.Fatalf("expected variant inventory 29 (started at 30, sold 1), got %v", qty)
	}
}

// The cashier tender endpoint shares completeTender with the self-order
// kiosk (self_order_shop_test.go has the kiosk-side version of this test);
// this proves the plugin-reported-tip path also reaches a sale placed
// through /api/pos/tender directly, not just through the kiosk handler —
// an independent review flagged that only the kiosk path had coverage,
// and that the fix's persistence actually depended on undocumented slice
// aliasing between completeTender's two parameters (since fixed by setting
// saleInput.Payments = payments explicitly in completeTender itself).
func TestTenderHandler_AppliesPluginReportedTipFromAuthorizeResponse(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demo', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', 'auth', 'site', '[]', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.payment-demo', 'Demo Pay', '1.0.0', 'demo.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.payment-demo', 'demopay', 'Demo Pay', 'payment', 'payment.demopay.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.payment-demo', 'payment.demopay.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.payment-demo', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers() // process-global singleton; isolate from other tests using the same plugin id/event
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"provider":"demopay","outcome":"approved","tip_amount":150}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"demopay","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			SaleID string `json:"saleId"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got: %v\nbody: %s", err, rec.Body.String())
	}
	if out.Error != nil {
		t.Fatalf("expected error:null on success, got %+v", out.Error)
	}

	var tip int64
	if err := dp.Db.QueryRow(`SELECT tip_amount FROM payments WHERE sale_id = ?`, out.Data.SaleID).Scan(&tip); err != nil {
		t.Fatalf("expected a payment row for the sale: %v", err)
	}
	if tip != 150 {
		t.Fatalf("want tip_amount 150 (from the plugin's authorize response) on the cashier tender path, got %d", tip)
	}
}

func TestTenderHandler_InvalidJSONBodyRejected(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{not valid json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed JSON body, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTenderHandler_QuickTenderFormFallback(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	// Use "card", not the "cash" fallback method: the handler falls back to
	// a default cash payment whenever len(payments)==0 (pos_api.go), so a
	// "cash" tender here would still pass even if form-method parsing were
	// broken and silently ignored -- exactly the regression called out in
	// pos_api.go's own comment about quick-tender buttons ("silently
	// recorded cash whatever method was tapped").
	rec := posPostForm(mux, "/api/pos/tender", "method=card&amount=120&offline=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM payments WHERE method_id='card'`).Scan(&count); err != nil {
		t.Fatalf("query payments: %v", err)
	}
	if count == 0 {
		t.Fatalf("expected a card payment recorded from the form fallback")
	}
}

// ut-docs#72, regression found by independent review: the quick-tender
// buttons (web/ui/pages/index.html) always POST amount=0, relying on the
// handler's zero-amount-payment fallback to fill in the real total. That
// fallback used to compute its own local total WITHOUT the service
// charge, so setting a non-zero rate broke every quick-tender button with
// a 400 "payments do not cover total" -- the feature couldn't be turned
// on at all. This proves the real HTTP path, not just the engine.
func TestTenderHandler_QuickTenderCoversServiceCharge(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.ServiceChargeRateBasisPoints = 1000 })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	// Exactly the shape a shipped quick-tender button posts: amount=0,
	// relying on the server to fill in the real (inflated) total.
	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (quick-tender must cover the service charge on its own), got %d: %s", rec.Code, rec.Body.String())
	}

	// ABC: price 100, 20% tax = 20, 10% service charge on the 100
	// subtotal = 10 -> total 130.
	var total, serviceCharge int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales`).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if serviceCharge != 10 {
		t.Fatalf("expected service_charge_amount 10, got %d", serviceCharge)
	}
	if total != 130 {
		t.Fatalf("expected total 130 (100 + 20 tax + 10 service charge), got %d", total)
	}
	var paymentAmount int64
	if err := dp.Db.QueryRow(`SELECT amount FROM payments`).Scan(&paymentAmount); err != nil {
		t.Fatalf("query payment: %v", err)
	}
	if paymentAmount != 130 {
		t.Fatalf("expected the zero-amount payment to be filled in as 130, got %d", paymentAmount)
	}
}

// ut-docs#244: a fractional rate like the UK's standard 12.5% must compute
// the exact basis-point amount end-to-end, not the 12%/13% a whole-percent
// field would truncate to.
func TestTenderHandler_QuickTenderCoversFractionalServiceCharge(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.ServiceChargeRateBasisPoints = 1250 })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// ABC: price 100, 20% tax = 20, 12.5% service charge on the 100
	// subtotal = 12.5 -> rounds to 13 (half-up) -> total 133.
	var total, serviceCharge int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales`).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if serviceCharge != 13 {
		t.Fatalf("expected service_charge_amount 13 (12.5%% of 100, half-up), got %d", serviceCharge)
	}
	if total != 133 {
		t.Fatalf("expected total 133 (100 + 20 tax + 13 service charge), got %d", total)
	}
}

func TestTenderHandler_EmptyBasketRejected(t *testing.T) {
	mux, _ := newPOSTestDeps(t)
	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=100")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty basket, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTenderHandler_SimulatedFailureRecordsAuditAndReturns502(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"simulateFailure":true,"failureReason":"card reader offline"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='payment_failed'`).Scan(&count); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if count == 0 {
		t.Fatalf("expected a payment_failed audit row")
	}
	// The basket must survive a simulated failure -- retrying the same
	// tender is the whole point (docs: the till never loses a basket on a
	// declined/offline card read).
	if len(dp.Engine.Basket().Lines) == 0 {
		t.Fatalf("expected basket lines to survive a simulated payment failure")
	}
}

func TestTenderHandler_InsufficientStockShowsToastAndKeepsBasket(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	// itm1 has 50 units at loc_main (seedForPages); scan far more than
	// that with negative inventory disallowed to force the insufficient-
	// stock path rather than actually completing an oversell. The payment
	// below (120000) must exactly cover the qty-1000 total, or the
	// payment-coverage check rejects the tender first and this never
	// reaches the stock check at all.
	if _, err := dp.Engine.ScanQty("ABC", 1000); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120000}],"allowNegativeInventory":false,"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast, not an error status), got %d: %s", rec.Code, rec.Body.String())
	}
	// ut-docs#213: the unified notice surface replaced the hand-built
	// #toast-error overlay — an insufficient-stock rejection renders as a
	// persistent error-level pos-notice inside the basket swap.
	if !strings.Contains(rec.Body.String(), "pos-notice error") {
		t.Fatalf("expected an error-level pos-notice, got: %s", rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) == 0 {
		t.Fatalf("expected basket to survive an insufficient-stock rejection")
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("query sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sale to be recorded when stock was insufficient, got %d", count)
	}
}

func TestLooksLikeCustomerCode(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"", false},
		{"CUST123", true},
		{"cust123", true},
		{"LOY-99", true},
		{"LOY42", true},
		{"LOYALTY", false}, // "LOY" prefix but next char isn't a digit
		{"ABC123", false},
	}
	for _, c := range cases {
		if got := looksLikeCustomerCode(c.code); got != c.want {
			t.Errorf("looksLikeCustomerCode(%q) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestNormalizeLegalLines(t *testing.T) {
	got := normalizeLegalLines("  Line A  \n\nLine B\n", []string{"Pre1", "  ", "Pre2"})
	want := []string{"Pre1", "Pre2", "Line A", "Line B"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestStockMovementReason(t *testing.T) {
	cases := map[string]string{
		"receive": "received",
		"adjust":  "adjustment",
		"sale":    "sale",
		"unknown": "unknown",
	}
	for in, want := range cases {
		if got := stockMovementReason(in); got != want {
			t.Errorf("stockMovementReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// pluginReportedTipAmount must only ever apply a real, well-formed,
// non-negative tip a plugin reports on its authorize response — anything
// else (no response, no field, a bad type, a negative amount) must leave
// the tender's own tip untouched rather than corrupting or inventing one.
func TestPluginReportedTipAmount(t *testing.T) {
	cases := []struct {
		name    string
		resp    json.RawMessage
		wantAmt int64
		wantOK  bool
	}{
		{"empty response", nil, 0, false},
		{"no tip field", json.RawMessage(`{"provider":"sumup","outcome":"approved"}`), 0, false},
		{"zero tip", json.RawMessage(`{"tip_amount":0}`), 0, true},
		{"positive tip", json.RawMessage(`{"tip_amount":150}`), 150, true},
		{"negative tip ignored", json.RawMessage(`{"tip_amount":-50}`), 0, false},
		{"malformed json ignored", json.RawMessage(`not json`), 0, false},
		{"wrong type ignored", json.RawMessage(`{"tip_amount":"lots"}`), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			amt, ok := pluginReportedTipAmount(c.resp)
			if ok != c.wantOK || (ok && amt != c.wantAmt) {
				t.Fatalf("pluginReportedTipAmount(%s) = (%d, %v), want (%d, %v)", c.resp, amt, ok, c.wantAmt, c.wantOK)
			}
		})
	}
}
