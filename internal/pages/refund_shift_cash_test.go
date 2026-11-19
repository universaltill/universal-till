package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5803: a cash refund made through POST /api/refund must reach the
// shift's expected cash. The refund handler used to build its SaleInput with
// no RegisterID, so the return row was stored with sales.register_id NULL and
// the shift window (s.register_id = ?) never saw it.
func TestPostRefund_CashRefundReducesShiftExpectedCash(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	registerShiftsAPI(mux, dp)
	if _, err := dp.Db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role) VALUES('cashier1','cashier1','Cashier One','','cashier')`); err != nil {
		t.Fatal(err)
	}
	const reg = "reg-5803"
	if _, err := dp.Db.Exec(`INSERT INTO registers(id,name,is_active) VALUES(?, 'Front Till', 1)`, reg); err != nil {
		t.Fatal(err)
	}
	// This till's identity, as Settings -> Registers persists it.
	if err := dp.Settings.Set(t.Context(), pos.SettingsKeyTillRegisterID, reg); err != nil {
		t.Fatalf("set till register: %v", err)
	}

	rec := postShiftForm(t, mux, "/api/shifts/open", "register_id="+reg+"&cashier_id=cashier1&opening_cash=10000")
	if rec.Code != http.StatusOK {
		t.Fatalf("open shift: %d %s", rec.Code, rec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRow(`SELECT id FROM shifts WHERE closed_at IS NULL`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}

	// A completed cash sale on this register inside the shift: 2 x 3.00 = 6.00.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := dp.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at, completed_at)
VALUES('sale-5803', 'R-5803', 'completed', 'sale', 'GBP', 600, 0, 0, 600, ?, ?, ?)`, reg, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line-5803', 'sale-5803', 1, 'itm-a', 'Apple', 'ABC', 2, 300, 0, 0, 600, 600)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES('pay-5803', 'sale-5803', 'cash', 600, 'GBP', 0, ?)`, now); err != nil {
		t.Fatal(err)
	}

	// Refund one of the two units in cash through the real handler.
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt=R-5803&qty_0=1&method=cash"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("refund: %d %s", rr.Code, rr.Body.String())
	}
	var refundMinor int64
	var retReg *string
	if err := dp.Db.QueryRow(`SELECT p.amount, s.register_id FROM sales s JOIN payments p ON p.sale_id = s.id WHERE s.sale_type = 'return'`).Scan(&refundMinor, &retReg); err != nil {
		t.Fatalf("read refund: %v", err)
	}
	if refundMinor != 300 {
		t.Fatalf("refund payment = %d, want 300", refundMinor)
	}
	if retReg == nil || *retReg != reg {
		t.Fatalf("return sales.register_id = %v, want %q", retReg, reg)
	}

	rec = postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+shiftID+`","closing_cash":10300}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	var expected int64
	if err := dp.Db.QueryRow(`SELECT expected_cash FROM shifts WHERE id = ?`, shiftID).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if want := int64(10000 + 600 - 300); expected != want {
		t.Fatalf("expected_cash = %d, want opening+sale-refund = %d", expected, want)
	}
}
