package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ut-docs#6909 (R6): a tap check/resolve already asking the broker about a leg
// must not run a second time beside itself. The 2 s throttle only covers
// /pay/tap/check; a retry after 2 s or a resolve beside a check ran concurrently.

// holdBroker parks the broker's first answer until the returned release runs
// and signals on entered when a request is parked.
func holdBroker(e *tapEnv) (entered chan struct{}, release func()) {
	entered = make(chan struct{}, 8)
	gate := make(chan struct{})
	first := make(chan struct{}, 1)
	first <- struct{}{}
	e.broker.setOnCall(func(string) {
		select {
		case <-first:
			entered <- struct{}{}
			<-gate
		default:
		}
	})
	closed := false
	return entered, func() {
		if !closed {
			closed = true
			close(gate)
		}
	}
}

func waitEntered(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the first request never reached the broker")
	}
}

func TestTapCheck_ConcurrentSameLegOneBrokerAsk(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	before := e.broker.count("session_status")
	entered, release := holdBroker(e)
	t.Cleanup(release)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	}()
	waitEntered(t, entered)

	rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("second check = %d: %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "tap.pending.still")
	if got := e.broker.count("session_status") - before; got != 1 {
		t.Fatalf("broker asked %d times while a check runs, want 1", got)
	}
	release()
	<-done
}

func TestTapResolve_WhileCheckRunningIs409(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	entered, release := holdBroker(e)
	t.Cleanup(release)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	}()
	waitEntered(t, entered)
	calls := len(e.broker.log())

	rec := e.resolve(t, e.manager, url.Values{"decision": {"not_charged"}, "reason": {"card_not_charged"}, "override_pin": {dataAPITestManagerPIN}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("resolve beside a running check = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "tap.resolve.in_progress")
	if n := len(e.broker.log()); n != calls {
		t.Fatalf("the refused resolve asked the broker (%d -> %d calls)", calls, n)
	}
	if e.dp.Engine.TenderLock() == "" {
		t.Fatal("the refused resolve unlocked the tender")
	}
	release()
	<-done
}

func TestAttentionResolve_WhileResolveRunningIs409(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	entered, release := holdBroker(e)
	t.Cleanup(release)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- e.resolveAttn(t, e.manager, "orphan-1", "not_charged", "", dataAPITestManagerPIN)
	}()
	waitEntered(t, entered)
	calls := len(e.broker.log())

	rec := e.resolveAttn(t, e.manager, "orphan-1", "not_charged", "", dataAPITestManagerPIN)
	if rec.Code != http.StatusConflict || !strings.Contains(bodyText(rec), "in_progress") {
		t.Fatalf("second attention resolve = %d, want 409 in_progress: %s", rec.Code, rec.Body.String())
	}
	if n := len(e.broker.log()); n != calls {
		t.Fatalf("the refused resolve asked the broker (%d -> %d calls)", calls, n)
	}
	release()
	<-done
}

// ut-docs#6909 (M2): while a tender request runs on the engine (a retried tap
// tender re-authorizing the locked leg), a Check or a manager resolve must not
// run beside it: no broker ask, no reversal, no attempt write, no unlock.
func TestTapCheck_RefusedWhileTenderInFlight(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	if !e.dp.Engine.TryBeginTender() {
		t.Fatal("could not claim the tender guard")
	}
	t.Cleanup(e.dp.Engine.EndTender)
	calls := len(e.broker.log())
	before := e.attempt(t)

	rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}, "cancel": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("check during a tender = %d: %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "tap.pending.still")
	if n := len(e.broker.log()); n != calls {
		t.Fatalf("check asked the broker beside a running tender (%d -> %d calls)", calls, n)
	}
	if after := e.attempt(t); after.State != before.State || after.Reason != before.Reason {
		t.Fatalf("check wrote the attempt row beside a tender: %+v -> %+v", before, after)
	}
	if e.dp.Engine.TenderLock() == "" {
		t.Fatal("check unlocked the tender beside a running tender")
	}
}

func TestTapResolve_RefusedWhileTenderInFlight(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	if !e.dp.Engine.TryBeginTender() {
		t.Fatal("could not claim the tender guard")
	}
	t.Cleanup(e.dp.Engine.EndTender)
	calls := len(e.broker.log())

	rec := e.resolve(t, e.manager, url.Values{"decision": {"not_charged"}, "reason": {"card_not_charged"}, "override_pin": {dataAPITestManagerPIN}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("resolve during a tender = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "tap.resolve.in_progress")
	if n := len(e.broker.log()); n != calls {
		t.Fatalf("resolve asked the broker beside a running tender (%d -> %d calls)", calls, n)
	}
	if e.dp.Engine.TenderLock() == "" {
		t.Fatal("resolve unlocked the tender beside a running tender")
	}
}
