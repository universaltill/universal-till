package pages

import (
	"context"
	"encoding/json"
	"errors"
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
	key := data.RefundLineKey("i1", "", 100, "")
	lines := refundableLines(detail, map[string]float64{key: 1})
	if len(lines) != 1 || lines[0].Remaining != 2 {
		t.Fatalf("expected 2 units remaining after 1 already returned, got %+v", lines)
	}
}

func TestRefundableLines_FullyReturnedNeverGoesNegative(t *testing.T) {
	detail := data.SaleDetail{Lines: []data.SaleDetailLine{
		{Name: "Apple", SKU: "A1", ItemID: "i1", UnitPrice: 100, Qty: 2},
	}}
	key := data.RefundLineKey("i1", "", 100, "")
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
	key := data.RefundLineKey("i1", "", 100, "")
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
	total := computeRefundTotal(lines, 0, 0, 0, false)
	if total.Minor() != 240 {
		t.Fatalf("expected 200 + 40 tax = 240, got %d", total.Minor())
	}
}

func TestComputeRefundTotal_InclusiveDoesNotAddTaxOnTop(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(120), Qty: 1, TaxRateBasisPoints: 2000}, // 120 already includes tax
	}
	total := computeRefundTotal(lines, 0, 0, 0, true)
	if total.Minor() != 120 {
		t.Fatalf("expected the inclusive total to stay 120 (tax already inside), got %d", total.Minor())
	}
}

func TestComputeRefundTotal_SubtractsSaleDiscount(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 1, TaxRateBasisPoints: 0},
	}
	total := computeRefundTotal(lines, money.FromMinor(30), 0, 0, false)
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
	total := computeRefundTotal(lines, money.FromMinor(999), 0, 0, false)
	if total.Minor() != 0 {
		t.Fatalf("expected a clamped-to-zero refund total, got %d", total.Minor())
	}
}

func TestComputeRefundTotal_LineDiscountReducesNet(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 1, LineDiscount: money.FromMinor(20), TaxRateBasisPoints: 0},
	}
	total := computeRefundTotal(lines, 0, 0, 0, false)
	if total.Minor() != 80 {
		t.Fatalf("expected 100 - 20 line discount = 80, got %d", total.Minor())
	}
}

// ut-docs#243: before this, computeRefundTotal had no service-charge
// parameter at all, so a refund never credited the customer's share of the
// original sale's service charge back -- the shop silently kept it.

func TestComputeRefundTotal_ExclusiveServiceChargeAddsChargeAndItsTaxOnTop(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 2, TaxRateBasisPoints: 0}, // net 200, no line tax
	}
	// A flat 10% basis on a 100 charge: 10 tax, added on top since exclusive.
	total := computeRefundTotal(lines, 0, money.FromMinor(100), 1000, false)
	if total.Minor() != 310 {
		t.Fatalf("expected 200 (lines) + 100 (charge) + 10 (charge tax) = 310, got %d", total.Minor())
	}
}

func TestComputeRefundTotal_InclusiveServiceChargeFoldsTaxIn(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 2, TaxRateBasisPoints: 0},
	}
	// Inclusive: the charge's tax is already embedded in the charge amount,
	// so it must NOT be added a second time on top of the total.
	total := computeRefundTotal(lines, 0, money.FromMinor(100), 1000, true)
	if total.Minor() != 300 {
		t.Fatalf("expected 200 (lines) + 100 (charge, tax already inside) = 300, got %d", total.Minor())
	}
}

func TestComputeRefundTotal_DiscountAppliesBeforeServiceChargeIsAdded(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 1, TaxRateBasisPoints: 0},
	}
	// Mirrors pos.CompleteSale's own ordering: a whole-sale discount reduces
	// the line subtotal but never eats into the service charge.
	total := computeRefundTotal(lines, money.FromMinor(30), money.FromMinor(10), 0, false)
	if total.Minor() != 80 {
		t.Fatalf("expected (100-30 discount)+10 charge = 80, got %d", total.Minor())
	}
}

