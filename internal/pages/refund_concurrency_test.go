package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6742: concurrent refunds of one sale are serialised per sale id.
// Ordering uses channels only: a gate in the fake plugin's refund hook
// (refundHookGates) holds the provider call open, and the lock's onWait seam
// (lockWaitSignals) says when a second request is parked behind it.

type hookGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *hookGate) open() { g.once.Do(func() { close(g.release) }) }

func (r *refCardRig) gateRefundHook(t *testing.T) *hookGate {
	t.Helper()
	g := &hookGate{entered: make(chan struct{}, 16), release: make(chan struct{})}
	refundHookGates.Store(r.dp.Db, func() {
		g.entered <- struct{}{}
		<-g.release
	})
	t.Cleanup(func() { g.open(); refundHookGates.Delete(r.dp.Db) })
	return g
}

func (r *refCardRig) postRefundAsync(ctx context.Context, receipt string, qty int) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/refund",
			strings.NewReader(fmt.Sprintf("receipt=%s&qty_0=%d&method=demopay", receipt, qty))).WithContext(ctx)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		r.mux.ServeHTTP(rec, req)
		out <- rec
	}()
	return out
}

func (r *refCardRig) saleID(t *testing.T, nth int) string {
	t.Helper()
	var id string
	if err := r.dp.Db.QueryRow(`SELECT id FROM sales WHERE sale_type != 'return' ORDER BY rowid LIMIT 1 OFFSET ?`, nth-1).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *refCardRig) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := r.dp.Db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func newDemoRig(t *testing.T) *refCardRig {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	return newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
}

// AC1: a 120 sale paid 80 + 40; two refunds of the one unit must not both go
// through to the provider.
func TestRefundConcurrency_SplitTenderTwoRefundsCannotOverRefund(t *testing.T) {
	rig := newDemoRig(t)
	rig.sell(t, `[{"method":"demopay","amount":80},{"method":"demopay","amount":40}]`)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)
	gate := rig.gateRefundHook(t)

	a := rig.postRefundAsync(context.Background(), receipt, 1)
	recvWithin(t, gate.entered, "A to reach the provider")
	b := rig.postRefundAsync(context.Background(), receipt, 1)
	recvEither(t, waits, "B to park behind A", gate.entered, "B to reach the provider")
	gate.open()
	ra := recvWithin(t, a, "refund A to finish")
	rb := recvWithin(t, b, "refund B to finish")

	if !((ra.Code == 200 && rb.Code == 409) || (ra.Code == 409 && rb.Code == 200)) {
		t.Fatalf("codes = %d, %d; want one 200 and one 409 (%s | %s)", ra.Code, rb.Code, ra.Body.String(), rb.Body.String())
	}
	got := rig.refunds()
	if len(got) != 1 {
		t.Fatalf("provider refund calls = %d, want 1", len(got))
	}
	var sum float64
	for _, v := range legAmounts(t, got[0]) {
		sum += v
	}
	if sum > 120 {
		t.Errorf("provider asked to refund %v of a 120 sale", sum)
	}
	if n := rig.count(t, `SELECT COUNT(*) FROM sales WHERE sale_type='return'`); n != 1 {
		t.Errorf("return sales = %d, want 1", n)
	}
	if n := rig.count(t, `SELECT COUNT(*) FROM refund_leg_refunds`); n != 2 {
		t.Errorf("refund_leg_refunds rows = %d, want 2 (one return's two legs)", n)
	}
	if n := rig.count(t, `SELECT COUNT(*) FROM (SELECT original_payment_id FROM refund_leg_refunds GROUP BY original_payment_id HAVING SUM(amount) > (SELECT amount FROM payments p WHERE p.id = original_payment_id))`); n != 0 {
		t.Errorf("%d legs refunded beyond their amount", n)
	}
}

