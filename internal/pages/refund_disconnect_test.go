package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6784: once the provider's refund hook has moved money, a client that
// disconnects (request context cancelled) must not stop the return from being
// recorded -- else money is out with no return row and no per-leg record, and
// the next refund of the sale is not netted. The disconnect lands INSIDE the
// hook (the fake, like a real WASM guest, fails if its ctx is gone), so the
// hook dispatch itself must run detached.
func TestRefund_ClientDisconnectAfterProviderRefundStillRecordsReturn(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt := rig.receiptNo(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The provider approves (money moves), then the client goes away.
	refundHookGates.Store(rig.dp.Db, func() { cancel() })
	t.Cleanup(func() { refundHookGates.Delete(rig.dp.Db) })

	req := httptest.NewRequest(http.MethodPost, "/api/refund",
		strings.NewReader("receipt="+receipt+"&qty_0=1&method=demopay")).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)

	if n := len(rig.refunds()); n != 1 {
		t.Fatalf("provider refund hook calls = %d, want 1 (money moved)", n)
	}
	if n := countRows(t, rig.dp, `SELECT COUNT(*) FROM sales s JOIN sale_links l ON l.sale_id = s.id WHERE s.sale_type = 'return'`); n != 1 {
		t.Fatalf("return sales recorded = %d, want 1 after the provider refunded (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if n := countRows(t, rig.dp, `SELECT COUNT(*) FROM refund_leg_refunds`); n < 1 {
		t.Fatalf("per-leg refund rows = %d, want >= 1", n)
	}
}

// ut-docs#6910 (inverts the #6784 AC2 guard): a signer was asked and approved
// the cash refund, then the client left. The signed return must be recorded
// (ADR-0044 D1; contract 1.4.0 forbids a belated re-sign).
func TestRefund_ClientDisconnectAfterCashSignStillRecordsReturn(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-disconnect", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		cancel() // client disconnects after the signer approved
		return json.RawMessage(`{"status":"approved"}`), nil
	})

	rec := postRefundCtx(t, mux, ctx, receiptNo)

	if asked.Load() != 1 {
		t.Fatalf("fiscal.sign.ask answered %d times, want 1 (the disconnect must happen mid-request)", asked.Load())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`); n != 1 {
		t.Fatalf("a signed cash refund must be recorded despite the disconnect, got %d returns (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if rec.Code >= 500 {
		t.Fatalf("response %d: %s", rec.Code, rec.Body.String())
	}
}

// ut-docs#6910: the client is already gone before the ask is dispatched:
// nothing signed, nothing recorded.
func TestRefund_ClientDisconnectBeforeCashSignRecordsNothing(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var asked atomic.Int32
	refundBeforeFiscalSignHook = cancel // gone at the last cancellable point
	t.Cleanup(func() { refundBeforeFiscalSignHook = nil })
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6910-rbefore-ask", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(`{"status":"approved"}`), nil
	})

	postRefundCtx(t, mux, ctx, receiptNo)

	if asked.Load() != 0 {
		t.Fatalf("fiscal.sign.ask answered %d times, want 0", asked.Load())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`); n != 0 {
		t.Fatalf("returns = %d, want 0", n)
	}
}

// ut-docs#6910 / ADR-0146: cannot-sign refuses the refund (409, no return)
// whether or not the client is still there.
func TestRefund_ClientDisconnectAfterCashSignCannotSignRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6910-rcannot", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		cancel()
		return json.RawMessage(`{"status":"cannot-sign"}`), nil
	})

	rec := postRefundCtx(t, mux, ctx, receiptNo)

	if rec.Code != http.StatusConflict {
		t.Fatalf("response %d, want 409 (ADR-0146): %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`); n != 0 {
		t.Fatalf("returns = %d, want 0", n)
	}
}

func postRefundCtx(t *testing.T, mux *http.ServeMux, ctx context.Context, receiptNo string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/refund",
		strings.NewReader("receipt="+receiptNo+"&qty_0=2&method=cash")).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