func TestComputeRefundTotal_ZeroServiceChargeIsARegressionNoOp(t *testing.T) {
	lines := []pos.SaleLineInput{
		{UnitPrice: money.FromMinor(100), Qty: 2, TaxRateBasisPoints: 2000},
	}
	total := computeRefundTotal(lines, 0, 0, 0, false)
	if total.Minor() != 240 {
		t.Fatalf("a zero service charge must behave exactly as before this change: expected 240, got %d", total.Minor())
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

// TestRefundPage_RendersOfflineFlag is ut-docs#1493's template-render
// regression check: a broken template edit could still return 200 (this
// test's siblings don't touch this part of the page), so this asserts the
// hidden #offline-flag input the fix depends on actually renders — without
// it, a real browser submit could never carry the offline signal to
// POST /api/refund no matter what refund_page.go's handler does with it.
func TestRefundPage_RendersOfflineFlag(t *testing.T) {
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/refund/"+receiptNo, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /refund/%s: %d", receiptNo, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="offline-flag" name="offline" value="0"`) {
		t.Fatal("refund-form is missing its hidden offline-flag input (ut-docs#1493)")
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

// seedCompletedSaleWithServiceChargeForRefund seeds a completed sale carrying
// a service charge (ut-docs#243's fixture): 2 units @ 100 (subtotal 200,
// no line tax to keep the arithmetic isolated to the charge itself) plus a
// 20 service charge, total 220.
func seedCompletedSaleWithServiceChargeForRefund(t *testing.T, dp *common.Deps) (saleID, receiptNo string) {
	t.Helper()
	ctx := context.Background()
	saleID, receiptNo = "sale-refund-charge-1", "R-REFUND-CHARGE-1"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, service_charge_tax_basis_bp, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 200, 0, 0, 220, 20, 0, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-charge-1', ?, 1, 'itm1', 'Apple', 'ABC', 2, 100, 0, 0, 200, 200)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-charge-1', ?, 'cash', 220, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}
	return saleID, receiptNo
}

// TestPostRefund_FullRefundReturnsFullServiceCharge is the end-to-end case
// for ut-docs#243: before the fix, computeRefundTotal and the return's
// pos.SaleInput never carried any service-charge component at all, so a
// fully-refunded sale returned only the goods (200) and the shop silently
// kept the 20 service charge. A full refund of every line must now return
// the whole original service charge back exactly.
func TestPostRefund_FullRefundReturnsFullServiceCharge(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleWithServiceChargeForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund failed: %d %s", rec.Code, rec.Body.String())
	}

	var returnTotal, returnCharge, returnPaid int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales WHERE sale_type = 'return'`).Scan(&returnTotal, &returnCharge); err != nil {
		t.Fatalf("read return sale: %v", err)
	}
	if err := dp.Db.QueryRow(`SELECT p.amount FROM payments p JOIN sales s ON s.id = p.sale_id WHERE s.sale_type = 'return'`).Scan(&returnPaid); err != nil {
		t.Fatalf("read return payment: %v", err)
	}
	if returnCharge != 20 {
		t.Fatalf("expected the full 20 service charge refunded, got %d", returnCharge)
	}
	if returnTotal != 220 || returnPaid != 220 {
		t.Fatalf("expected total/paid = 220/220 (200 goods + 20 service charge), got %d/%d", returnTotal, returnPaid)
	}
}

// TestPostRefund_PartialRefundProratesServiceCharge: refunding only 1 of the
// 2 units (half the original gross) must return half the service charge —
// the same proration fraction (refundGross/origGross) already used for
// SaleDiscount, not "all or nothing."
func TestPostRefund_PartialRefundProratesServiceCharge(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleWithServiceChargeForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund failed: %d %s", rec.Code, rec.Body.String())
	}

	var returnTotal, returnCharge int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales WHERE sale_type = 'return'`).Scan(&returnTotal, &returnCharge); err != nil {
		t.Fatalf("read return sale: %v", err)
	}
	// refundGross=100, origGross=200 -> half the 20 charge = 10.
	if returnCharge != 10 {
		t.Fatalf("expected half the service charge (10) prorated for a half-quantity refund, got %d", returnCharge)
	}
	if returnTotal != 110 {
		t.Fatalf("expected total = 100 (1 unit) + 10 (prorated charge) = 110, got %d", returnTotal)
	}
}

// TestPostRefund_ZeroServiceChargeSaleIsUnaffected is the explicit
// regression guard: a sale with no service charge at all (the common case,
// and every fixture predating ut-docs#243) must refund exactly as before —
// no charge appears on the return, no accidental non-zero leaked in.
func TestPostRefund_ZeroServiceChargeSaleIsUnaffected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund failed: %d %s", rec.Code, rec.Body.String())
	}

	var returnCharge int64
	if err := dp.Db.QueryRow(`SELECT service_charge_amount FROM sales WHERE sale_type = 'return'`).Scan(&returnCharge); err != nil {
		t.Fatalf("read return sale: %v", err)
	}
	if returnCharge != 0 {
		t.Fatalf("expected no service charge on a sale that never had one, got %d", returnCharge)
	}
}

// TestPostRefund_TwoSequentialPartialRefundsSumToTheFullServiceCharge guards
// against the double-refund risk this proration formula would have if it
// were based on the ORIGINAL sale's full gross on every call: since each
// refund's fraction is refundGross-of-THIS-request/origGross, and the
// existing double-refund pool guard (refundLinePool) already prevents any
// unit from being refunded twice, two sequential partial refunds covering
// all units can never OVER-refund the charge. This particular even 1+1-of-2
// split happens to sum back to the exact original 20 too (integer division
// is exact here); that's not a general guarantee -- an uneven split (e.g.
// three 1-of-3 refunds) truncates on each call and can sum to slightly
// LESS than the original charge (never more), same direction and shape as
// the pre-existing SaleDiscount proration two lines above it in the
// handler. See TestPostRefund_UnevenSequentialRefundsNeverExceedTheOriginalServiceCharge
// for that residual case.
func TestPostRefund_TwoSequentialPartialRefundsSumToTheFullServiceCharge(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleWithServiceChargeForRefund(t, dp)

	for _, qty := range []string{"1", "1"} {
		req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0="+qty))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("refund failed: %d %s", rec.Code, rec.Body.String())
		}
	}

	var totalCharge int64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(service_charge_amount), 0) FROM sales WHERE sale_type = 'return'`).Scan(&totalCharge); err != nil {
		t.Fatalf("sum return service charges: %v", err)
	}
	if totalCharge != 20 {
		t.Fatalf("two sequential half-refunds must sum to the full original 20 service charge, got %d", totalCharge)
	}
}