// AC2: a cash sale has no provider backstop; two refunds of the same unit
// must record one return.
func TestRefundConcurrency_CashSameLineRefundedOnce(t *testing.T) {
	rig := newDemoRig(t)
	rig.sell(t, `[{"method":"cash","amount":120}]`)
	saleID := rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)

	hold, err := refundSaleLocks.lock(context.Background(), saleID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hold) // idempotent; frees the lock if a guard fatals first
	post := func() <-chan *httptest.ResponseRecorder {
		out := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/api/refund",
				strings.NewReader("receipt="+rig.receiptNo(t, 1)+"&qty_0=1&method=cash"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			rig.mux.ServeHTTP(rec, req)
			out <- rec
		}()
		return out
	}
	a, b := post(), post()
	recvWithin(t, waits, "first request to park")
	recvWithin(t, waits, "second request to park") // both requests are parked behind the held lock
	hold()
	ra := recvWithin(t, a, "refund A to finish")
	rb := recvWithin(t, b, "refund B to finish")
	codes := []int{ra.Code, rb.Code}
	if !((codes[0] == 200 && codes[1] == 409) || (codes[0] == 409 && codes[1] == 200)) {
		t.Fatalf("codes = %v; want one 200 and one 409 (%s | %s)", codes, ra.Body.String(), rb.Body.String())
	}
	if n := rig.count(t, `SELECT COUNT(*) FROM sales WHERE sale_type='return'`); n != 1 {
		t.Errorf("return sales = %d, want 1", n)
	}
}

// AC3: two refunds that fit together both succeed, allocated in order.
func TestRefundConcurrency_TwoRefundsThatFitBothSucceedInOrder(t *testing.T) {
	rig := newDemoRig(t)
	if _, err := rig.dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)
	gate := rig.gateRefundHook(t)

	a := rig.postRefundAsync(context.Background(), receipt, 1)
	recvWithin(t, gate.entered, "A to reach the provider")
	b := rig.postRefundAsync(context.Background(), receipt, 1)
	recvEither(t, waits, "B to park behind A", gate.entered, "B to reach the provider")
	gate.open()
	ra := recvWithin(t, a, "refund A to finish")
	rb := recvWithin(t, b, "refund B to finish")
	if ra.Code != 200 || rb.Code != 200 {
		t.Fatalf("codes = %d, %d; want both 200 (%s | %s)", ra.Code, rb.Code, ra.Body.String(), rb.Body.String())
	}
	got := rig.refunds()
	if len(got) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(got))
	}
	if a := legAmounts(t, got[0]); fmt.Sprint(a) != "[120]" {
		t.Errorf("first legs = %v, want [120]", a)
	}
	if a := legAmounts(t, got[1]); fmt.Sprint(a) != "[30 90]" {
		t.Errorf("second legs = %v, want [30 90]", a)
	}
}

