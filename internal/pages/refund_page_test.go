package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// refundableLines is the double-refund guard's presentation layer: what's
// still available to give back, per line, after accounting for whatever
// prior partial returns already took. Wrong math here means either a
// customer can be refunded twice for the same item, or a legitimate
// remaining refund is wrongly blocked.

func TestRefundableLines_NoPriorReturns(t *testing.T) {
	detail := data.SaleDetail{Lines: []data.SaleDetailLine{
		{Name: "Apple", SKU: "A1", ItemID: "i1", UnitPrice: 100, Qty: 3},
	}}
	lines := refundableLines(detail, map[string]float64{})
	if len(lines) != 1 || lines[0].Remaining != 3 || lines[0].Sold != 3 {
		t.Fatalf("expected all 3 units still refundable, got %+v", lines)
	}
}

func TestRefundableLines_SubtractsPriorPartialReturn(t *testing.T) {
	detail := data.SaleDetail{Lines: []data.SaleDetailLine{
		{Name: "Apple", SKU: "A1", ItemID: "i1", UnitPrice: 100, Qty: 3},
	}}
	key := data.RefundLineKey("i1", "", 100)
	lines := refundableLines(detail, map[string]float64{key: 1})
	if len(lines) != 1 || lines[0].Remaining != 2 {
		t.Fatalf("expected 2 units remaining after 1 already returned, got %+v", lines)
	}
}

func TestRefundableLines_FullyReturnedNeverGoesNegative(t *testing.T) {
	detail := data.SaleDetail{Lines: []data.SaleDetailLine{
		{Name: "Apple", SKU: "A1", ItemID: "i1", UnitPrice: 100, Qty: 2},
	}}
	key := data.RefundLineKey("i1", "", 100)
	// More already "returned" than was ever sold shouldn't happen in
	// practice, but the view must clamp to zero, not go negative.
	lines := refundableLines(detail, map[string]float64{key: 5})
	if lines[0].Remaining != 0 {
		t.Fatalf("expected remaining clamped to 0, got %v", lines[0].Remaining)
	}
}

// TestRefundableLines_SplitLinesShareTheSamePool covers the trickiest case:
// TWO separate sale lines for the same item/variant/price (e.g. added to
// the basket in two separate scans) share ONE refundable pool, keyed by
// RefundLineKey — the view must not let each line offer its own full
// quantity independently, or the total offered would exceed what was
// actually sold.
func TestRefundableLines_SplitLinesShareTheSamePool(t *testing.T) {
	detail := data.SaleDetail{Lines: []data.SaleDetailLine{
		{Name: "Apple", SKU: "A1", ItemID: "i1", UnitPrice: 100, Qty: 2},
		{Name: "Apple", SKU: "A1", ItemID: "i1", UnitPrice: 100, Qty: 2},
	}}
	key := data.RefundLineKey("i1", "", 100)
	// One unit already returned against the combined pool of 4.
	lines := refundableLines(detail, map[string]float64{key: 1})
	if len(lines) != 2 {
		t.Fatalf("expected 2 line views, got %+v", lines)
	}
	// Exact per-line allocation: pool starts at 3 (4 sold - 1 returned).
	// Line 0 (qty 2) takes min(2,3)=2, leaving 1 in the pool; line 1
	// (qty 2) takes min(2,1)=1. This is the regression case for the real
	// bug found while writing this test: the old algorithm computed each
	// line's remaining directly against the running "returned" tally
	// (l.Qty - returned[key]) instead of against the true shared pool,
	// which gave line 0 only 1 and line 1 0 — a total of 1 instead of the
	// true 3.
	if lines[0].Remaining != 2 || lines[1].Remaining != 1 {
		t.Fatalf("expected line0=2, line1=1 (true pool 3 correctly split), got %+v", lines)
	}
	if total := lines[0].Remaining + lines[1].Remaining; total != 3 {
		t.Fatalf("expected the pool (4 sold - 1 returned = 3) split across both lines, got total=%v (%+v)", total, lines)
	}
}

func TestSaleIsTaxInclusive(t *testing.T) {
	// No tax at all: both modes are the identity, so this reports exclusive
	// (false) — matches the function's documented short-circuit.
	if saleIsTaxInclusive(data.SaleDetail{TaxTotal: 0}) {
		t.Fatal("expected false when TaxTotal is 0")
	}
	// Inclusive: total == subtotal - discount (tax was already inside the
	// item prices).
	if !saleIsTaxInclusive(data.SaleDetail{Subtotal: 220, DiscountTotal: 22, Total: 198, TaxTotal: 20}) {
		t.Fatal("expected inclusive detection when total == subtotal - discount")
	}
	// Exclusive: tax was added on top, so total != subtotal - discount.
	if saleIsTaxInclusive(data.SaleDetail{Subtotal: 100, DiscountTotal: 10, Total: 110, TaxTotal: 20}) {
		t.Fatal("expected exclusive detection when total != subtotal - discount")
	}
}

