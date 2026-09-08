package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// discInputValueRE extracts the per-line discount box's redisplayed value
// from rendered basket HTML (web/ui/partials/basket.html's disc-input),
// used by TestLineHandler_QtyChangeDoesNotClearDiscount (ut-docs#971) to
// assert on exactly what a browser would re-submit via hx-include.
var discInputValueRE = regexp.MustCompile(`class="disc-input"[^>]*value="([^"]*)"`)

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
	// Same charge-policy seam init.go wires in production (ADR-0061) — the
	// tender handler consults d.Engine.ChargePolicy(), which answers only
	// through this asker.
	engine.SetChargePolicyAsker(&pluginChargePolicyAsker{db: db})

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
	// Registered AFTER the db.Close cleanup above, so LIFO order runs this
	// FIRST: any printReceiptAsync/printKitchenAsync goroutine a test's
	// tender started (ut-docs#425, #514) finishes before Close and
	// TempDir removal can race its still-in-flight Db/Settings access.
	t.Cleanup(dp.WaitForAsyncWork)
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

// TestLineHandler_QtyChangeDoesNotClearDiscount is a regression test for
// ut-docs#971: the disc-input box's hx-include="closest tr" (basket.html)
// means changing a line's quantity re-submits the whole row, including
// whatever value the discount box currently redisplays. That redisplayed
// value must round-trip through this same handler's minor-units ParseInt,
// or the discount silently resets to 0 with no error shown to the
// operator.
func TestLineHandler_QtyChangeDoesNotClearDiscount(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/line", "code=ABC&qty=3&discount=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	line := dp.Engine.Basket().Lines[0]
	if line.LineDiscount.Minor() != 20 {
		t.Fatalf("expected line discount 20 after initial set, got %v", line.LineDiscount.Minor())
	}

	// Extract the disc-input's redisplayed value exactly as the browser
	// would see it in the rendered basket HTML.
	m := discInputValueRE.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("could not find disc-input value in rendered basket:\n%s", rec.Body.String())
	}
	redisplayed := m[1]

	// Simulate hx-include="closest tr": the qty change re-submits the row,
	// carrying the discount box's currently redisplayed value untouched —
	// the operator never re-typed the discount.
	rec = posPostForm(mux, "/api/pos/line", "code=ABC&qty=5&discount="+redisplayed)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	line = dp.Engine.Basket().Lines[0]
	if line.Qty != 5 {
		t.Fatalf("expected qty 5, got %v", line.Qty)
	}
	if line.LineDiscount.Minor() != 20 {
		t.Fatalf("expected line discount to survive the quantity change at 20, got %v (redisplayed value was %q)", line.LineDiscount.Minor(), redisplayed)
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

// TestTableHandler_AssignsResolvesLabelAndClears (ut-docs#820) mirrors
// TestOrderTypeHandler_TogglesTakeawayAndBack's shape: POST a table_id,
// the handler resolves its current label and stamps both onto the live
// basket; posting an empty table_id clears the assignment.
func TestTableHandler_AssignsResolvesLabelAndClears(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	tableID, err := data.NewPOSRepo(dp.Db).CreateTable(context.Background(), "T5", "Terrace", 4, "rect", 200, 200)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/table", "table_id="+tableID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != tableID {
		t.Fatalf("expected TableID %q, got %q", tableID, got)
	}
	if got := dp.Engine.Basket().TableLabel; got != "T5" {
		t.Fatalf("expected TableLabel resolved to T5, got %q", got)
	}
	// The label itself renders in the table-picker fragment (which the basket
	// re-loads on every swap), not inline in the basket partial -- so a
	// no-tables shop pays no basket height for table chrome (ADR-0054
	// soft-gate; ut-docs#820 review B fix). Assert the re-rendered basket
	// wires that fragment; the label text is covered by the picker's own
	// tests (TestTablePicker_*).
	if !strings.Contains(rec.Body.String(), `hx-get="/ui/pos/table-picker"`) {
		t.Fatalf("expected the re-rendered basket to load the table picker, got: %s", rec.Body.String())
	}

	rec = posPostForm(mux, "/api/pos/table", "table_id=")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("expected TableID cleared, got %q", got)
	}
	if got := dp.Engine.Basket().TableLabel; got != "" {
		t.Fatalf("expected TableLabel cleared, got %q", got)
	}
}