// TestPostRefund_UnevenSequentialRefundsNeverExceedTheOriginalServiceCharge
// is the residual case the comment above calls out: an UNEVEN split (three
// separate 1-of-3 refunds, each an exact third of the gross) truncates on
// every call (10*100/300 = 3, three times = 9, not 10) because each refund
// re-derives its own fraction independently rather than tracking a running
// remainder. The direction matters more than the exact figure: this can
// only ever under-refund by a few minor units, never over-refund (which
// would mean the till pays out more than the original charge), and it is
// the exact same shape the pre-existing SaleDiscount proration already has
// two lines above it in the handler -- not a regression this change
// introduces.
func TestPostRefund_UnevenSequentialRefundsNeverExceedTheOriginalServiceCharge(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-refund-charge-uneven", "R-REFUND-CHARGE-UNEVEN"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, service_charge_tax_basis_bp, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 300, 0, 0, 310, 10, 0, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-charge-uneven', ?, 1, 'itm1', 'Apple', 'ABC', 3, 100, 0, 0, 300, 300)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-charge-uneven', ?, 'cash', 310, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("refund %d failed: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}

	var totalCharge int64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(service_charge_amount), 0) FROM sales WHERE sale_type = 'return'`).Scan(&totalCharge); err != nil {
		t.Fatalf("sum return service charges: %v", err)
	}
	if totalCharge > 10 {
		t.Fatalf("three uneven partial refunds must never exceed the original 10 service charge, got %d", totalCharge)
	}
	if totalCharge != 9 {
		t.Fatalf("expected the known truncation residual (3+3+3=9, one short of 10), got %d -- if this is now 10, the proration got more precise and this test's comment should be updated", totalCharge)
	}
}

// TestPostRefund_SplitRefundServiceChargeTaxSumsExactly is ut-docs#1215's
// regression case, reproducing the independent reviewer's exact reported
// scenario from ut-docs#243's review
// (docs/code-reviews/2026-08-28-service-charge-refund-proration-243.md,
// finding N2): an exclusive sale with two lines that mix a per-line
// discount AND different tax rates, plus a service charge apportioned
// across bands (not a flat basis), refunded in two SEPARATE partial
// requests rather than one full refund.
//
//	Line A: 1 @ 200, tax rate 0%,  line discount 100 -> net 100
//	Line B: 1 @ 100, tax rate 20%, no discount        -> net 100
//	Service charge: 30 (basis 0 -> apportioned by net share, ADR-0061)
//
// Original apportionment (equal net shares, 100:100): band 0% = 15 (tax
// 0), band 20% = 15 (tax 3) -- 3 total charge tax, 20 total line tax (all
// on line B), 23 total tax, 253 total (200 subtotal + 23 tax + 30 charge).
//
// Refunding line B THEN line A, each as its own request, used to recover
// only 2 of the 3 charge-tax units (a 1-unit discrepancy). Both before and
// after the fix, each request's tax bands are (re)derived from THAT
// request's own line subset (pos.ServiceChargeTax(serviceChargeRefund,
// ChargeTaxLinesFromSale(lines), ...) is unchanged) -- what changed is the
// CHARGE AMOUNT fed into that recomputation: pre-fix it was prorated by
// gross share (refundGross/origGross), post-fix by net-after-discount
// share (refundNetWeight/origNetWeight), matching the basis
// ApportionServiceChargeTax itself weighs by (ADR-0061). In THIS
// particular sale (each refund happens to touch exactly one whole rate
// group), that basis switch is what recovers the missing unit. Since each
// refunded line's own tax rate is fixed and unambiguous, the line-tax
// component (20, all from line B) can't drift -- so asserting the total
// tax summed across both partial refunds equals the original sale's exact
// 23 isolates and proves the charge-tax component landed on the full 3,
// not 2. This is NOT a general exactness guarantee for every split-refund
// shape (see the code comment above the fix, and
// TestPostRefund_LineDiscountedMultiPartialRefundNeverExceedsOriginalServiceCharge
// below for the case where it still under-refunds by design, bounded by
// an explicit clamp rather than by luck of the arithmetic).
func TestPostRefund_SplitRefundServiceChargeTaxSumsExactly(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-refund-charge-split-1215", "R-REFUND-CHARGE-SPLIT-1215"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, service_charge_tax_basis_bp, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 200, 0, 23, 253, 30, 0, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-split-a', ?, 1, 'itm-a', 'Widget A', 'A', 1, 200, 100, 0, 0, 100, 100)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-split-b', ?, 2, 'itm-b', 'Widget B', 'B', 1, 100, 0, 2000, 20, 100, 120)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-split-1215', ?, 'cash', 253, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}

	// Refund line B (index 1) alone, then line A (index 0) alone, as two
	// separate requests -- the split-refund shape the bug needs.
	for _, form := range []string{"qty_1=1", "qty_0=1"} {
		req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&"+form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("refund (%s) failed: %d %s", form, rec.Code, rec.Body.String())
		}
	}

	var totalCharge, totalTax, totalTotal int64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(service_charge_amount), 0), COALESCE(SUM(tax_total), 0), COALESCE(SUM(total), 0) FROM sales WHERE sale_type = 'return'`).Scan(&totalCharge, &totalTax, &totalTotal); err != nil {
		t.Fatalf("sum return sales: %v", err)
	}
	if totalCharge != 30 {
		t.Fatalf("two split refunds covering every unit must sum to the full 30 service charge, got %d", totalCharge)
	}
	if totalTax != 23 {
		t.Fatalf("split refund tax must sum to the original sale's exact 23 (20 line tax + 3 charge tax) -- got %d (pre-fix this landed on 22, one charge-tax unit short)", totalTax)
	}
	if totalTotal != 253 {
		t.Fatalf("split refund totals must sum to the original sale's exact 253, got %d", totalTotal)
	}
}

