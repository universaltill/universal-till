package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"strconv"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#1181 / ADR-0073 Decision 6 (independent review blocker B1): the
// same product sold once dine-in (19%) and once takeaway (7%) has the SAME
// gross unit price and DIFFERENT tax rates — a shape that could not exist
// before. The refund pool must be keyed per mode, or a cashier could refund
// both units against the 19% row and reclaim VAT collected at 7%.
func seedMixedSaleForRefund(t *testing.T, dp *commonDepsLike) (saleID, receiptNo string) {
	t.Helper()
	ctx := context.Background()
	saleID, receiptNo = "sale-refund-mixed", "R-REFUND-MIXED"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, order_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'mixed', 'GBP', 200, 0, 26, 226, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, order_type)
VALUES('line-mixed-1', ?, 1, 'itm1', 'Apple', 'ABC', 1, 100, 1900, 19, 100, 119, '')`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, order_type)
VALUES('line-mixed-2', ?, 2, 'itm1', 'Apple', 'ABC', 1, 100, 700, 7, 100, 107, 'takeaway')`, saleID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-mixed', ?, 'cash', 226, 'GBP', 0, datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}
	return saleID, receiptNo
}

func TestRefund_MixedSaleKeysPoolPerOrderType(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedMixedSaleForRefund(t, dp)

	// The refund page shows a per-line mode marker so the two identical
	// rows are distinguishable.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/refund/"+receiptNo, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET refund: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-testid="refund-line-mode-1"`) {
		t.Fatalf("expected a per-line order-type marker on the refund page, got: %s", rec.Body.String())
	}

	// Refunding 2 against the dine-in row (only 1 sold dine-in) must be
	// refused — the takeaway unit is a different pool.
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("over-refund against the dine-in row: expected 409, got %d %s", rec.Code, rec.Body.String())
	}

	// A legitimate one-of-each refund records each return line with its
	// original mode and tax rate, and the return header derives "mixed".
	req = httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=1&qty_1=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund: %d %s", rec.Code, rec.Body.String())
	}
	rows, err := dp.Db.Query(`SELECT l.order_type, l.tax_rate_bp FROM sale_lines l JOIN sales s ON s.id=l.sale_id WHERE s.sale_type='return' ORDER BY l.line_no`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var ot string
		var bp int
		_ = rows.Scan(&ot, &bp)
		got = append(got, ot+"@"+itoa(bp))
	}
	if len(got) != 2 || got[0] != "@1900" || got[1] != "takeaway@700" {
		t.Fatalf("return lines = %v, want [@1900 takeaway@700]", got)
	}
	var header string
	_ = dp.Db.QueryRow(`SELECT order_type FROM sales WHERE sale_type='return'`).Scan(&header)
	if header != pos.OrderTypeMixed {
		t.Fatalf("return header = %q, want mixed", header)
	}
	// And the already-returned aggregate is per mode too.
	returned, err := data.NewPOSRepo(dp.Db).ReturnedQuantities(context.Background(), "sale-refund-mixed")
	if err != nil {
		t.Fatal(err)
	}
	if returned[data.RefundLineKey("itm1", "", 100, "")] != 1 || returned[data.RefundLineKey("itm1", "", 100, "takeaway")] != 1 {
		t.Fatalf("ReturnedQuantities = %v, want 1 per mode", returned)
	}
}

func TestRefundableLines_DifferentModesDoNotSharePool(t *testing.T) {
	detail := data.SaleDetail{Lines: []data.SaleDetailLine{
		{Name: "Apple", SKU: "A1", ItemID: "i1", UnitPrice: 100, Qty: 1, OrderType: ""},
		{Name: "Apple", SKU: "A1", ItemID: "i1", UnitPrice: 100, Qty: 1, OrderType: "takeaway"},
	}}
	// One takeaway unit already returned must not eat the dine-in unit.
	lines := refundableLines(detail, map[string]float64{data.RefundLineKey("i1", "", 100, "takeaway"): 1})
	if lines[0].Remaining != 1 || lines[1].Remaining != 0 {
		t.Fatalf("expected dine-in 1 / takeaway 0 remaining, got %+v", lines)
	}
	if lines[0].OrderType != "" || lines[1].OrderType != "takeaway" {
		t.Fatalf("refund view must carry the line mode, got %+v", lines)
	}
}

type commonDepsLike = common.Deps

func itoa(i int) string { return strconv.Itoa(i) }

// Review B2 (ut-docs#1181): a historic takeaway sale that was FULLY refunded
// before the upgrade — its return persisted with header ” and untyped
// lines — must still show 0 remaining and reject a second refund after
// migration 077's backfill keys the pool per mode.
func TestRefund_HistoricTakeawaySaleAlreadyRefunded_StillRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	ctx := context.Background()
	x := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Post-077 shape of a pre-upgrade takeaway sale + its full return, i.e.
	// what the backfill must have produced: sale lines takeaway, return
	// lines takeaway (derived through sale_links), return header takeaway.
	x(`INSERT INTO sales(id, receipt_no, status, sale_type, order_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at) VALUES('s-hist','R-HIST','completed','sale','takeaway','GBP',100,0,7,107,datetime('now'),datetime('now'))`)
	x(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, order_type) VALUES('l-hist','s-hist',1,'itm1','Apple','ABC',1,100,700,7,100,107,'takeaway')`)
	x(`INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('p-hist','s-hist','cash',107,'GBP',0,datetime('now'))`)
	x(`INSERT INTO sales(id, receipt_no, status, sale_type, order_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at) VALUES('s-hist-ret','R-HIST-RET','completed','return','takeaway','GBP',100,0,7,107,datetime('now'),datetime('now'))`)
	x(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, order_type) VALUES('l-hist-ret','s-hist-ret',1,'itm1','Apple','ABC',1,100,700,7,100,107,'takeaway')`)
	x(`INSERT INTO sale_links(id, sale_id, original_sale_id, reason) VALUES('lnk-hist','s-hist-ret','s-hist','return')`)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/refund/R-HIST", nil))
	if !strings.Contains(rec.Body.String(), "refund.fully_refunded") && !strings.Contains(rec.Body.String(), "Fully refunded") && !strings.Contains(rec.Body.String(), "fully refunded") {
		t.Fatalf("expected the line to show as fully refunded, got: %s", rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt=R-HIST&qty_0=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second refund of a fully-refunded historic takeaway sale: want 409, got %d %s", rec.Code, rec.Body.String())
	}
}

// LOW-4: a uniform sale's refund page carries NO per-line mode marker.
func TestRefund_UniformSaleHasNoLineModeMarker(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/refund/"+receiptNo, nil))
	if strings.Contains(rec.Body.String(), "refund-line-mode-") {
		t.Fatalf("uniform sale must not show per-line mode markers: %s", rec.Body.String())
	}
}
