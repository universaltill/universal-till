package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#1833 (independent review): the scan-to-redeem pay-grid button
// (#pay-voucher-btn, web/ui/pages/index.html) is a plain hx-vals button, and
// no json-enc htmx extension is registered anywhere under web/ui -- so it
// posts /api/pos/tender FORM-ENCODED, taking the handler's form fallback
// rather than the JSON `payments` branch TestPOSTender_VoucherIssueAndRedeem
// covers. That fallback originally dropped voucher_id on the floor, so the
// tender silently degraded to a generic UNTRACKED voucher payment: the sale
// completed and the goods left the shop, but vouchers.balance was never
// debited and no 'redemption' voucher_transactions row was written -- the
// same voucher could then be redeemed again, without limit.
//
// This test pins the form-encoded path to the same tracked-redemption
// behaviour the JSON path already has.
func postTenderForm(t *testing.T, mux *http.ServeMux, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPOSTender_FormEncodedVoucherRedemptionIsTracked(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)

	// Issue a 15.00 voucher (same shape as the JSON-path test).
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":1500}],"issue_vouchers":[{"amount":1500,"code":"GS-F1"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("voucher issue failed: code %d body %s", rec.Code, rec.Body.String())
	}

	// 2.50 net + 20% exclusive = 3.00 due.
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}

	// Exactly what #pay-voucher-btn sends: amount already clamped to
	// min(balance, due) client-side, method + voucher_id alongside it.
	rec = postTenderForm(t, mux, "amount=300&method=voucher&voucher_id=GS-F1")
	if rec.Code != http.StatusOK {
		t.Fatalf("form-encoded voucher tender failed: code %d body %s", rec.Code, rec.Body.String())
	}

	var balance int64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-F1'`).Scan(&balance); err != nil {
		t.Fatalf("voucher row after redemption: %v", err)
	}
	if balance != 1200 {
		t.Fatalf("balance after a 3.00 form-encoded redemption = %d, want 1200 (voucher was not debited -- the redemption was recorded as an untracked voucher payment)", balance)
	}

	var redCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-F1' AND type = 'redemption'`).Scan(&redCount); err != nil {
		t.Fatalf("count redemptions: %v", err)
	}
	if redCount != 1 {
		t.Fatalf("redemption ledger rows = %d, want 1", redCount)
	}

	// The payments row must name the voucher too (payments.voucher_id,
	// migration 072) -- that is what makes the redemption traceable.
	var pmtVoucherID string
	if err := dp.Db.QueryRow(`SELECT COALESCE(voucher_id, '') FROM payments WHERE method_id = 'voucher'`).Scan(&pmtVoucherID); err != nil {
		t.Fatalf("payments row: %v", err)
	}
	if pmtVoucherID != "GS-F1" {
		t.Fatalf("payments.voucher_id = %q, want GS-F1 (untracked payment)", pmtVoucherID)
	}
}

// A form-encoded redemption for MORE than the voucher balance must be
// refused by exactly the same rule the JSON path enforces -- nothing
// debited, no sale persisted.
func TestPOSTender_FormEncodedVoucherOverspendRefused(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)

	rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":100}],"issue_vouchers":[{"amount":100,"code":"GS-F2"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("voucher issue failed: code %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}

	rec = postTenderForm(t, mux, "amount=300&method=voucher&voucher_id=GS-F2")
	if rec.Code != http.StatusOK {
		t.Fatalf("overspend rejection should render the basket with a toast (200), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Voucher balance does not cover") {
		t.Fatalf("overspend rejection missing the localized voucher toast: %s", rec.Body.String())
	}
	var balance int64
	if err := dp.Db.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-F2'`).Scan(&balance); err != nil {
		t.Fatalf("voucher row: %v", err)
	}
	if balance != 100 {
		t.Fatalf("balance after a refused form-encoded overspend = %d, want 100", balance)
	}
	var saleCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE tender_type = 'voucher'`).Scan(&saleCount); err != nil {
		t.Fatalf("count voucher sales: %v", err)
	}
	if saleCount != 0 {
		t.Fatalf("refused redemption persisted %d sale(s), want 0", saleCount)
	}
}

// An over-long voucher_id on the form path is refused at the boundary with
// the same 400 + bound the JSON path applies, before any DB work.
func TestPOSTender_FormEncodedVoucherIDBound(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan item: %v", err)
	}
	rec := postTenderForm(t, mux, "amount=300&method=voucher&voucher_id="+strings.Repeat("G", 65))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long form voucher_id: code %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}