// TestPostRefund_LineDiscountedMultiPartialRefundNeverExceedsOriginalServiceCharge
// is ut-docs#1215's independent review finding B1: prorating the line
// discount for a partial-quantity refund floors (`int64(float64(l.
// LineDiscount) * share)`, ~30 lines up), so each request's own refunded
// net is slightly LARGER than its true proportional share -- summed across
// several sequential partial refunds of the SAME line, the cumulative net
// can exceed the original line's net entirely. Feeding that inflated net
// straight into the service-charge proration (uncapped) let the charge
// itself be over-refunded: a real driven repro during review turned a 299
// charge into 300 paid back (3 refunds of 1 unit each, single 0%-rate
// line, a 10 line discount that doesn't divide evenly by 3). The fix is
// the explicit clamp in the handler (against
// data.RefundedServiceChargeTotal, i.e. what's ACTUALLY already been paid
// back) -- this test pins that the cumulative charge refunded can never
// exceed the original 299, no matter how the per-request math floors.
func TestPostRefund_LineDiscountedMultiPartialRefundNeverExceedsOriginalServiceCharge(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-refund-b1-1215", "R-REFUND-B1-1215"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, service_charge_tax_basis_bp, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 290, 0, 0, 589, 299, 0, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-b1', ?, 1, 'itm-b1', 'Widget', 'W1', 3, 100, 10, 0, 0, 290, 290)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-b1', ?, 'cash', 589, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("refund %d failed: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}

	var totalCharge int64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(service_charge_amount), 0) FROM sales WHERE sale_type = 'return'`).Scan(&totalCharge); err != nil {
		t.Fatalf("sum return service charges: %v", err)
	}
	if totalCharge > 299 {
		t.Fatalf("three 1-of-3 refunds must never pay back more than the original 299 service charge, got %d (over-refund)", totalCharge)
	}
	// 298, not 299, now that ut-docs#1531's running per-key discount clamp
	// has landed (100+100+98 -- the third request's now-EXACT line net
	// (96, was 97) lowers its own net-weight share of the charge proration
	// by one). This is the service-charge clamp's own pre-existing,
	// explicitly documented characteristic (see the "drift by a minor unit
	// in EITHER direction" comment ~15 lines up): it only ever guards
	// against OVER-refund, never tops the last request up to the exact
	// remainder, so the 1-unit drift this fixture happened to show as an
	// overage before #1531 now shows as a shortfall instead -- not a new
	// bug, just which direction this specific fixture's rounding lands in.
	if totalCharge != 298 {
		t.Fatalf("expected 100+100+98=298 in this fixture post-#1531 (never-exceeds is the real invariant above), got %d", totalCharge)
	}
	// ut-docs#1531 itself: the line SUBTOTAL (unlike the charge) now
	// recovers EXACTLY the original line's net, no drift in either
	// direction -- 97+97+96=290, the promise the old comment here deferred.
	var totalSubtotal int64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(total), 0) FROM sales WHERE sale_type = 'return'`).Scan(&totalSubtotal); err != nil {
		t.Fatalf("sum return totals: %v", err)
	}
	if want := int64(290 + 298); totalSubtotal != want {
		t.Fatalf("expected the three returns' total (line net 290 + charge 298) to equal %d exactly, got %d", want, totalSubtotal)
	}
}

