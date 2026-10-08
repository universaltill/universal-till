package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// --- ut-docs#3408 / ADR-0146: a fiscal.sign.ask "cannot-sign" answer on a
// refund is refused wherever no money has moved electronically yet (a
// cash/hookless refund). A refund whose
// payment.<key>.refund hook already sent the money back keeps
// proceed-and-declare (ADR-0146 Decision 3, ut-docs#3556) — pinned by
// TestFiscalSignAsk_CannotSignOnProviderRefundStillDeclaresWithDifferentWording
// in fiscal_sign_hook_test.go. ---

// refundCannotSignCopy is a stable fragment of en.json's
// refund.error.fiscal_cannot_sign.
const refundCannotSignCopy = "can't sign this refund as presented"

func TestRefund_CannotSignCashRefundIsRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	subscribeCannotSignSigner(t, dp)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2&method=cash"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("a cash refund the signer can't sign must be refused with 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), refundCannotSignCopy) {
		t.Fatalf("want the refund.error.fiscal_cannot_sign copy, got: %s", rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales WHERE sale_type = 'return'"); n != 0 {
		t.Fatalf("a refused refund must record no return sale, got %d", n)
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionCannotSign); n != 0 {
		t.Fatalf("a refused refund has nothing to journal: want 0 %s rows, got %d", fiscalSignGapActionCannotSign, n)
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionSigning); n != 0 {
		t.Fatalf("cannot-sign is not an outage: want 0 %s rows, got %d", fiscalSignGapActionSigning, n)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM audit_log WHERE action = 'refund'"); n != 0 {
		t.Fatalf("a refused refund must write no refund audit row, got %d", n)
	}
}

// A refund method with a payment entry but no payment.<key>.refund
// subscriber moves no money either (blockingPaymentEventDispatch reports
// delivered=false), so it is refused exactly like cash.
func TestRefund_CannotSignHooklessMethodIsRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	subscribeCannotSignSigner(t, dp)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2&method=voucherless"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("a hookless refund the signer can't sign must be refused with 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales WHERE sale_type = 'return'"); n != 0 {
		t.Fatalf("a refused refund must record no return sale, got %d", n)
	}
}

// An unreachable signer is an outage, not a property of the refund's data:
// it keeps proceed-and-declare even for a cash refund (ADR-0146 Decision 2).
func TestRefund_UnreachableCashRefundStillCompletesAndDeclares(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-unreachable", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"unreachable"}`), nil
	})
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("an outage must not refuse the refund, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionSigning); n != 1 {
		t.Fatalf("want 1 %s marker for the outage, got %d", fiscalSignGapActionSigning, n)
	}
}