func TestComputeRefundTotal_ExclusiveAddsTaxOnTop(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 2, TaxRateBasisPoints: 2000}, // net 200, tax 40 (exclusive)
	}
	total := computeRefundTotal(lines, 0, false)
	if total.Minor() != 240 {
		t.Fatalf("expected 200 + 40 tax = 240, got %d", total.Minor())
	}
}

func TestComputeRefundTotal_InclusiveDoesNotAddTaxOnTop(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(120), Qty: 1, TaxRateBasisPoints: 2000}, // 120 already includes tax
	}
	total := computeRefundTotal(lines, 0, true)
	if total.Minor() != 120 {
		t.Fatalf("expected the inclusive total to stay 120 (tax already inside), got %d", total.Minor())
	}
}

func TestComputeRefundTotal_SubtractsSaleDiscount(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 1, TaxRateBasisPoints: 0},
	}
	total := computeRefundTotal(lines, money.FromMinor(30), false)
	if total.Minor() != 70 {
		t.Fatalf("expected 100 - 30 discount = 70, got %d", total.Minor())
	}
}

func TestComputeRefundTotal_NeverNegative(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(50), Qty: 1, TaxRateBasisPoints: 0},
	}
	// A discount larger than the line total must clamp to zero, not swing
	// negative (which would mean the shop pays the customer to return an
	// already-discounted item).
	total := computeRefundTotal(lines, money.FromMinor(999), false)
	if total.Minor() != 0 {
		t.Fatalf("expected a clamped-to-zero refund total, got %d", total.Minor())
	}
}

func TestComputeRefundTotal_LineDiscountReducesNet(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 1, LineDiscount: money.FromMinor(20), TaxRateBasisPoints: 0},
	}
	total := computeRefundTotal(lines, 0, false)
	if total.Minor() != 80 {
		t.Fatalf("expected 100 - 20 line discount = 80, got %d", total.Minor())
	}
}

// --- HTTP-level tests for POST /api/refund ---

func newRefundTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *auth.Service) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
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
	// Registered AFTER the db.Close cleanup above, so LIFO order runs this
	// FIRST: a refund's detached print goroutines (ut-docs#425, #514)
	// finish before Close and TempDir removal can race them.
	t.Cleanup(dp.WaitForAsyncWork)
	svc := auth.NewService(db)
	mux := http.NewServeMux()
	registerRefund(mux, dp, svc)
	return mux, dp, svc
}

func TestRefundPage_UnknownReceiptRedirectsToJournal(t *testing.T) {
	mux, _, _ := newRefundTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/refund/NO-SUCH-RECEIPT", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect for an unknown receipt, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/journal" {
		t.Fatalf("expected redirect to /journal, got %q", loc)
	}
}

func seedCompletedSaleForRefund(t *testing.T, dp *common.Deps) (saleID, receiptNo string) {
	t.Helper()
	ctx := context.Background()
	saleID, receiptNo = "sale-refund-1", "R-REFUND-1"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 100, 0, 20, 120, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-1', ?, 1, 'itm1', 'Apple', 'ABC', 2, 100, 2000, 40, 200, 240)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-1', ?, 'cash', 120, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}
	return saleID, receiptNo
}

func TestRefundPage_ShowsRefundableLines(t *testing.T) {
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodGet, "/refund/"+receiptNo, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Apple") {
		t.Fatalf("expected the sale's line item on the refund page, got body without it")
	}
}

// ut-docs#944 (ut-docs#924 increment 2 of 4): a genuine DB-layer failure in
// ReturnedQuantities used to leak raw Go/SQL error text (err.Error()) via
// http.Error(w, err.Error(), 500) regardless of locale -- same defect class
// as #921/#923/#929/#316 elsewhere in this package, reached on the page-load
// GET, before any refund is even attempted.
func TestRefundPage_ReturnedQuantitiesFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)
	if _, err := dp.Db.Exec(`DROP TABLE sale_links`); err != nil {
		t.Fatalf("drop sale_links: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/refund/"+receiptNo, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on a ReturnedQuantities DB failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sale_links") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw engine/SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("expected the localized refund.error.server copy, got: %s", rec.Body.String())
	}
}