// TestPostRefund_MultiPartialRefundNeverExceedsOriginalLineNet is
// ut-docs#1531's own acceptance criterion, isolated from the service-charge
// interaction the sibling B1 test above now also covers: a per-line-
// discounted, multi-partial (3+) refund of the same line whose discount
// does not divide evenly by the number of partial refunds must never let
// the cumulative refunded subtotal exceed the original line's net, at any
// intermediate step, and must land exactly on it once every unit is back.
//
// Same driven repro as the card: 3 @ 100, a line discount of 10 (10/3 does
// not divide evenly), refunded 1 unit at a time across 3 separate requests.
// Before the running per-key clamp in the handler, each request
// independently floored its own share (`int64(float64(10) * (1.0/3))` = 3
// every time), so the true 10 discount only ever came back as 3*3=9 --
// summed net 97+97+97=291, one minor unit over the line's actual 290 net.
func TestPostRefund_MultiPartialRefundNeverExceedsOriginalLineNet(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-refund-1531", "R-REFUND-1531"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 290, 0, 0, 290, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-1531', ?, 1, 'itm-1531', 'Widget', 'W1', 3, 100, 10, 0, 0, 290, 290)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-1531', ?, 'cash', 290, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}

	const originalLineNet = 290
	var cumulative int64
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("refund %d failed: %d %s", i+1, rec.Code, rec.Body.String())
		}
		var total int64
		if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(total), 0) FROM sales WHERE sale_type = 'return'`).Scan(&total); err != nil {
			t.Fatalf("sum return totals after refund %d: %v", i+1, err)
		}
		if total > originalLineNet {
			t.Fatalf("refund %d: cumulative refunded subtotal %d exceeds the original line's net %d (over-refund)", i+1, total, originalLineNet)
		}
		cumulative = total
	}
	if cumulative != originalLineNet {
		t.Fatalf("expected the three 1-of-3 refunds to sum to exactly the original line net %d, got %d", originalLineNet, cumulative)
	}
}

// TestPostRefund_SiblingLinesWithDifferentDiscountsDoNotCrossAttribute is
// ut-docs#1531's own independent-review finding F1: two ORIGINAL lines can
// share a refund-line key (same item/variant/price/mode) while carrying
// DIFFERENT LineDiscount amounts -- e.g. the same product scanned twice as
// separate lines, only one of which a cashier manually discounted.
// Aggregating discount by key (this card's main fix) is only exact when
// every line under a key shares one discount rate; naively pooling it here
// would let one line's discount be given back against the OTHER line's own
// refund. This pins the fallback: a non-uniform key's lines are each
// refunded against their OWN discount, independently, exactly as before
// this card (no cross-attribution) rather than a blended/wrong amount.
func TestPostRefund_SiblingLinesWithDifferentDiscountsDoNotCrossAttribute(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-refund-f1-1531", "R-REFUND-F1-1531"
	// subtotal = 100 (line 0, undiscounted) + 50 (line 1, net after its own
	// 50 discount) = 150.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 150, 0, 0, 150, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	// Both lines: same item/variant/price/mode -> SAME refund-line key.
	// Line 0: no discount (net 100). Line 1: 50 discount (net 50).
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-f1-a', ?, 1, 'itm-f1', 'Widget', 'W1', 1, 100, 0, 0, 0, 100, 100)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-f1-b', ?, 2, 'itm-f1', 'Widget', 'W1', 1, 100, 50, 0, 0, 50, 50)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-f1', ?, 'cash', 150, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}

	// Refund line 1 (the discounted one) ALONE first.
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_1=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refunding the discounted line failed: %d %s", rec.Code, rec.Body.String())
	}
	var afterFirst int64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(total), 0) FROM sales WHERE sale_type = 'return'`).Scan(&afterFirst); err != nil {
		t.Fatalf("sum return totals: %v", err)
	}
	// Bug shape (pooled-by-key, no fallback): floor(50*1/2)=25 net 75 --
	// 25 over the discounted line's true 50 net. Must be exactly 50.
	if afterFirst != 50 {
		t.Fatalf("refunding line 1 (discount 50) alone: expected exactly its own net 50, got %d (cross-attributed from/to line 0)", afterFirst)
	}

	// Then refund line 0 (the undiscounted one) alone.
	req2 := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("refunding the undiscounted line failed: %d %s", rec2.Code, rec2.Body.String())
	}
	var total int64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(total), 0) FROM sales WHERE sale_type = 'return'`).Scan(&total); err != nil {
		t.Fatalf("sum return totals: %v", err)
	}
	// Bug shape: line 0 would have been shorted to 100-25=75 too high a
	// discount taken previously would leave 100-25=75; must be exactly 100
	// more (150 total), not blended.
	if total != 150 {
		t.Fatalf("expected both lines' true nets (50 + 100 = 150) with no cross-attribution, got %d", total)
	}
}

// TestPostRefund_FractionalQuantityNeverExceedsOriginalLineNet is ut-docs#1531
// review finding F4: cumulativeQty (keyQty[key]-pool[key]) is a float, and a
// fractional original Qty need not land exactly on the key's total even
// after every unit has genuinely been refunded. Flooring a target discount
// against a cumulativeQty that's a hair short of the true total under-shoots
// by a minor unit, reviving the exact over-refund this card exists to
// eliminate. Pins the epsilon snap that treats "within 1e-9 of fully
// refunded" as exactly fully refunded.
//
// Round-2 review finding B2: an earlier version of this test used 0.3 sliced
// as three 0.1s -- textbook float64 (0.1+0.1+0.1 == 0.30000000000000004),
// but on the SAFE side of the boundary (cumulativeQty ends up slightly ABOVE
// the true total, not below), so it passed identically with the epsilon
// snap disabled -- a false-pass that would have let a future refactor
// delete the snap and ship green. 0.9 sliced as three 0.3s lands on the
// dangerous side instead (cumulativeQty 0.89999999999999991118, a hair
// BELOW the true 0.90000000000000002220) and was confirmed, driven against
// the real handler, to fail without the snap (870 > true net 869) and pass
// with it.
func TestPostRefund_FractionalQuantityNeverExceedsOriginalLineNet(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-refund-f4-1531", "R-REFUND-F4-1531"
	// 0.9 units @ 1000, discount 31 -> net 869 (900 - 31).
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 869, 0, 0, 869, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-f4', ?, 1, 'itm-f4', 'Weighed Widget', 'WW1', 0.9, 1000, 31, 0, 0, 869, 869)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-f4', ?, 'cash', 869, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}

	const originalLineNet = 869
	var cumulative int64
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=0.3"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("refund %d failed: %d %s", i+1, rec.Code, rec.Body.String())
		}
		var total int64
		if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(total), 0) FROM sales WHERE sale_type = 'return'`).Scan(&total); err != nil {
			t.Fatalf("sum return totals after refund %d: %v", i+1, err)
		}
		if total > originalLineNet {
			t.Fatalf("refund %d: cumulative refunded subtotal %d exceeds the original line's net %d (over-refund, float-boundary miss)", i+1, total, originalLineNet)
		}
		cumulative = total
	}
	if cumulative != originalLineNet {
		t.Fatalf("expected three 0.3-of-0.9 refunds to sum to exactly the original line net %d, got %d", originalLineNet, cumulative)
	}
}