// AC4: a blocked provider call on one sale never delays another sale.
func TestRefundConcurrency_OtherSaleNotDelayed(t *testing.T) {
	rig := newDemoRig(t)
	rig.sell(t, `[{"method":"demopay","amount":120}]`)
	rig.sell(t, `[{"method":"cash","amount":120}]`)
	gate := rig.gateRefundHook(t)

	a := rig.postRefundAsync(context.Background(), rig.receiptNo(t, 1), 1)
	recvWithin(t, gate.entered, "sale 1 refund to reach the provider")
	req := httptest.NewRequest(http.MethodPost, "/api/refund",
		strings.NewReader("receipt="+rig.receiptNo(t, 2)+"&qty_0=1&method=cash"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req) // would hang here if sales shared a lock
	if rec.Code != 200 {
		t.Fatalf("other sale refund = %d: %s", rec.Code, rec.Body.String())
	}
	gate.open()
	if ra := recvWithin(t, a, "first refund to finish"); ra.Code != 200 {
		t.Fatalf("first refund = %d: %s", ra.Code, ra.Body.String())
	}
}

// AC5: the lock is released on a decline and on bad input.
func TestRefundConcurrency_LockReleasedAfterDeclineAndBadInput(t *testing.T) {
	rig := newDemoRig(t)
	rig.sell(t, `[{"method":"demopay","amount":80},{"method":"demopay","amount":40}]`)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)

	rig.setRefundError(&plugins.HandlerExitError{Code: 2, Stdout: []byte(`{"decline_code":"card_declined"}`)})
	if rec := rig.postRefundQty(t, receipt, 1); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("decline = %d: %s", rec.Code, rec.Body.String())
	}
	refundErrOverrides.Delete(rig.dp.Db)
	if rec := rig.postRefundQty(t, receipt, 0); rec.Code != http.StatusBadRequest {
		t.Fatalf("nothing selected = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := rig.postRefundQty(t, receipt, 1); rec.Code != 200 {
		t.Fatalf("refund after decline = %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-waits:
		t.Fatal("a refund had to wait for a lock a finished request should have released")
	default:
	}
	if n := refundSaleLocks.size(); n != 0 {
		t.Fatalf("lock map size = %d, want 0", n)
	}
}

// A request whose client has gone while waiting records nothing.
func TestRefundConcurrency_CancelledWaiterRecordsNothing(t *testing.T) {
	rig := newDemoRig(t)
	rig.sell(t, `[{"method":"demopay","amount":80},{"method":"demopay","amount":40}]`)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)

	hold, err := refundSaleLocks.lock(context.Background(), saleID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hold) // idempotent; frees the lock if a guard fatals first
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := rig.postRefundAsync(ctx, receipt, 1)
	recvWithin(t, waits, "waiter to park")
	cancel()
	rec := recvWithin(t, res, "cancelled refund to return")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancelled waiter = %d: %s", rec.Code, rec.Body.String())
	}
	hold()
	if n := rig.count(t, `SELECT COUNT(*) FROM sales WHERE sale_type='return'`); n != 0 {
		t.Errorf("return sales = %d, want 0", n)
	}
	if n := len(rig.refunds()); n != 0 {
		t.Errorf("provider called %d times", n)
	}
	if rec := rig.postRefundQty(t, receipt, 1); rec.Code != 200 {
		t.Fatalf("next refund = %d: %s", rec.Code, rec.Body.String())
	}
}

// AC1 (per-leg cap path): sale paid 100 cash + 70 + 70 demopay. Two
// concurrent one-unit refunds must not both pass the per-method cap; the
// loser is refused with method_exhausted and the provider is called once.
func TestRefundConcurrency_LegCapRaceRefusesLoserAsMethodExhausted(t *testing.T) {
	rig := newDemoRig(t)
	if _, err := rig.dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rig.sell(t, `[{"method":"cash","amount":100},{"method":"demopay","amount":70},{"method":"demopay","amount":70}]`)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)
	gate := rig.gateRefundHook(t)

	a := rig.postRefundAsync(context.Background(), receipt, 1)
	recvWithin(t, gate.entered, "A to reach the provider")
	b := rig.postRefundAsync(context.Background(), receipt, 1)
	recvEither(t, waits, "B to park behind A", gate.entered, "B to reach the provider")
	gate.open()
	ra := recvWithin(t, a, "refund A to finish")
	rb := recvWithin(t, b, "refund B to finish")

	if !((ra.Code == 200 && rb.Code == 409) || (ra.Code == 409 && rb.Code == 200)) {
		t.Fatalf("codes = %d, %d; want one 200 and one 409 (%s | %s)", ra.Code, rb.Code, ra.Body.String(), rb.Body.String())
	}
	if body := ra.Body.String() + rb.Body.String(); !strings.Contains(body, refundErrorText(t, "refund.error.method_exhausted")) {
		t.Errorf("409 is not method_exhausted: %s", body)
	}
	if n := rig.count(t, `SELECT COUNT(*) FROM sales WHERE sale_type='return'`); n != 1 {
		t.Errorf("return sales = %d, want 1", n)
	}
	if n := len(rig.refunds()); n != 1 {
		t.Errorf("provider refund calls = %d, want 1", n)
	}
}