// An unknown/garbage table_id must not silently stamp a bogus label onto
// the basket -- it degrades to "no table assigned", the same way an
// unrecognized order_type value degrades to dine-in.
func TestTableHandler_UnknownTableIDIgnored(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	rec := posPostForm(mux, "/api/pos/table", "table_id=does-not-exist")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("expected an unknown table_id to be ignored, got TableID %q", got)
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

	// min_pos_version/api_version/published_at are NOT NULL on the real
	// plugin_catalog table (ut-docs#1677).
	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demo', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
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

// TestTenderHandler_NonOKCPluginDoesNotReceiveBasketDetail is the
// regression test for ut-docs#1766: completeTender used to merge
// deviceAuthorizePayloadExtras (basket lines, discounts, tax rates) into
// EVERY payment plugin's authorize payload, not just the tax-tr (ÖKC)
// plugin's. A plugin that only ever asked for method/amount/reference
// must not see the sale's line detail.
func TestTenderHandler_NonOKCPluginDoesNotReceiveBasketDetail(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demo', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
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
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	var received map[string]any
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			if err := json.Unmarshal(ev.Payload, &received); err != nil {
				t.Fatalf("unmarshal authorize payload: %v", err)
			}
			return json.RawMessage(`{"provider":"demopay","outcome":"approved"}`), nil
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

	if received == nil {
		t.Fatal("plugin never received an authorize call")
	}
	for _, leaked := range []string{"lines", "sale_discount", "service_charge", "tax_inclusive", "currency", "total"} {
		if _, ok := received[leaked]; ok {
			t.Fatalf("demopay is not the fiscal-device plugin — its authorize payload must not carry %q, got %+v", leaked, received)
		}
	}
	for _, want := range []string{"method", "amount", "reference"} {
		if _, ok := received[want]; !ok {
			t.Fatalf("authorize payload dropped the base contract field %q, got %+v", want, received)
		}
	}
}

// TestTenderHandler_OKCPluginReceivesBasketDetail is the other half of the
// ut-docs#1766 regression test: the ONE payment method that legitimately
// needs basket/line detail (tax-tr's "okc" entry key, fiscal.MethodKeyOKC)
// must keep receiving it — this is not a permission grant/manifest change,
// so an existing TR install must not regress.
func TestTenderHandler_OKCPluginReceivesBasketDetail(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.tax-tr', '1.0.0', 'Turkiye fiscal device', 'okc', 'wasm', 'plugin.wasm', 'https://example.test/tax-tr.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.tax-tr', 'Turkiye fiscal device', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.tax-tr', 'okc', 'Yazarkasa (OKC)', 'payment', 'payment.okc.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.tax-tr', 'payment.okc.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.tax-tr', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.okc.authorize", plugins.Blocking)
	var received map[string]any
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-tr",
		[]string{"payment.okc.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			if err := json.Unmarshal(ev.Payload, &received); err != nil {
				t.Fatalf("unmarshal authorize payload: %v", err)
			}
			return json.RawMessage(`{"provider":"okc","outcome":"approved","fiscal_device":{"receipt_no":"12345"}}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"okc","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if received == nil {
		t.Fatal("plugin never received an authorize call")
	}
	if _, ok := received["lines"]; !ok {
		t.Fatalf("the fiscal-device (okc) plugin must still receive basket line detail, got %+v", received)
	}
}

// TestTenderHandler_OKCPluginApprovesWithNoEvidence_SaleRefused is
// ut-docs#1779: a fiscal-device (OKC) plugin that answers "approved" with no
// `fiscal_device` object (or an invalid one) must not be able to complete a
// TR sale. MethodKeyOKC's own doc comment claims "fail-closed by
// construction" purely from the plugin refusing a bad tender itself — this
// proves core has an INDEPENDENT backstop that doesn't rely on the plugin
// behaving, mirroring TestTenderHandler_OKCPluginReceivesBasketDetail's
// harness but with the plugin's answer missing the receipt.
func TestTenderHandler_OKCPluginApprovesWithNoEvidence_SaleRefused(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.tax-tr', '1.0.0', 'Turkiye fiscal device', 'okc', 'wasm', 'plugin.wasm', 'https://example.test/tax-tr.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.tax-tr', 'Turkiye fiscal device', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.tax-tr', 'okc', 'Yazarkasa (OKC)', 'payment', 'payment.okc.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.tax-tr', 'payment.okc.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.tax-tr', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.okc.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-tr",
		[]string{"payment.okc.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			// Approved (nil error = not a decline), but no `fiscal_device`
			// object at all -- the exact shape a buggy/malicious OKC plugin
			// could return.
			return json.RawMessage(`{"provider":"okc","outcome":"approved"}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"okc","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 (declined -- no fiscal-device evidence), got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("query sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sale to be recorded when the OKC plugin approved with no receipt evidence, got %d", count)
	}
}

// installTaxTRPlugin seeds the com.universaltill.tax-tr plugin's catalog/
// plugin/entry/hook/permission rows — the same fixture shape
// TestTenderHandler_OKCPluginApprovesWithNoEvidence_SaleRefused above hand-
// rolls once; factored out here so ut-docs#1768's tests below (three of
// them) don't triple that boilerplate.
func installTaxTRPlugin(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.tax-tr', '1.0.0', 'Turkiye fiscal device', 'okc', 'wasm', 'plugin.wasm', 'https://example.test/tax-tr.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.tax-tr', 'Turkiye fiscal device', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e-taxtr', 'com.universaltill.tax-tr', 'okc', 'Yazarkasa (OKC)', 'payment', 'payment.okc.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h-taxtr', 'com.universaltill.tax-tr', 'payment.okc.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p-taxtr', 'com.universaltill.tax-tr', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}
}

// installDemoPayPlugin seeds a non-fiscal-device payment plugin (the same
// fixture shape TestTenderHandler_SplitTender_EarlierLegEvidenceCannotCoverMissingOKCReceipt
// below hand-rolls) -- used by ut-docs#1768's forged-evidence regression
// test to prove a plugin other than com.universaltill.tax-tr cannot
// satisfy the per-sale OKC-leg requirement merely by echoing a
// fiscal_device object.
func installDemoPayPlugin(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demopay', 'wasm', 'plugin.wasm', 'https://example.test/demopay.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.payment-demo', 'Demo Pay', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e-demopay', 'com.universaltill.payment-demo', 'demopay', 'Demo Pay', 'payment', 'payment.demopay.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h-demopay', 'com.universaltill.payment-demo', 'payment.demopay.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p-demopay', 'com.universaltill.payment-demo', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}
}

// setTRSystemOfRecordConfigured puts the shop in Turkey, hard-gated,
// system-of-record, with a confirmed and healthy signing device — the
// fiscal.Allowed posture ut-docs#1768's per-sale check layers on top of.
// Reloads dp.State so d.CurrentState().Country reflects the write, exactly
// as every production settings writer does (TestFiscalSettings_
// CountryChangeClearsSigningDeviceFlags above uses the same pattern).
func setTRSystemOfRecordConfigured(t *testing.T, dp *common.Deps) {
	t.Helper()
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, common.KeyCountry, "TR"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeySystemOfRecord, "true"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.SigningDeviceConfiguredKey("TR"), "true"); err != nil {
		t.Fatal(err)
	}
	dp.State = common.LoadState(ctx, dp.Settings, dp.Cfg)
}