// TestPostRefund_FullRefundOfFullyDiscountedLineWithChargeStillSucceeds is
// ut-docs#1215's independent review finding B2: switching the service-
// charge proration guard from `origGross > 0` to `origNetWeight > 0`
// (net-after-discount can be zero even when gross is positive -- e.g.
// every line is 100% line-discounted) made a previously-refundable sale
// fail outright: the charge refund computed to 0, so the required payment
// was 0, and pos.CompleteSale rejects a non-positive payment amount. This
// pins that such a sale -- a fully line-discounted line, still carrying a
// legitimate service charge (ADR-0061's own zero-weight rule already
// allows this on the SALE side, landing the whole charge+tax on the
// highest band) -- can still be refunded in full, via the gross fallback.
func TestPostRefund_FullRefundOfFullyDiscountedLineWithChargeStillSucceeds(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-refund-b2-1215", "R-REFUND-B2-1215"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, service_charge_tax_basis_bp, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 0, 0, 6, 36, 30, 0, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-b2', ?, 1, 'itm-b2', 'Widget', 'W2', 1, 100, 100, 2000, 0, 0, 0)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-b2', ?, 'cash', 36, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund failed: %d %s (pre-fix regression: this used to 400 with a fully line-discounted line)", rec.Code, rec.Body.String())
	}

	var returnCharge, returnTax, returnTotal int64
	if err := dp.Db.QueryRow(`SELECT service_charge_amount, tax_total, total FROM sales WHERE sale_type = 'return'`).Scan(&returnCharge, &returnTax, &returnTotal); err != nil {
		t.Fatalf("read return sale: %v", err)
	}
	if returnCharge != 30 || returnTax != 6 || returnTotal != 36 {
		t.Fatalf("expected the full charge/tax/total (30/6/36) back via the gross fallback, got %d/%d/%d", returnCharge, returnTax, returnTotal)
	}
}

