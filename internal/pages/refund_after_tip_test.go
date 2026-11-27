package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A refund after a sale that took a tip returns the goods money only: the
// 12 tip stays the staff's and is not refunded, and the return leg carries no tip.
func TestPostRefund_AfterTipSaleRefundsGoodsNotTip(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := dp.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('sale-tip', 'R-TIP', 'completed', 'sale', 'GBP', 600, 0, 0, 600, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-tip', 'sale-tip', 1, 'itm-a', 'Apple', 'ABC', 2, 300, 0, 0, 600, 600)`); err != nil {
		t.Fatal(err)
	}
	// Card payment 6.00 + 0.72 tip (12 percent), the amount includes the tip.
	if _, err := dp.Db.Exec(`INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, tip_recipient, paid_at) VALUES('pay-tip', 'sale-tip', 'card', 672, 'GBP', 0, 72, 'employee', ?)`, now); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt=R-TIP&qty_0=1&method=card"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("refund: %d %s", rr.Code, rr.Body.String())
	}
	var amount, tip int64
	if err := dp.Db.QueryRow(`SELECT p.amount, p.tip_amount FROM sales s JOIN payments p ON p.sale_id = s.id WHERE s.sale_type = 'return'`).Scan(&amount, &tip); err != nil {
		t.Fatalf("read refund: %v", err)
	}
	if amount != 300 || tip != 0 {
		t.Fatalf("refund payment amount/tip = %d/%d, want 300/0 (one unit of goods, no tip)", amount, tip)
	}
	var origTip int64
	if err := dp.Db.QueryRow(`SELECT tip_amount FROM payments WHERE id = 'pay-tip'`).Scan(&origTip); err != nil || origTip != 72 {
		t.Fatalf("original tip = %d (%v), want 72 untouched", origTip, err)
	}
}