// Same underlying call as the GET handler above, reached from the POST
// handler's own copy of the ReturnedQuantities call instead.
func TestPostRefund_ReturnedQuantitiesFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)
	if _, err := dp.Db.Exec(`DROP TABLE sale_links`); err != nil {
		t.Fatalf("drop sale_links: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on a ReturnedQuantities DB failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sale_links") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw engine/SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("expected the localized refund.error.server copy, got: %s", rec.Body.String())
	}
}

// ut-docs#944: same repo method, same generic internal-failure semantics as
// pos_api.go's tender handler (ut-docs#929) -- this call site reuses that
// key rather than a refund-specific one.
func TestPostRefund_EnsureStockLocationFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)
	if _, err := dp.Db.Exec(`DROP TABLE stock_locations`); err != nil {
		t.Fatalf("drop stock_locations: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

// ut-docs#944: same repo method, same generic internal-failure semantics as
// pos_api.go's tender handler (ut-docs#923) -- reuses that key too.
func TestPostRefund_EnsurePaymentMethodFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)
	if _, err := dp.Db.Exec(`DROP TABLE payment_methods`); err != nil {
		t.Fatalf("drop payment_methods: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
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

// ut-docs#944: CompleteSale's own failure used to leak raw Go/SQL error text
// via http.Error(w, err.Error(), 400). Forced here by dropping
// stock_movements -- a table CompleteSale's own transaction writes to
// (RecordStockMovement, per line) but nothing earlier in the handler
// (GetSaleDetail/ReturnedQuantities/EnsureStockLocation/EnsurePaymentMethod)
// touches, so every upstream step succeeds and only CompleteSale itself
// fails. classifyTenderError has no specific match for this message, so it
// falls through to its generic default -- the same key pos_api.go's own
// tender handler renders for an unclassified CompleteSale failure.
func TestPostRefund_CompleteSaleFailureShowsLocalizedMessageNotRawError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)
	if _, err := dp.Db.Exec(`DROP TABLE stock_movements`); err != nil {
		t.Fatalf("drop stock_movements: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on a CompleteSale DB failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "stock_movements") || strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("raw engine/SQL error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "could not be completed") {
		t.Fatalf("expected the localized pos.toast.tender_failed fallback copy, got: %s", rec.Body.String())
	}
}

func TestPostRefund_SaleNotFound(t *testing.T) {
	mux, _, _ := newRefundTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt=NO-SUCH-RECEIPT"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown receipt, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPostRefund_RequiresManagerPINWhenAuthEnabled(t *testing.T) {
	// UT_AUTH is unset in this test process, so auth.Disabled(...) is
	// false — registerRefund requires the manager-PIN gate.
	t.Setenv("UT_AUTH", "")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPostRefund_NoQuantitiesSelected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when no line quantities are given, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPostRefund_QuantityExceedsRemainingIsRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	// The seeded line sold quantity 2; requesting 5 must be rejected as a
	// double-refund guard violation, not silently clamped.
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a quantity exceeding what's remaining, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPostRefund_SplitLinesCombinedRequestWithinTruePool is the HTTP-level
// regression case for the refundLinePool bug: an original sale with the
// SAME item/price rung up as two separate lines (qty 2 each), one unit
// already returned against the shared key (true pool = 4-1 = 3). A
// combined request of 2 on line 0 + 1 on line 1 = 3 is exactly the true
// remaining and must succeed. Before the fix, line 0 alone was computed
// against `l.Qty - returned[key]` = 2-1 = 1, so requesting 2 on line 0
// was wrongly rejected with 409 "only 1 left to refund" even though the
// combined request was entirely valid.
func TestPostRefund_SplitLinesCombinedRequestWithinTruePool(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-split-1", "R-SPLIT-1"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 400, 0, 0, 400, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	for i, lineID := range []string{"split-line-0", "split-line-1"} {
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, ?, 'itm1', 'Apple', 'ABC', 2, 100, 0, 0, 200, 200)`, lineID, saleID, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('split-pay', ?, 'cash', 400, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}
	// A prior return of 1 unit against this sale.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('return-split-1', 'R-SPLIT-RETURN-1', 'completed', 'return', 'GBP', 100, 0, 0, 100, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('return-split-line-1', 'return-split-1', 1, 'itm1', 'Apple', 'ABC', 1, 100, 0, 0, 100, 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_links(id, sale_id, original_sale_id, reason) VALUES('link-split-1', 'return-split-1', ?, 'test')`, saleID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2&qty_1=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a combined request within the true pool (2+1=3, true remaining=3), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPostRefund_InvalidQuantity(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=not-a-number"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-numeric quantity, got %d: %s", rec.Code, rec.Body.String())
	}
}
