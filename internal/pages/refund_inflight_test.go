package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7045: a second submit of the same refund attempt while the first is
// still running is refused at once (409), not parked behind the sale lock and
// then refunded a second time.

// secondSubmit posts B and returns its response, failing fast if B parks
// behind the sale lock (the pre-fix behaviour) instead of being refused.
func secondSubmit(t *testing.T, b <-chan *httptest.ResponseRecorder, waits <-chan string) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rb := <-b:
		return rb
	case <-waits:
		t.Fatal("second submit parked behind the sale lock; want an immediate 409")
	case <-time.After(testWaitTimeout):
		t.Fatal("second submit neither answered nor parked")
	}
	return nil
}

func sellThree(t *testing.T, rig *refCardRig) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := rig.dp.Engine.Scan("ABC"); err != nil {
			t.Fatal(err)
		}
	}
	rig.sell(t, `[{"method":"demopay","amount":360}]`)
}

func TestRefundInFlight_SameAttemptPartialRefundAskedOnce(t *testing.T) {
	rig := newDemoRig(t)
	sellThree(t, rig)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)
	gate := rig.gateRefundHook(t)

	a := rig.postRefundAttemptAsync(context.Background(), receipt, 1, "att-1")
	recvWithin(t, gate.entered, "A to reach the provider")
	b := rig.postRefundAttemptAsync(context.Background(), receipt, 1, "att-1")
	rb := secondSubmit(t, b, waits)
	if rb.Code != http.StatusConflict || !strings.Contains(bodyText(rb), enText(t, "refund.error.in_progress")) {
		t.Fatalf("B = %d %s, want 409 in_progress", rb.Code, rb.Body.String())
	}
	gate.open()
	if ra := recvWithin(t, a, "refund A to finish"); ra.Code != 200 {
		t.Fatalf("A = %d %s", ra.Code, ra.Body.String())
	}
	if n := len(rig.refunds()); n != 1 {
		t.Fatalf("provider refund calls = %d, want 1", n)
	}
	if n := rig.count(t, `SELECT COUNT(*) FROM sales WHERE sale_type='return'`); n != 1 {
		t.Errorf("return sales = %d, want 1", n)
	}
	if l := rig.refundLegs(t, data.LegSettled); len(l) != 1 {
		t.Errorf("refund legs = %+v, want 1", l)
	}
}

func TestRefundInFlight_SameAttemptFullRefundGetsLocalizedConflict(t *testing.T) {
	rig := newDemoRig(t)
	rig.sell(t, `[{"method":"demopay","amount":120}]`)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)
	gate := rig.gateRefundHook(t)

	a := rig.postRefundAttemptAsync(context.Background(), receipt, 1, "att-1")
	recvWithin(t, gate.entered, "A to reach the provider")
	b := rig.postRefundAttemptAsync(context.Background(), receipt, 1, "att-1")
	rb := secondSubmit(t, b, waits)
	if rb.Code != http.StatusConflict || !strings.Contains(bodyText(rb), enText(t, "refund.error.in_progress")) {
		t.Fatalf("B = %d %s, want 409 in_progress", rb.Code, rb.Body.String())
	}
	if strings.Contains(rb.Body.String(), "left to refund") {
		t.Errorf("B carries the raw quantity error: %s", rb.Body.String())
	}
	gate.open()
	recvWithin(t, a, "refund A to finish")
	if n := len(rig.refunds()); n != 1 {
		t.Fatalf("provider refund calls = %d, want 1", n)
	}
}

func TestRefundInFlight_DifferentAttemptsBothSucceed(t *testing.T) {
	rig := newDemoRig(t)
	sellThree(t, rig)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	waits := watchLockWaits(t, saleID)
	gate := rig.gateRefundHook(t)

	a := rig.postRefundAttemptAsync(context.Background(), receipt, 1, "att-a")
	recvWithin(t, gate.entered, "A to reach the provider")
	b := rig.postRefundAttemptAsync(context.Background(), receipt, 1, "att-b")
	recvWithin(t, waits, "B to park behind A")
	gate.open()
	ra := recvWithin(t, a, "refund A to finish")
	rb := recvWithin(t, b, "refund B to finish")
	if ra.Code != 200 || rb.Code != 200 {
		t.Fatalf("codes = %d, %d; want both 200 (%s | %s)", ra.Code, rb.Code, ra.Body.String(), rb.Body.String())
	}
	if n := len(rig.refunds()); n != 2 {
		t.Fatalf("provider calls = %d, want 2", n)
	}
}