// TestTenderHandler_TRSystemOfRecordCashOnly_NoDeviceEvidence_Refused is
// ut-docs#1768: the TR hard gate (ADR-0048/#1208) is a one-time posture
// flag proving the shop's device has EVER printed, not that THIS sale used
// it. A system-of-record TR shop whose device is confirmed and healthy
// (fiscal.Allowed — the gate itself would let this tender through) must
// still be refused here if the sale never touched fiscal.MethodKeyOKC at
// all: a plain cash tender obtains no mali fiş, exactly the gap the
// ut-docs#1750 review recorded as "cash included — a cashier tapping Nakit
// completes a sale with no mali fiş, no marker and no receipt notice."
func TestTenderHandler_TRSystemOfRecordCashOnly_NoDeviceEvidence_Refused(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	setTRSystemOfRecordConfigured(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 (refused -- TR system-of-record sale with no fiscal-device leg), got %d: %s", rec.Code, rec.Body.String())
	}
	// Independent review: assert the SPECIFIC refusal copy, not just the
	// status code -- paymentDeclinedError also maps to 402, and a test
	// that can't tell the two apart would still pass if this check were
	// silently replaced by an unrelated decline.
	if !strings.Contains(rec.Body.String(), "fiscal device") {
		t.Fatalf("expected the #1779/#1768 fiscal-device refusal copy, got: %s", rec.Body.String())
	}

	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("query sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sale to be recorded for a TR system-of-record cash-only tender, got %d", count)
	}
}

// TestTenderHandler_TRSystemOfRecordForgedEvidenceFromNonOKCPlugin_StillRefused is
// the independent-review BLOCKER-1 regression: the check must gate on
// PAYMENT-LEG IDENTITY (was any leg's MethodID == fiscal.MethodKeyOKC),
// never on the sale-wide deviceEvidence accumulator. That accumulator
// (pickDeviceEvidence) takes evidence from ANY method's response with no
// MethodID check of its own -- a non-OKC plugin (card/QR/demo) can return
// a `fiscal_device` object, forged or otherwise, and an evidence-based
// version of this check would have let it launder a sale that used NO real
// OKC leg at all. Confirmed empirically against an earlier, evidence-based
// draft of this fix: HTTP 200, one sale row, zero OKC legs.
func TestTenderHandler_TRSystemOfRecordForgedEvidenceFromNonOKCPlugin_StillRefused(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	setTRSystemOfRecordConfigured(t, dp)
	installDemoPayPlugin(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			// demopay is NOT fiscal.MethodKeyOKC, but nothing stops its
			// answer from carrying a fiscal_device object -- plugin-
			// controlled JSON, parsed unconditionally.
			return json.RawMessage(`{"provider":"demopay","outcome":"approved","fiscal_device":{"receipt_no":"NOT-A-REAL-OKC-RECEIPT"}}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"demopay","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 -- no leg used fiscal.MethodKeyOKC, a non-OKC plugin's forged evidence must not launder this sale, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 0 {
		t.Fatalf("expected no sale to be recorded, got %d", got)
	}
}

// TestTenderHandler_TRSystemOfRecordCardOnly_RefusedBeforeAuthorize is the
// independent-review BLOCKER-2 regression: a shop lacking any
// fiscal.MethodKeyOKC leg is provably knowable from the DECLARED payment
// methods alone, before any plugin's payment.<key>.authorize round trip —
// so the refusal must happen BEFORE that call, never after a card (or
// other) plugin has already captured the customer's money with no way to
// unwind it. Proven here by asserting the card plugin's authorize hook is
// never invoked at all, not merely that the sale is refused.
func TestTenderHandler_TRSystemOfRecordCardOnly_RefusedBeforeAuthorize(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	setTRSystemOfRecordConfigured(t, dp)
	installDemoPayPlugin(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	authorizeCalls := 0
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			authorizeCalls++
			return json.RawMessage(`{"provider":"demopay","outcome":"approved"}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"demopay","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d: %s", rec.Code, rec.Body.String())
	}
	if authorizeCalls != 0 {
		t.Fatalf("expected the per-sale OKC-leg check to refuse BEFORE any payment.<key>.authorize round trip -- demopay's authorize hook was called %d time(s), which would mean the customer's money could already be taken", authorizeCalls)
	}
	if got := countSales(t, dp); got != 0 {
		t.Fatalf("expected no sale to be recorded, got %d", got)
	}
}

// TestTenderHandler_TRSystemOfRecordOverride_CashOnly_AllowedAndAudited
// covers the AllowedWithOverride posture ut-docs#1768's own check
// deliberately does not re-block: a device confirmed but currently
// FAILING, with an active owner override, must still complete a cash-only
// sale -- the override's existing unsigned_override audit marker (fired
// unconditionally of country, pos_api.go's completeTender) already
// satisfies "recorded in a way that is honestly distinguishable" per the
// card's own acceptance criteria, so this posture must not be hard-blocked
// on top of that.
func TestTenderHandler_TRSystemOfRecordOverride_CashOnly_AllowedAndAudited(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	ctx := context.Background()
	setTRSystemOfRecordConfigured(t, dp)
	if err := dp.Settings.Set(ctx, fiscal.SigningDeviceFailingSinceKey("TR"), "2026-09-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeyOverrideUntil, time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeyOverrideReason, "device outage"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeyOverrideActor, "user1"); err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("expected 200 -- an active owner override permits a cash sale while the device is known-failing, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected exactly 1 sale, got %d", got)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='sale' AND action='unsigned_override'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected the sale to carry its own unsigned_override audit marker (honestly-distinguishable record), got %d", n)
	}
}

// TestTenderHandler_DESystemOfRecordCashOnly_NotBlocked is the independent-
// review MAJOR-3 regression: fiscal.RequiresPerSaleDeviceReceipt is scoped
// to Turkey only (Germany's TSE already signs/declares per sale via
// fiscal.sign.ask, ADR-0044) -- a German system-of-record shop with a
// confirmed, healthy TSE must keep completing plain cash sales exactly as
// before this card, proving ut-docs#1768's check cannot regress into
// blocking every hard-gated market's cash tenders.
func TestTenderHandler_DESystemOfRecordCashOnly_NotBlocked(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, common.KeyCountry, "DE"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeySystemOfRecord, "true"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.SigningDeviceConfiguredKey("DE"), "true"); err != nil {
		t.Fatal(err)
	}
	dp.State = common.LoadState(ctx, dp.Settings, dp.Cfg)
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
		t.Fatalf("expected 200 -- ut-docs#1768's per-sale check must not apply to DE, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected exactly 1 sale, got %d", got)
	}
}

