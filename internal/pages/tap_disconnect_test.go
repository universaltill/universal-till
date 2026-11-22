package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#6908 (rule of #6805/#6784): once a tap decision is made, its
// compensations (broker cancel, reversal of captured legs, attempt write,
// audit, native release) run detached from the request, so a client that
// disconnects mid-call neither loses the reversal nor corrupts the facts.

const approvedDemoLeg = `{"provider":"demopay","outcome":"approved","transaction_ref":"ch_TESTREF9876"}`

// splitPending locks the basket on [demopay 60 captured, tapstripe 60
// pending]; it returns the demopay refund log.
func (e *tapEnv) splitPending(t *testing.T) (refunds func() []map[string]any) {
	t.Helper()
	refunds, _ = seedCardPluginWithAnswerFunc(t, e.dp, nil, func(int) string { return approvedDemoLeg })
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(tenderBody(leg("demopay", 60, ""), leg("tapstripe", 60, ""))))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, e.cashier))
	if e.sales(t) != 0 || e.dp.Engine.TenderLock() == "" {
		t.Fatalf("setup: split tender must lock (sales=%d lock=%q): %s", e.sales(t), e.dp.Engine.TenderLock(), rec.Body.String())
	}
	if len(e.dp.Engine.PendingTender().Captured) != 1 {
		t.Fatalf("setup: the demopay leg must be captured: %+v", e.dp.Engine.PendingTender())
	}
	return refunds
}

func failedReversals() int {
	n := 0
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "reversing captured payment FAILED") {
			n++
		}
	}
	return n
}

func (e *tapEnv) auditRows(t *testing.T) int {
	return countRows(t, e.dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'tap_resolve'`)
}

func TestTapResolve_ClientDisconnectDuringReversalStillReverses(t *testing.T) {
	pin := url.Values{"override_pin": {dataAPITestManagerPIN}}
	for _, tc := range []struct {
		name      string
		broker    string // "" = broker down
		path      string
		form      url.Values
		user      func(*tapEnv) auth.User
		wantState string
		wantAudit int
	}{
		{"cashier check", piStatus("requires_payment_method", 0), "/pay/tap/check", nil,
			func(e *tapEnv) auth.User { return e.cashier }, data.AttemptCancelled, 0},
		{"manager not_charged", piStatus("requires_payment_method", 0), "/manager/pay/tap/resolve",
			url.Values{"decision": {"not_charged"}, "override_pin": pin["override_pin"]},
			func(e *tapEnv) auth.User { return e.manager }, data.AttemptResolvedNotCharged, 1},
		{"manager override", "", "/manager/pay/tap/resolve",
			url.Values{"decision": {"not_charged"}, "reason": {"customer_left"}, "override_pin": pin["override_pin"]},
			func(e *tapEnv) auth.User { return e.manager }, data.AttemptResolvedNotCharged, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tapRig(t)
			refunds := e.splitPending(t)
			e.broker.setStatus(tc.broker)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			refundHookGates.Store(e.dp.Db, func() { cancel() })
			t.Cleanup(func() { refundHookGates.Delete(e.dp.Db) })
			logging.ResetRecent()
			rec := e.doCtx(t, ctx, tc.user(e), http.MethodPost, tc.path, tc.form)
			if n := len(refunds()); n != 1 {
				t.Fatalf("refunds = %d, want 1 (%d %s)", n, rec.Code, rec.Body.String())
			}
			if n := failedReversals(); n != 0 {
				t.Fatalf("a dropped connection failed the reversal %d time(s)", n)
			}
			if a := e.attempt(t); a.State != tc.wantState {
				t.Fatalf("attempt state = %s, want %s", a.State, tc.wantState)
			}
			if lock := e.dp.Engine.TenderLock(); lock != "" {
				t.Fatalf("lock = %q, want cleared", lock)
			}
			if n := e.auditRows(t); n != tc.wantAudit {
				t.Fatalf("tap_resolve audit rows = %d, want %d", n, tc.wantAudit)
			}
		})
	}
}

func TestTapCancelThenAssess_ClientDisconnectDuringSessionCancelStillSettles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attention bool
		path      string
		user      func(*tapEnv) auth.User
		form      url.Values
		wantState string
		wantAudit int
	}{
		{"cashier check", false, "/pay/tap/check", func(e *tapEnv) auth.User { return e.cashier }, nil, data.AttemptCancelled, 0},
		{"manager not_charged", false, "/manager/pay/tap/resolve", func(e *tapEnv) auth.User { return e.manager },
			url.Values{"decision": {"not_charged"}, "override_pin": {dataAPITestManagerPIN}}, data.AttemptResolvedNotCharged, 1},
		{"attention not_charged", true, "/api/pay/attempts/attention/resolve", func(e *tapEnv) auth.User { return e.manager },
			url.Values{"key": {"orphan-1"}, "decision": {"not_charged"}, "override_pin": {dataAPITestManagerPIN}}, data.AttemptResolvedNotCharged, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tapRig(t)
			key := ""
			if tc.attention {
				key = "orphan-1"
				e.orphan(t, key, "stripe")
			} else {
				e.pending(t)
			}
			e.broker.setStatus(piStatus("requires_payment_method", 0))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.broker.setOnCall(func(p string) {
				if strings.HasSuffix(p, "/session_cancel") {
					cancel()
				}
			})
			rec := e.doCtx(t, ctx, tc.user(e), http.MethodPost, tc.path, tc.form)
			if rec.Code != http.StatusOK {
				t.Fatalf("= %d %s", rec.Code, rec.Body.String())
			}
			if n := e.broker.count("session_cancel"); n != 1 {
				t.Fatalf("session_cancel calls = %d, want 1", n)
			}
			var a data.PaymentAttempt
			if tc.attention {
				a = e.attemptByKey(t, key)
			} else {
				a = e.attempt(t)
			}
			if a.State != tc.wantState {
				t.Fatalf("attempt state = %s, want %s", a.State, tc.wantState)
			}
			if n := e.auditRows(t); n != tc.wantAudit {
				t.Fatalf("tap_resolve audit rows = %d, want %d", n, tc.wantAudit)
			}
		})
	}
}

type blockingResolver struct{ release chan struct{} }

func (b *blockingResolver) Resolve(string, string) string { <-b.release; return `{}` }

// A detached ctx must not let a wedged native shell hold the handler forever.
func TestTellNativeResolver_BoundedOnDetachedCtx(t *testing.T) {
	e := tapRig(t)
	br := &blockingResolver{release: make(chan struct{})}
	devicepay.SetResolver(br)
	t.Cleanup(func() { close(br.release); devicepay.SetResolver(nil) })
	old := nativeResolveTimeout
	nativeResolveTimeout = 50 * time.Millisecond
	t.Cleanup(func() { nativeResolveTimeout = old })

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/x", nil), e.manager)
	f := tapFacts{HaveRow: true, Attempt: data.PaymentAttempt{Provider: devicepay.ProviderStripe}}
	f.Leg.RequestID = "k"
	done := make(chan struct{})
	go func() {
		tellNativeResolver(context.WithoutCancel(req.Context()), req, f, devicepay.OutcomeNotCharged)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("tellNativeResolver is unbounded on a detached ctx")
	}
}