func TestRefundInFlight_ReleasedAfterDeclineAndBadInput(t *testing.T) {
	rig := newDemoRig(t)
	sellThree(t, rig)
	receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
	post := func(qty int) *httptest.ResponseRecorder {
		return recvWithin(t, rig.postRefundAttemptAsync(context.Background(), receipt, qty, "att-d"), "refund")
	}

	rig.setRefundError(&plugins.HandlerExitError{Code: 2, Stdout: []byte(`{"decline_code":"card_declined"}`)})
	if rec := post(1); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("decline = %d: %s", rec.Code, rec.Body.String())
	}
	refundErrOverrides.Delete(rig.dp.Db)
	if rec := post(0); rec.Code != http.StatusBadRequest {
		t.Fatalf("nothing selected = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(1); rec.Code != 200 {
		t.Fatalf("retry after decline = %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 2 {
		t.Fatalf("provider calls = %d, want 2", n)
	}
	settled := rig.refundLegs(t, data.LegSettled)
	if len(settled) != 1 || settled[0].Ask != 2 {
		t.Fatalf("settled = %+v, want one with ask 2", settled)
	}
	key := saleID + "\x00att-d"
	if !refundAttemptInFlight.acquire(key) {
		t.Fatal("attempt key still held after every request ended")
	}
	refundAttemptInFlight.release(key)
}

const failDeclinedOutcome = `CREATE TRIGGER fail_outcome BEFORE INSERT ON payment_leg_journal WHEN NEW.state = 'declined' BEGIN SELECT RAISE(ABORT,'forced'); END`

// leaveAsked declines a refund while the declined write is forced to fail, so
// the leg's newest row stays asked with nothing running.
func leaveAsked(t *testing.T, rig *refCardRig, receipt, attempt string) {
	t.Helper()
	rig.exec(t, failDeclinedOutcome)
	rig.setRefundError(&plugins.HandlerExitError{Code: 2, Stdout: []byte(`{"decline_code":"card_declined"}`)})
	rig.postRefund(t, receipt, attempt)
	rig.exec(t, `DROP TRIGGER fail_outcome`)
	refundErrOverrides.Delete(rig.dp.Db)
	if l := rig.refundLegs(t, data.LegAsked); len(l) != 1 {
		t.Fatalf("asked refund legs = %+v, want 1", l)
	}
}

func TestRefundInFlight_StaleAskedInProcessReasks(t *testing.T) {
	rig := newDemoRig(t)
	receipt := rig.sellOne(t)
	leaveAsked(t, rig, receipt, "att-s")
	if rec := rig.postRefund(t, receipt, "att-s"); rec.Code != 200 {
		t.Fatalf("resubmit = %d %s", rec.Code, rec.Body.String())
	}
	// Re-asked under the same key: the provider is called again (its own
	// idempotency key dedupes) and the one leg settles. The ask counter only
	// rises after a non-asked state (AskLeg), so it stays 1 here.
	if n := len(rig.refunds()); n != 2 {
		t.Fatalf("provider calls = %d, want 2 (declined first, re-asked)", n)
	}
	if settled := rig.refundLegs(t, data.LegSettled); len(settled) != 1 {
		t.Fatalf("settled = %+v, want one leg", settled)
	}
}

func TestRefundInFlight_AskedBeforeRestartRefusedAfterReconcile(t *testing.T) {
	rig := newDemoRig(t)
	receipt := rig.sellOne(t)
	leaveAsked(t, rig, receipt, "att-s")
	before := len(rig.refunds())
	ReconcilePaymentLegs(context.Background(), rig.dp, time.Now().Add(time.Minute))
	rec := rig.postRefund(t, receipt, "att-s")
	if rec.Code != http.StatusConflict || !strings.Contains(bodyText(rec), enText(t, "refund.error.refunded_not_recorded")) {
		t.Fatalf("resubmit = %d %s, want 409 refunded_not_recorded", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != before {
		t.Fatalf("provider calls %d -> %d, want unchanged", before, n)
	}
	if n := rig.count(t, `SELECT COUNT(*) FROM sales WHERE sale_type='return'`); n != 0 {
		t.Fatalf("return sales = %d, want 0", n)
	}
}

func TestRefundInFlight_NoAttemptIDNeverGuarded(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129)} {
		rig := newDemoRig(t)
		sellThree(t, rig)
		receipt, saleID := rig.receiptNo(t, 1), rig.saleID(t, 1)
		waits := watchLockWaits(t, saleID)
		gate := rig.gateRefundHook(t)

		a := rig.postRefundAttemptAsync(context.Background(), receipt, 1, id)
		recvWithin(t, gate.entered, "A to reach the provider")
		b := rig.postRefundAttemptAsync(context.Background(), receipt, 1, id)
		recvWithin(t, waits, "B to park behind A (no guard without a usable attempt id)")
		gate.open()
		ra := recvWithin(t, a, "A")
		rb := recvWithin(t, b, "B")
		if strings.Contains(bodyText(rb), enText(t, "refund.error.in_progress")) || ra.Code != 200 {
			t.Fatalf("id %q: A=%d B=%d %s", id, ra.Code, rb.Code, rb.Body.String())
		}
	}
}

func TestRefundAttemptIDFromForm(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""}, {"  x  ", "x"}, {strings.Repeat("a", 128), strings.Repeat("a", 128)},
		{strings.Repeat("a", 129), ""}, {reversalRequestIDPrefix + "abc", ""},
	} {
		if got := refundAttemptIDFromForm(url.Values{"refund_attempt_id": {c.in}}); got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
}