// TestTenderHandler_TRSystemOfRecordWithOKCEvidence_Allowed is the
// companion positive case: the SAME posture (system-of-record, device
// confirmed and healthy) with a payment leg that actually went through
// fiscal.MethodKeyOKC and returned valid evidence must still complete
// normally -- ut-docs#1768 must not turn every TR sale into a hard block,
// only the ones that genuinely bypassed the device.
func TestTenderHandler_TRSystemOfRecordWithOKCEvidence_Allowed(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	setTRSystemOfRecordConfigured(t, dp)
	installTaxTRPlugin(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.okc.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-tr",
		[]string{"payment.okc.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"provider":"okc","outcome":"approved","fiscal_device":{"receipt_no":"OKC-0001"}}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"okc","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 -- the sale carried valid OKC device evidence, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("query sales: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 sale, got %d", count)
	}
}

// TestTenderHandler_TRShadowMode_CashOnly_NotBlocked is the non-regression
// case turkey-compliance.md §8 names explicitly: "Shadow mode … no new
// fiscal-device blocker found — our software is not the point of fiscal
// record in that mode." A TR shop that has NOT declared
// fiscal.system_of_record (shadow/trial/demo) must keep completing
// cash-only sales exactly as before ut-docs#1768 -- the new per-sale check
// is scoped to a live, system-of-record shop only.
func TestTenderHandler_TRShadowMode_CashOnly_NotBlocked(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, common.KeyCountry, "TR"); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT setting fiscal.KeySystemOfRecord or the TR
	// signing_device_configured row -- shadow mode, nothing confirmed.
	dp.State = common.LoadState(ctx, dp.Settings, dp.Cfg)
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
		t.Fatalf("expected 200 -- a shadow-mode TR shop is not gated, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestTenderHandler_SplitTender_EarlierLegEvidenceCannotCoverMissingOKCReceipt
// is ut-docs#1779's independent-review finding (BLOCKER 1): the fail-closed
// check must gate on the OKC leg's OWN parsed response, never the sale-wide
// deviceEvidence accumulator — pickDeviceEvidence keeps first-wins evidence
// from ANY payment method's response (plugin-controlled JSON, parsed with no
// MethodID check of its own), so a non-OKC leg that happens to carry a
// `fiscal_device` object must not "cover" a LATER OKC leg that returned
// none. Confirmed by the reviewer to bypass the original (accumulator-
// gated) version of this fix: HTTP 200, one sale row.
func TestTenderHandler_SplitTender_EarlierLegEvidenceCannotCoverMissingOKCReceipt(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demopay', 'wasm', 'plugin.wasm', 'https://example.test/demopay.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.payment-demo', 'Demo Pay', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
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

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.tax-tr', '1.0.0', 'Turkiye fiscal device', 'okc', 'wasm', 'plugin.wasm', 'https://example.test/tax-tr.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.tax-tr', 'Turkiye fiscal device', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e2', 'com.universaltill.tax-tr', 'okc', 'Yazarkasa (OKC)', 'payment', 'payment.okc.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h2', 'com.universaltill.tax-tr', 'payment.okc.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p2', 'com.universaltill.tax-tr', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	bus.SetEventMode("payment.okc.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			// demopay is NOT a fiscal-device method, but nothing stops a
			// plugin's answer from carrying a `fiscal_device` object -- it
			// is plugin-controlled JSON, parsed unconditionally.
			return json.RawMessage(`{"provider":"demopay","outcome":"approved","fiscal_device":{"receipt_no":"NOT-A-REAL-OKC-RECEIPT"}}`), nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-tr",
		[]string{"payment.okc.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"provider":"okc","outcome":"approved"}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"demopay","amount":20},{"method":"okc","amount":100}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 (declined -- the demopay leg's evidence must not cover the okc leg's missing receipt), got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("query sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sale to be recorded -- the okc leg itself returned no receipt evidence, got %d", count)
	}
}

// TestTenderHandler_SplitTender_SecondOKCLegWithNoEvidenceRefused is
// ut-docs#1779's independent-review finding (BLOCKER 1), the other half:
// pickDeviceEvidence's first-wins rule means a FIRST okc leg's valid receipt
// must not cover a SECOND okc leg that returned none. pickDeviceEvidence's
// own doc comment justifies first-wins on "the plugin refuses a split
// tender" -- a real OKC plugin's own behaviour, not something core enforces
// -- so this is exactly the "don't rely on the plugin policing itself" gap
// ut-docs#1779 exists to close.
func TestTenderHandler_SplitTender_SecondOKCLegWithNoEvidenceRefused(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.tax-tr', '1.0.0', 'Turkiye fiscal device', 'okc', 'wasm', 'plugin.wasm', 'https://example.test/tax-tr.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.tax-tr', 'Turkiye fiscal device', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.tax-tr', 'okc', 'Yazarkasa (OKC)', 'payment', 'payment.okc.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.tax-tr', 'payment.okc.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.tax-tr', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.okc.authorize", plugins.Blocking)
	var calls int
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-tr",
		[]string{"payment.okc.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			if calls == 1 {
				return json.RawMessage(`{"provider":"okc","outcome":"approved","fiscal_device":{"receipt_no":"12345"}}`), nil
			}
			// Second leg: approved, but no receipt -- a real OKC plugin is
			// supposed to refuse a split tender itself, but core must not
			// depend on that.
			return json.RawMessage(`{"provider":"okc","outcome":"approved"}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"okc","amount":60},{"method":"okc","amount":60}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 (declined -- the second okc leg returned no receipt evidence), got %d: %s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("expected both okc legs to be authorized, got %d call(s)", calls)
	}

	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("query sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sale to be recorded -- the second okc leg returned no receipt evidence, got %d", count)
	}
}

// TestTenderHandler_OKCPluginReceivesNetOfChangeAmount is ut-docs#1764: a
// cash-with-change OKC tender must tell the device the actual sale amount
// (120, ABC's seeded price+tax), not the 200 gross tendered before the 80
// change was handed back — the device prints "total", and a fiscal receipt
// for more than the sale actually cost is a real compliance problem, not
// cosmetic.
func TestTenderHandler_OKCPluginReceivesNetOfChangeAmount(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.tax-tr', '1.0.0', 'Turkiye fiscal device', 'okc', 'wasm', 'plugin.wasm', 'https://example.test/tax-tr.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.tax-tr', 'Turkiye fiscal device', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.tax-tr', 'okc', 'Yazarkasa (OKC)', 'payment', 'payment.okc.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.tax-tr', 'payment.okc.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.tax-tr', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.okc.authorize", plugins.Blocking)
	var received map[string]any
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-tr",
		[]string{"payment.okc.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			if err := json.Unmarshal(ev.Payload, &received); err != nil {
				t.Fatalf("unmarshal authorize payload: %v", err)
			}
			return json.RawMessage(`{"provider":"okc","outcome":"approved","fiscal_device":{"receipt_no":"12345"}}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	// Sale total is 120 (ABC's 100 + 20% tax). Customer hands over 200 cash,
	// gets 80 change.
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"okc","amount":200,"change":80}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if received == nil {
		t.Fatal("plugin never received an authorize call")
	}
	if got, want := received["amount"], float64(120); got != want {
		t.Fatalf("plugin's \"amount\" = %v, want %v (net of change, not the 200 gross tender)", got, want)
	}
	if got, want := received["total"], float64(120); got != want {
		t.Fatalf("plugin's \"total\" = %v, want %v (net of change, not the 200 gross tender)", got, want)
	}
}

// TestTenderHandler_RejectsChangeGreaterThanAmount is ut-docs#1764's
// independent-review finding: pos.CompleteSale's netPayments already
// rejects change > amount as invalid (internal/pos/sales.go), but that
// check only runs AFTER completeTender's plugin-authorize round trip --
// so before this test's fix, a malformed request reached the OKC fiscal
// device with a negative "amount"/"total" (amount minus an
// impossibly-large change) before the till's own validation ever fired.
// A legally-binding fiscal receipt can't be un-printed, so this must be
// rejected before any plugin ever sees it, not just before the sale
// persists.
func TestTenderHandler_RejectsChangeGreaterThanAmount(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.tax-tr', '1.0.0', 'Turkiye fiscal device', 'okc', 'wasm', 'plugin.wasm', 'https://example.test/tax-tr.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.tax-tr', 'Turkiye fiscal device', '1.0.0', 'plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.tax-tr', 'okc', 'Yazarkasa (OKC)', 'payment', 'payment.okc.requested', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.tax-tr', 'payment.okc.authorize', 'handle_authorize', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.tax-tr', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.okc.authorize", plugins.Blocking)
	pluginCalled := false
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-tr",
		[]string{"payment.okc.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			pluginCalled = true
			return json.RawMessage(`{"provider":"okc","outcome":"approved","fiscal_device":{"receipt_no":"12345"}}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	// change (250) exceeds amount (200) -- impossible, and never legitimate.
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"okc","amount":200,"change":250}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if pluginCalled {
		t.Fatal("the fiscal device must never be called with an impossible change > amount -- this must be rejected before the authorize round trip, not after")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for change > amount, got %d: %s", rec.Code, rec.Body.String())
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
	// subtotal = 10, itself taxed at the sale's blended 20% rate = 2
	// (ADR-0061 — the charge is never untaxed) -> total 132.
	var total, serviceCharge int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales`).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if serviceCharge != 10 {
		t.Fatalf("expected service_charge_amount 10, got %d", serviceCharge)
	}
	if total != 132 {
		t.Fatalf("expected total 132 (100 + 22 tax + 10 service charge), got %d", total)
	}
	var paymentAmount int64
	if err := dp.Db.QueryRow(`SELECT amount FROM payments`).Scan(&paymentAmount); err != nil {
		t.Fatalf("query payment: %v", err)
	}
	if paymentAmount != 132 {
		t.Fatalf("expected the zero-amount payment to be filled in as 132, got %d", paymentAmount)
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
	// subtotal = 12.5 -> rounds to 13 (half-up); the charge is itself taxed
	// at the sale's blended 20% rate = 3 (ADR-0061) -> total 136.
	var total, serviceCharge int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales`).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if serviceCharge != 13 {
		t.Fatalf("expected service_charge_amount 13 (12.5%% of 100, half-up), got %d", serviceCharge)
	}
	if total != 136 {
		t.Fatalf("expected total 136 (100 + 20 tax + 13 service charge + 3 tax on it), got %d", total)
	}
}

// ut-docs#962: Turkey's 2026-01-30 Fiyat Etiketi Yönetmeliği amendment
// makes a service-charge/cover line illegal on any bill. The settings-
// upsert handler is the primary defense (refuses to save a nonzero rate
// for a TR shop in the first place) — this proves the fail-closed
// backstop for whatever reaches the tender path regardless (here, a rate
// set directly on the live state, standing in for e.g. a rate saved
// before the shop's country was changed to TR): the sale must still
// complete (never blocked, per offline-first), but with the service
// charge line unreachable, not merely rejected.
func TestTenderHandler_TurkeyNeverAppliesServiceCharge(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) {
		s.Country = "TR"
		s.ServiceChargeRateBasisPoints = 1250
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (a TR sale must still complete, never blocked), got %d: %s", rec.Code, rec.Body.String())
	}

	// ABC: price 100, 20% tax = 20, service charge must be 0 despite the
	// configured 12.5% rate -> total 120, not 133.
	var total, serviceCharge int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales`).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if serviceCharge != 0 {
		t.Fatalf("expected service_charge_amount 0 for a TR shop despite a configured 12.5%% rate, got %d", serviceCharge)
	}
	if total != 120 {
		t.Fatalf("expected total 120 (100 + 20 tax, no service charge), got %d", total)
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

// ut-docs#944 (ut-docs#924 increment 2 of 4): the SimFail branch's own
// audit-record write used to leak raw Go/SQL error text (err.Error()) via
// http.Error(w, err.Error(), 500) regardless of locale -- same defect class
// as #921/#923/#929 above, just a different call site reached only when the
// audit_log INSERT itself fails.
func TestTenderHandler_SimulatedFailureAuditWriteFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if _, err := dp.Db.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatalf("drop audit_log: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"simulateFailure":true,"failureReason":"card reader offline"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on a RecordPaymentFailure DB failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "audit_log") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw engine/SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("expected the localized pos.error.server copy, got: %s", rec.Body.String())
	}
}

// ut-docs#944: /api/pos/sale/status's UpdateSaleStatus failure used to leak
// raw Go/SQL error text regardless of locale. Two distinct branches: a
// genuine DB-layer failure (generic key, 400) and ErrSaleNotFound (its own
// key, 404) -- exercised separately since they're reached by different
// preconditions (a broken table vs. a saleID nothing matches).
func TestSaleStatusHandler_DBFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Db.Exec(`DROP TABLE sales`); err != nil {
		t.Fatalf("drop sales: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/sale/status",
		strings.NewReader(`{"saleId":"whatever","status":"voided"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on an UpdateSaleStatus DB failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no such table") || strings.Contains(rec.Body.String(), "update sale status") {
		t.Fatalf("raw engine/SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("expected the localized pos.error.server copy, got: %s", rec.Body.String())
	}
}

func TestSaleStatusHandler_UnknownSaleIDShowsLocalizedNotFoundNotRawError(t *testing.T) {
	mux, _ := newPOSTestDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/sale/status",
		strings.NewReader(`{"saleId":"no-such-sale","status":"voided"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for an unknown saleId, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no-such-sale") || strings.Contains(rec.Body.String(), "sale not found:") {
		t.Fatalf("raw engine error text (including the sale id) leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Sale not found") {
		t.Fatalf("expected the localized pos.error.sale_not_found copy, got: %s", rec.Body.String())
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

// ut-docs#921: before this fix, completeTender's fallback branch rendered
// the raw Go error text (e.g. "payments (100) do not cover total (120)")
// straight into the operator-facing toast via http.Error(w, err.Error(),
// ...) -- verbatim English regardless of locale, and never went through
// httpx.T at all. Underpayment is a real, reachable cashier scenario (the
// #72 regression test above already documents its own historical brush
// with this exact message), so it gets the same in-place localized toast
// treatment as the insufficient-stock/fiscal-gate rejections, not a raw
// 400.
func TestTenderHandler_UnderpaymentShowsLocalizedToastNotRawError(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	// ABC: price 100, 20% tax = 20 -> total 120. Tender 50, well short.
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":50}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place toast, not an error status), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pos-notice error") {
		t.Fatalf("expected an error-level pos-notice, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "do not cover total") {
		t.Fatalf("raw engine error text leaked into the operator-facing toast: %s", rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) == 0 {
		t.Fatalf("expected basket to survive an underpayment rejection")
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("query sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sale to be recorded on an underpayment, got %d", count)
	}
}

// classifyTenderError is the pure classifier behind the toast above --
// unit-tested directly for its default branch since a genuinely
// unclassified CompleteSale failure (a DB-layer error, say) isn't
// practical to provoke through the full HTTP path without a contrived
// fault injection. Any error the switch doesn't specifically recognize
// must fall back to the generic key, mirroring the self-order kiosk
// handler's own default (self_order_shop.go's "selforder.checkout.failed"),
// so nothing falls through to raw English again.
func TestClassifyTenderError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"insufficient stock", fmt.Errorf("insufficient stock for item itm1 at location loc_main (have 5.00, need 10.00)"), "pos.toast.insufficient_stock"},
		{"underpayment", fmt.Errorf("payments (50) do not cover total (120)"), "pos.toast.payment_insufficient"},
		{"unclassified", fmt.Errorf("database is locked"), "pos.toast.tender_failed"},
	}
	for _, c := range cases {
		if got := classifyTenderError(c.err); got != c.want {
			t.Errorf("classifyTenderError(%q) = %q, want %q", c.err, got, c.want)
		}
	}
}

// ut-docs#921 review finding (F2): the declined-payment branch had the same
// raw-English leak as the underpayment fallback -- err.Error() ("payment
// declined: demopay") went straight to the operator regardless of locale.
// The 402 status is unchanged (its own type's doc comment: deliberately
// distinct from the generic 400, so a caller can tell a decline apart) --
// only the body must stop being raw Go error text.
func TestTenderHandler_DeclinedPaymentShowsLocalizedMessageNotRawError(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	// min_pos_version/api_version/published_at are NOT NULL on the real
	// plugin_catalog table (ut-docs#1677).
	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demo', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
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
			return nil, errors.New("demopay: card declined")
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"demopay","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("want 402 on a declined plugin gate, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "payment declined:") {
		t.Fatalf("raw engine error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Payment declined") {
		t.Fatalf("expected the localized pos.toast.payment_declined copy, got: %s", rec.Body.String())
	}
	if len(dp.Engine.Basket().Lines) == 0 {
		t.Fatalf("expected basket to survive a declined payment")
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("query sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sale to be recorded on a declined payment, got %d", count)
	}
}

// ut-docs#1762: the operator manually re-tapping Pay on the SAME basket
// after a decline/timeout must ask the payment/fiscal-device plugin the
// SAME question (same event id, which a device plugin forwards downstream
// as its own idempotency/request key) -- a fresh id per retry is what let a
// real chip-and-PIN retry double-charge and print two mali fiş. Verified
// end to end through the real POST /api/pos/tender handler, not just at
// the completeTender/EventBus level -- see okc bridge_test.go's
// TestBridgeSale_SlowFirstAttemptThenRetryPrintsOnce for the device-level
// half of this fix (the device itself already de-dupes correctly once it
// is actually asked the same request twice).
func TestTenderHandler_RetriedTenderOnSameBasketReusesIdempotencyKey(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demo', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
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
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	var seenIDs []string
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			seenIDs = append(seenIDs, ev.ID)
			// Every attempt in this test declines -- what's under test is
			// whether a retry on the same basket carries the same id, not
			// what happens on eventual success.
			return nil, errors.New("demopay: card declined")
		}); err != nil {
		t.Fatal(err)
	}

	body := `{"payments":[{"method":"demopay","amount":120}],"offline":true}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusPaymentRequired {
			t.Fatalf("attempt %d: want 402 on a declined plugin gate, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	if len(seenIDs) != 2 {
		t.Fatalf("plugin saw %d authorize calls, want 2 (one per tender attempt): %v", len(seenIDs), seenIDs)
	}
	if seenIDs[0] == "" {
		t.Fatalf("first attempt's event id was empty")
	}
	if seenIDs[0] != seenIDs[1] {
		t.Fatalf("retry on the SAME basket used a different idempotency key: first=%q second=%q", seenIDs[0], seenIDs[1])
	}
}

// ut-docs#1762 independent review finding: TenderAttemptID alone is stable
// across ANY retry on the same basket -- including one where the operator
// (or, on the shared kiosk engine, a second customer) changed what's being
// paid before retrying. Reusing the SAME idempotency key there would make
// the device's own dedup hand back the FIRST attempt's memoized approval
// for what is now the WRONG amount, unnoticed. The fix folds the
// authorize payload's own content into the key, so a changed amount must
// mint a different key even though the basket was never Reset() between
// attempts.
func TestTenderHandler_ChangedPaymentOnSameBasketGetsDifferentIdempotencyKey(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demo', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`); err != nil {
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
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	var seenIDs []string
	var seenAmounts []int64
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			seenIDs = append(seenIDs, ev.ID)
			var p struct {
				Amount int64 `json:"amount"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			seenAmounts = append(seenAmounts, p.Amount)
			return nil, errors.New("demopay: card declined")
		}); err != nil {
		t.Fatal(err)
	}

	// First attempt: 120. Declined, basket survives untouched (same as the
	// sibling test above). Second attempt, SAME basket, but the operator
	// changed the tendered amount to 90 before retrying -- a genuinely
	// different payment, not a retry of the first.
	for _, amount := range []int{120, 90} {
		req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
			strings.NewReader(fmt.Sprintf(`{"payments":[{"method":"demopay","amount":%d}],"offline":true}`, amount)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusPaymentRequired {
			t.Fatalf("amount=%d: want 402 on a declined plugin gate, got %d: %s", amount, rec.Code, rec.Body.String())
		}
	}

	if len(seenIDs) != 2 {
		t.Fatalf("plugin saw %d authorize calls, want 2: %v", len(seenIDs), seenIDs)
	}
	if seenAmounts[0] == seenAmounts[1] {
		t.Fatalf("test setup bug: both attempts carried the same amount %v", seenAmounts)
	}
	if seenIDs[0] == seenIDs[1] {
		t.Fatalf("a DIFFERENT payment (amount %d then %d) on the same basket reused the SAME idempotency key %q -- the device would replay the first attempt's stale approval for the new amount", seenAmounts[0], seenAmounts[1], seenIDs[0])
	}
}

// ut-docs#929: EnsureStockLocation's failure path -- called before the
// payment loop (and thus before EnsurePaymentMethod), so it's a different
// code path from the #923 tests below -- used to leak raw Go error text
// (err.Error(), e.g. "no such table: stock_locations") via
// http.Error(w, err.Error(), ...) regardless of locale. Provoked here by
// dropping stock_locations out from under a live request: this is the very
// first DB call the handler makes after the basket check, so the failure is
// deterministically isolated to EnsureStockLocation itself.
func TestTenderHandler_EnsureStockLocationFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	// ut-docs#1679: DROP TABLE stock_locations used to force EnsureStockLocation
	// to fail, but stock_locations now has real incoming FKs that block the
	// DROP under real migrations. A closed *sql.DB forces the same generic
	// repo-error path deterministically without touching schema.
	dp.Db.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on an EnsureStockLocation DB failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "stock_locations") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw engine/SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "could not be completed") {
		t.Fatalf("expected the localized pos.toast.tender_failed copy, got: %s", rec.Body.String())
	}
}

// ut-docs#923: EnsurePaymentMethod's failure path -- reached before
// completeTender/CompleteSale is ever called, so it's a different code path
// from the #921 fixes above -- used to leak raw Go error text
// (err.Error(), e.g. "no such table: payment_methods") via
// http.Error(w, err.Error(), ...) regardless of locale. Provoked here by
// dropping payment_methods out from under a live request: EnsureStockLocation
// (called earlier in the handler, over the untouched stock_locations table)
// still succeeds, so the failure is deterministically isolated to
// EnsurePaymentMethod's own SELECT/INSERT.
func TestTenderHandler_EnsurePaymentMethodFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if _, err := dp.Db.Exec(`DROP TABLE payment_methods`); err != nil {
		t.Fatalf("drop payment_methods: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on an EnsurePaymentMethod DB failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "payment_methods") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw engine/SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "could not be completed") {
		t.Fatalf("expected the localized pos.toast.tender_failed copy, got: %s", rec.Body.String())
	}
}

// Same fix, form-encoded fallback branch (quick-tender buttons that send
// hx-vals rather than a JSON body) -- the ticket's own second call site.
func TestTenderHandler_FormFallbackEnsurePaymentMethodFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if _, err := dp.Db.Exec(`DROP TABLE payment_methods`); err != nil {
		t.Fatalf("drop payment_methods: %v", err)
	}

	form := url.Values{"method": {"cash"}, "amount": {"120"}, "offline": {"true"}}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on an EnsurePaymentMethod DB failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "payment_methods") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw engine/SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "could not be completed") {
		t.Fatalf("expected the localized pos.toast.tender_failed copy, got: %s", rec.Body.String())
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

// tableOccupied reports a table's live state via the same ListTablesWithState
// the floor plan and the basket's table picker read (ut-docs#1390 tests).
func tableOccupied(t *testing.T, dp *common.Deps, tableID string) bool {
	t.Helper()
	states, err := data.NewPOSRepo(dp.Db).ListTablesWithState(context.Background(), time.Now().Add(-tillClaimTTL))
	if err != nil {
		t.Fatalf("ListTablesWithState: %v", err)
	}
	for _, s := range states {
		if s.ID == tableID {
			return s.Occupied
		}
	}
	t.Fatalf("table %s not listed", tableID)
	return false
}

func createTestTable(t *testing.T, dp *common.Deps, label string) string {
	t.Helper()
	id, err := data.NewPOSRepo(dp.Db).CreateTable(context.Background(), label, "", 4, "rect", 200, 200)
	if err != nil {
		t.Fatalf("CreateTable %s: %v", label, err)
	}
	return id
}

// TestTableHandler_AssignClaimsTable_OccupiedPickRejected is the regression
// for ut-docs#1390: assigning a table to the LIVE basket must reserve it the
// instant it's picked (visible through ListTablesWithState), re-picking the
// basket's own table is a no-op, and a pick of a table some other basket
// already claims is rejected in place -- the basket keeps its current table
// and the operator sees an error toast, never a silent success.
func TestTableHandler_AssignClaimsTable_OccupiedPickRejected(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	t2 := createTestTable(t, dp, "T2")

	rec := posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != t1 {
		t.Fatalf("expected TableID %q, got %q", t1, got)
	}
	if !tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must read occupied the moment the live basket picks it")
	}

	// Re-picking the basket's own current table: no-op, no error.
	rec = posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-pick: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != t1 {
		t.Fatalf("re-pick must keep TableID %q, got %q", t1, got)
	}
	if strings.Contains(rec.Body.String(), "already occupied") {
		t.Fatalf("re-picking the basket's own table must not render the occupied toast: %s", rec.Body.String())
	}

	// T2 is claimed by another live basket (seeded raw, as the held_sales
	// occupancy tests do): picking it must be rejected, leaving this basket
	// on T1 -- and T1's own claim untouched.
	if _, err := dp.Db.Exec(`INSERT INTO table_claims (table_id, claimed_at) VALUES (?, '2026-08-18T11:00:00Z')`, t2); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	rec = posPostForm(mux, "/api/pos/table", "table_id="+t2)
	if rec.Code != http.StatusOK {
		t.Fatalf("occupied pick: expected 200 (in-place toast), got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != t1 {
		t.Fatalf("an occupied pick must leave the basket on T1, got TableID %q", got)
	}
	if !strings.Contains(rec.Body.String(), "already occupied") {
		t.Fatalf("expected the occupied toast in the re-rendered basket, got: %s", rec.Body.String())
	}
	if !tableOccupied(t, dp, t1) {
		t.Fatalf("T1's claim must survive a rejected move")
	}
	var claims int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM table_claims WHERE table_id = ?`, t2).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("T2 must keep exactly its one foreign claim, got %d (err %v)", claims, err)
	}
}

// The end-to-end shape of the reported bug (ut-docs#1390), through nothing
// but public handlers: order 1 picks T1 and is parked; order 2 (the next
// live basket) picks T1 -- it used to succeed, because the live-basket path
// never consulted IsTableFree at all.
func TestTableHandler_RejectsTableOfParkedOrder(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	registerHoldAPI(mux, dp)
	t1 := createTestTable(t, dp, "T1")

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("assign: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := posPostForm(mux, "/api/pos/hold", ""); rec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("hold must clear the live basket's table, got %q", got)
	}

	rec := posPostForm(mux, "/api/pos/table", "table_id="+t1)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("order 2 must not be assigned the parked order's table, got TableID %q", got)
	}
	if !strings.Contains(rec.Body.String(), "already occupied") {
		t.Fatalf("expected the occupied toast, got: %s", rec.Body.String())
	}
}

// Clearing the live basket's table releases its claim (ut-docs#1390).
func TestTableHandler_ClearReleasesClaim(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("assign: expected 200, got %d", rec.Code)
	}
	if !tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must be occupied after assignment")
	}
	if rec := posPostForm(mux, "/api/pos/table", "table_id="); rec.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d", rec.Code)
	}
	if tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must be free again after the basket clears its table")
	}
}

