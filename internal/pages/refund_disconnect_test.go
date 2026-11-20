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

// AC2: no money moved electronically (cash, no refund subscriber). The client
// goes away mid-request, after the payment step (from the fiscal.sign.ask
// answerer), but BEFORE the return commits -- nothing is recorded, because the
// cashier hands cash over only after this request succeeds (ADR-0146 D1).
func TestRefund_ClientDisconnectCashRefundRecordsNothing(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-disconnect", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		cancel() // client disconnects after the payment step, before the commit
		return json.RawMessage(`{"status":"approved"}`), nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/refund",
		strings.NewReader("receipt="+receiptNo+"&qty_0=2&method=cash")).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if asked.Load() != 1 {
		t.Fatalf("fiscal.sign.ask answered %d times, want 1 (the disconnect must happen mid-request)", asked.Load())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`); n != 0 {
		t.Fatalf("a cash refund whose client left before the commit must record no return, got %d (response %d)", n, rec.Code)
	}
}