// TestPostRefund_RefundingOnlyAFullyDiscountedLineStillSucceeds is
// ut-docs#1215's independent review finding B3 (round 2): the B2 fallback
// above keys on the WHOLE SALE's net weight (`origNetWeight == 0`), so it
// doesn't engage when the sale overall has positive net weight but THIS
// PARTICULAR request only refunds a comped/BOGO/staff-freebie line (net
// 0, gross > 0) while a DIFFERENT, unrefunded line elsewhere in the sale
// is what makes origNetWeight positive. Driven repro during review: a
// sale with a comped line (3 @ 100, fully line-discounted, net 0) plus a
// full-price line (1 @ 100, net 100) and a 50 service charge -- refunding
// the comped line ALONE used to 400 ("Sale could not be completed") on
// this branch even though the identical request succeeds on main. Fixed
// by keying the fallback on THIS REQUEST's own refundNetWeight too, not
// just the sale's origNetWeight.
func TestPostRefund_RefundingOnlyAFullyDiscountedLineStillSucceeds(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	saleID, receiptNo := "sale-refund-b3-1215", "R-REFUND-B3-1215"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, service_charge_tax_basis_bp, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 100, 0, 0, 150, 50, 0, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	// Line 0: the comped line -- 3 units @ 100, fully line-discounted
	// (net 0), still positive gross (300).
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-b3-comped', ?, 1, 'itm-b3-comped', 'Comped Widget', 'C1', 3, 100, 300, 0, 0, 0, 0)`, saleID); err != nil {
		t.Fatal(err)
	}
	// Line 1: full-price -- 1 unit @ 100, no discount (net 100). This is
	// what makes origNetWeight positive for the sale overall.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-refund-b3-full', ?, 2, 'itm-b3-full', 'Full Widget', 'F1', 1, 100, 0, 0, 0, 100, 100)`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-refund-b3', ?, 'cash', 150, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}

	// Refund the comped line (index 0) ALONE first -- the exact shape B3
	// found broken (this request's own refundNetWeight is 0, even though
	// the sale's origNetWeight is 100 from the untouched full-price line).
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refunding the comped line alone failed: %d %s (this must succeed -- it works on main today)", rec.Code, rec.Body.String())
	}

	// Then refund the full-price line (index 1) in a SEPARATE request --
	// the clamp (B1) must still land the cumulative charge on the exact
	// original 50, not over- or under-shoot it.
	req2 := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_1=1"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("refunding the remaining full-price line failed: %d %s", rec2.Code, rec2.Body.String())
	}

	var totalCharge int64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(service_charge_amount), 0) FROM sales WHERE sale_type = 'return'`).Scan(&totalCharge); err != nil {
		t.Fatalf("sum return service charges: %v", err)
	}
	if totalCharge != 50 {
		t.Fatalf("expected the two refunds to land on the exact original 50 service charge (37 gross-fallback + 13 clamped), got %d", totalCharge)
	}
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

// ut-docs#944: a generic internal DB failure on the refund path, so it shows
// the refund-flow key (refund.error.server) -- deliberately not pos_api.go's
// sale-worded pos.toast.tender_failed, even though the same repo method is
// being called.
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
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("expected the localized refund.error.server copy, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Sale could not be completed") {
		t.Fatalf("refund flow showed the sale-worded pos.toast.tender_failed copy: %s", rec.Body.String())
	}
}

// ut-docs#944: same reasoning as the EnsureStockLocation test above --
// refund-flow key, not the sale-worded tender one.
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
	if !strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Fatalf("expected the localized refund.error.server copy, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Sale could not be completed") {
		t.Fatalf("refund flow showed the sale-worded pos.toast.tender_failed copy: %s", rec.Body.String())
	}
}

// ut-docs#950 (flagged by the ut-docs#944 review as a separate follow-up,
// ut-docs#924 increment 2): the payment-provider refund gate
// (payment.<key>.refund) leaked a raw PLUGIN-originated error string,
// untranslated -- http.Error(w, "provider refund failed for "+method+": "+
// blocked.Error(), 402). `blocked` comes from a third-party plugin's own
// response, so it must never reach the operator verbatim, same policy
// ut-docs#921 (F2) already established for the sibling payment.<key>.authorize
// gate in pos_api.go's completeTender (mirrors
// TestTenderHandler_DeclinedPaymentShowsLocalizedMessageNotRawError).
func TestPostRefund_ProviderRefundDeclinedShowsLocalizedMessageNotRawError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

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
	          VALUES ('h1', 'com.universaltill.payment-demo', 'payment.demopay.refund', 'handle_refund', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.payment-demo', 'events:receive', 1)`); err != nil {
		t.Fatal(err)
	}

	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers() // process-global singleton; isolate from other tests using the same plugin id/event
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.refund", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.refund"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return nil, errors.New("demopay: refund window expired, contact the customer's bank")
		}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2&method=demopay"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("want 402 on a declined provider refund, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "refund window expired") || strings.Contains(rec.Body.String(), "provider refund failed") {
		t.Fatalf("raw plugin-originated error text leaked into the operator-facing response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "The payment provider declined this refund") {
		t.Fatalf("expected the localized refund.error.provider_declined copy, got: %s", rec.Body.String())
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

// ut-docs#1008 review, blocker F1 — the real-money case, end to end: an
// INCLUSIVE-priced sale that also issued a voucher, refunded through the
// real POST /api/refund handler. Before the fix, the voucher's face value
// unbalanced pos.InferTaxInclusive's identity, the sale was misread as
// EXCLUSIVE, and computeRefundTotal added 19% VAT on top of the
// already-inclusive line price — refunding 14.16 in cash for goods the
// customer paid 11.90 for. The original sale goes through the real
// pos.CompleteSale (never hand-inserted rows) so the header the inference
// reads is exactly what production persists.
func TestPostRefund_InclusiveSaleWithVoucherIssueRefundsInclusiveAmount(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()

	if _, err := dp.Db.Exec(`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('voucher', 'Voucher', 'voucher', 1)`); err != nil {
		t.Fatalf("seed voucher method: %v", err)
	}

	// 11.90 inclusive at 19% (tax inside: 1.90) + a 15.00 voucher issue.
	saleID, err := pos.CompleteSale(ctx, dp.Db, pos.SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm1", SKU: "ABC", Name: "Apple", Qty: 1,
			UnitPrice: money.FromMinor(1190), TaxRateBasisPoints: 1900,
			LocationID: "loc_main",
		}},
		VoucherIssues:          []pos.VoucherIssueInput{{VoucherID: "GS-RF1", Amount: money.FromMinor(1500)}},
		Payments:               []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2690)}},
		AllowNegativeInventory: true,
	})
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var receiptNo string
	if err := dp.Db.QueryRow(`SELECT receipt_no FROM sales WHERE id = ?`, saleID).Scan(&receiptNo); err != nil {
		t.Fatalf("read receipt no: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund failed: %d %s", rec.Code, rec.Body.String())
	}

	// The cash handed back must be the inclusive price the customer paid
	// (11.90), NOT 11.90 + a second 19% VAT application (14.16 — the exact
	// overpayment the reviewer's probe produced).
	var refundTotal, refundPaid int64
	if err := dp.Db.QueryRow(`SELECT total FROM sales WHERE sale_type = 'return'`).Scan(&refundTotal); err != nil {
		t.Fatalf("read return sale: %v", err)
	}
	if err := dp.Db.QueryRow(`SELECT p.amount FROM payments p JOIN sales s ON s.id = p.sale_id WHERE s.sale_type = 'return'`).Scan(&refundPaid); err != nil {
		t.Fatalf("read return payment: %v", err)
	}
	if refundTotal != 1190 || refundPaid != 1190 {
		t.Fatalf("refund total/paid = %d/%d, want 1190/1190 (an inflated figure means the sale was misread as tax-exclusive)", refundTotal, refundPaid)
	}
}