// Moving the live basket from T1 to T2: T2 gets claimed, T1 released
// (ut-docs#1390).
func TestTableHandler_MoveReleasesOldClaimsNew(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	t2 := createTestTable(t, dp, "T2")
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("assign T1: expected 200, got %d", rec.Code)
	}
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t2); rec.Code != http.StatusOK {
		t.Fatalf("move to T2: expected 200, got %d", rec.Code)
	}
	if got := dp.Engine.Basket().TableID; got != t2 {
		t.Fatalf("expected TableID %q after move, got %q", t2, got)
	}
	if tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must be free after the basket moved off it")
	}
	if !tableOccupied(t, dp, t2) {
		t.Fatalf("T2 must be occupied after the basket moved onto it")
	}
}

// ut-docs#1355 x ut-docs#1390: Service.SetTable is a no-op on a Takeaway
// basket, so a claim taken for that pick must be undone -- otherwise the
// table would read occupied with nothing actually on it.
func TestTableHandler_TakeawayPickDoesNotLeakClaim(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	dp.Engine.SetOrderType(pos.OrderTypeTakeaway)
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("a takeaway basket must not be assigned a table, got %q", got)
	}
	if tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must not stay claimed by a pick SetTable refused")
	}
}

// ut-docs#1355 x ut-docs#1390: switching the order to Takeaway clears the
// basket's table in the engine (SetOrderType), so the claim goes with it.
func TestOrderTypeHandler_TakeawayReleasesTableClaim(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("assign: expected 200, got %d", rec.Code)
	}
	if rec := posPostForm(mux, "/api/pos/order-type", "order_type="+pos.OrderTypeTakeaway); rec.Code != http.StatusOK {
		t.Fatalf("order-type: expected 200, got %d", rec.Code)
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("takeaway must clear the table, got %q", got)
	}
	if tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must be free once the order went takeaway")
	}
}

// The explicit "new customer" reset releases the table claim (ut-docs#1390).
func TestResetHandler_ReleasesTableClaim(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("assign: expected 200, got %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/reset", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("reset must clear the table, got %q", got)
	}
	if tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must be free after the basket was reset")
	}
}

// Completing the sale (tender) releases the table claim afterwards
// (ut-docs#1390) -- completeTender captures the table before engine.Reset()
// clears it.
func TestTenderHandler_ReleasesTableClaim(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t1 := createTestTable(t, dp, "T1")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if rec := posPostForm(mux, "/api/pos/table", "table_id="+t1); rec.Code != http.StatusOK {
		t.Fatalf("assign: expected 200, got %d", rec.Code)
	}
	if !tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must be occupied before tender")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.Basket().TableID; got != "" {
		t.Fatalf("tender must reset the basket's table, got %q", got)
	}
	if tableOccupied(t, dp, t1) {
		t.Fatalf("T1 must be free after the sale completed")
	}
}
