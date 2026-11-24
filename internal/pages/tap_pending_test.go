package pages

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paybroker"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6455 K1b: core's payment gate binds a tap dispatch, confirms with
// the broker (never the plugin's say-so), and a pending payment is settled by
// "Check again" (cashier or manager) or a manager's PIN-gated resolve.

// enText returns the English copy for key; a missing key fails the test (an
// empty string would make every Contains check pass).
func enText(t *testing.T, key string) string {
	t.Helper()
	_, file, _, _ := runtimeCallerFile()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "web", "locales", "en.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	v, ok := m[key]
	if !ok || v == "" {
		t.Fatalf("web/locales/en.json has no %q", key)
	}
	return v
}

// fakeBroker is the cloud payment broker: it answers session_status from
// .status, records every request path+body and can be taken down.
type fakeBroker struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status string // JSON answered to session_status ("" = provider unavailable)
	calls  []string
	// seq, when set, answers successive session_status calls in order (the
	// last answer repeats); cancelCode/cancelBody override the session_cancel
	// answer (default 200 canceled).
	seq        []string
	cancelCode int
	cancelBody string
	// onCall, when set, runs for every request before the answer is written
	// (ut-docs#6908: a test cancels the request context from inside a call).
	onCall func(path string)
	// roleRefused answers every call 403 device_role_unrecorded.
	roleRefused bool
	// roleCode overrides the refusal code (default device_role_unrecorded).
	roleCode string
	// errStatus/errCode, when set, answer every call with that HTTP status and
	// error code (checked before roleRefused; ut-docs#6981).
	errStatus int
	errCode   string
}

func (b *fakeBroker) setError(status int, code string) {
	b.mu.Lock()
	b.errStatus, b.errCode = status, code
	b.mu.Unlock()
}

func (b *fakeBroker) setRoleCode(c string) { b.mu.Lock(); b.roleCode = c; b.mu.Unlock() }

func (b *fakeBroker) setRoleRefused(v bool) { b.mu.Lock(); b.roleRefused = v; b.mu.Unlock() }

func (b *fakeBroker) setOnCall(f func(path string)) { b.mu.Lock(); b.onCall = f; b.mu.Unlock() }

func (b *fakeBroker) setSeq(s ...string) { b.mu.Lock(); b.seq = s; b.mu.Unlock() }
func (b *fakeBroker) setCancel(code int, body string) {
	b.mu.Lock()
	b.cancelCode, b.cancelBody = code, body
	b.mu.Unlock()
}

func (b *fakeBroker) setStatus(s string) { b.mu.Lock(); b.status = s; b.mu.Unlock() }
func (b *fakeBroker) log() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}
func (b *fakeBroker) count(op string) int {
	n := 0
	for _, c := range b.log() {
		if strings.Contains(c, "/"+op+" ") {
			n++
		}
	}
	return n
}

func newFakeBroker(t *testing.T) *fakeBroker {
	t.Helper()
	b := &fakeBroker{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.calls = append(b.calls, r.URL.Path+" "+string(body))
		st := b.status
		if len(b.seq) > 0 && !strings.HasSuffix(r.URL.Path, "/session_cancel") {
			st = b.seq[0]
			if len(b.seq) > 1 {
				b.seq = b.seq[1:]
			}
		}
		cc, cb := b.cancelCode, b.cancelBody
		hook := b.onCall
		refused, rcode := b.roleRefused, b.roleCode
		es, ec := b.errStatus, b.errCode
		b.mu.Unlock()
		if es != 0 {
			w.WriteHeader(es)
			_, _ = io.WriteString(w, `{"error":{"code":"`+ec+`"}}`)
			return
		}
		if refused {
			if rcode == "" {
				rcode = "device_role_unrecorded"
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"`+rcode+`"}}`)
			return
		}
		if hook != nil {
			hook(r.URL.Path)
		}
		if strings.HasSuffix(r.URL.Path, "/session_cancel") {
			if cc != 0 {
				w.WriteHeader(cc)
				_, _ = io.WriteString(w, cb)
				return
			}
			_, _ = io.WriteString(w, `{"status":"canceled"}`)
			return
		}
		if st == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"provider_unavailable"}}`)
			return
		}
		_, _ = io.WriteString(w, st)
	}))
	paybroker.SetTargetResolver(func(context.Context) (paybroker.Target, bool) {
		return paybroker.Target{BaseURL: b.srv.URL, Bearer: "cred"}, true
	})
	t.Cleanup(func() { b.srv.Close(); paybroker.SetTargetResolver(nil) })
	return b
}

func piStatus(status string, received int64) string {
	return `{"payment_intent_id":"pi_k1b","status":"` + status + `","amount_received_minor":` +
		strconv.FormatInt(received, 10) + `,"currency":"gbp","card":{"brand":"visa","last4":"4242"}}`
}

type tapEnv struct {
	mux      *http.ServeMux
	dp       *common.Deps
	broker   *fakeBroker
	plugin   *readerPlugin
	cashier  auth.User
	manager  auth.User
	mu       sync.Mutex
	bindings []paybroker.Binding
	noRow    bool // the plugin stub creates no attempt row (a payment that never started)
}

func (e *tapEnv) lastBinding(t *testing.T) paybroker.Binding {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.bindings) == 0 {
		t.Fatal("the plugin's authorize dispatch carried no payment binding")
	}
	return e.bindings[len(e.bindings)-1]
}

// tapRig: a till with a tap payment plugin ("tapstripe", entry config tap:true),
// real auth (a cashier and a manager with PIN) and a fake broker.
func tapRig(t *testing.T) *tapEnv { return tapRigWith(t, false) }

// tapRigWith is tapRig; withReadersHook makes the method a managed-reader one
// (its plugin declares payment.<m>.readers), so a tender can name a reader.
func tapRigWith(t *testing.T, withReadersHook bool) *tapEnv {
	t.Helper()
	mux, dp := authRig(t)
	registerTapPay(mux, dp)
	registerTapAttention(mux, dp)
	// ut-docs#6458: a tap entry is offered only on a ready device (or with a
	// paired phone reader); these tests are about what happens after the offer.
	devicepay.SetBridge(&fakeTapBridge{caps: capsReady})
	t.Cleanup(func() { devicepay.SetBridge(nil) })
	tapCheckInterval = 0
	t.Cleanup(func() { tapCheckInterval = 2 * time.Second })
	e := &tapEnv{mux: mux, dp: dp, broker: newFakeBroker(t)}
	e.broker.setStatus(piStatus("processing", 0))
	e.plugin = seedReaderPlugin(t, dp, "p.tap", "tapstripe", withReadersHook)
	if _, err := dp.Db.Exec(`UPDATE plugin_entries SET config_json = '{"tap":true,"provider":"stripe"}' WHERE plugin_id = 'p.tap'`); err != nil {
		t.Fatal(err)
	}
	e.plugin.onAuth = func(ctx context.Context, ev plugins.Event) {
		b, ok := paybroker.BindingFrom(ctx)
		if !ok {
			return
		}
		e.mu.Lock()
		e.bindings = append(e.bindings, b)
		e.mu.Unlock()
		// What tap_to_pay_request does before the tap (K1a).
		if e.noRow {
			return
		}
		_, _ = data.NewPaymentAttemptRepo(dp.Db).Begin(ctx, data.PaymentAttempt{
			Key: b.AttemptKey, Provider: b.Provider, AmountMinor: b.AmountMinor, Currency: b.Currency, Deadline: time.Now().Add(2 * time.Minute),
		})
	}
	repo := data.NewAuthRepo(dp.Db)
	cid, err := repo.CreateUser(context.Background(), "k1b-cashier", "Cashier", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	e.cashier = auth.User{ID: cid, Role: "cashier"}
	e.manager = auth.User{ID: seedDataAPIManager(t, dp.Db), Role: "manager"}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *tapEnv) do(t *testing.T, u auth.User, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return e.doCtx(t, context.Background(), u, method, path, form)
}

// doCtx is do on a caller-owned request context, so a test can cancel it
// mid-request like a client that disconnects (ut-docs#6908).
func (e *tapEnv) doCtx(t *testing.T, ctx context.Context, u auth.User, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body).WithContext(ctx)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, u))
	return rec
}

// tender pays 120 on the tap method as the cashier.
func (e *tapEnv) tender(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(tenderBody(leg("tapstripe", 120, ""))))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, e.cashier))
	return rec
}

func (e *tapEnv) sales(t *testing.T) int {
	return countRows(t, e.dp, "SELECT COUNT(*) FROM sales")
}

func (e *tapEnv) attempt(t *testing.T) data.PaymentAttempt {
	t.Helper()
	a, ok, err := data.NewPaymentAttemptRepo(e.dp.Db).Get(context.Background(), e.lastBinding(t).AttemptKey)
	if err != nil || !ok {
		t.Fatalf("attempt row: %v %v", ok, err)
	}
	return a
}

// pending runs a tap tender whose plugin approves but the broker says
// processing: the till must hold the basket on a pending tender.
func (e *tapEnv) pending(t *testing.T) {
	t.Helper()
	rec := e.tender(t)
	if e.sales(t) != 0 || e.dp.Engine.TenderLock() == "" {
		t.Fatalf("a payment the broker has not confirmed must lock the tender (sales=%d lock=%q): %s", e.sales(t), e.dp.Engine.TenderLock(), rec.Body.String())
	}
}

func bodyText(rec *httptest.ResponseRecorder) string { return html.UnescapeString(rec.Body.String()) }

func (e *tapEnv) wantText(t *testing.T, rec *httptest.ResponseRecorder, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !strings.Contains(bodyText(rec), enText(t, k)) {
			t.Errorf("response lacks %s (%q): %d %s", k, enText(t, k), rec.Code, rec.Body.String())
		}
	}
}

// --- the gate ---

func TestTapGate_BindsDispatchAndConfirmsWithBroker(t *testing.T) {
	e := tapRig(t)
	e.broker.setStatus(piStatus("succeeded", 120))
	rec := e.tender(t)
	if e.sales(t) != 1 {
		t.Fatalf("a confirmed tap payment completes the sale: %d %s", rec.Code, rec.Body.String())
	}
	b := e.lastBinding(t)
	calls := e.plugin.authCalls()
	if b.Kind != paybroker.BindAuthorize || b.AmountMinor != 120 || b.Provider != "stripe" || !strings.EqualFold(b.Currency, "GBP") || b.AttemptKey != calls[0].ID {
		t.Fatalf("binding must come from the gate (amount, currency, provider, request id): %+v want key %s", b, calls[0].ID)
	}
	if a := e.attempt(t); a.State != data.AttemptApproved || a.PIID != "pi_k1b" || a.CardBrand != "visa" || a.Last4 != "4242" {
		t.Fatalf("attempt must be approved from the broker's answer: %+v", a)
	}
	if e.broker.count("session_status") == 0 {
		t.Fatal("core must ask the broker itself")
	}
}

// ut-docs#6805: a client that leaves while the tap authorize is in flight must
// not turn a paid tap into a pending one (pos.ConfirmTap returns
// PendingContextCancelled on a done ctx) -- the tap steps run detached.
func TestTapGate_ClientDisconnectDuringTapStillCompletesSale(t *testing.T) {
	e := tapRig(t)
	e.broker.setStatus(piStatus("succeeded", 120))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orig := e.plugin.onAuth
	e.plugin.onAuth = func(c context.Context, ev plugins.Event) {
		orig(c, ev) // begins the attempt row, as the real tap plugin does
		cancel()    // the client goes away while the reader waits
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(tenderBody(leg("tapstripe", 120, "")))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, e.cashier))
	if e.sales(t) != 1 {
		t.Fatalf("a paid tap must complete the sale after a disconnect: %d %s", rec.Code, rec.Body.String())
	}
	if a := e.attempt(t); a.State != data.AttemptApproved {
		t.Fatalf("attempt must be settled approved, got %+v", a)
	}
	if lock := e.dp.Engine.TenderLock(); lock != "" {
		t.Fatalf("a completed sale leaves no tender lock, got %q", lock)
	}
}

func TestTapGate_PluginApprovedBrokerNotSucceededLocksTender(t *testing.T) {
	for _, tc := range []struct {
		name, reply, reason string
	}{
		{"processing", piStatus("processing", 0), "processing"},
		{"amount mismatch", piStatus("succeeded", 119), "amount_mismatch"},
		{"broker down", "", "lookup_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tapRig(t)
			e.broker.setStatus(tc.reply)
			rec := e.tender(t)
			if e.sales(t) != 0 || e.dp.Engine.TenderLock() == "" {
				t.Fatalf("must not sell: sales=%d lock=%q", e.sales(t), e.dp.Engine.TenderLock())
			}
			e.wantText(t, rec, "tap.pending.title", "tap.check_again", "tap.ask_manager")
			if a := e.attempt(t); a.State != data.AttemptUnknown || a.Reason != tc.reason {
				t.Fatalf("attempt = %+v, want unknown/%s", a, tc.reason)
			}
			if strings.Contains(strings.ToLower(rec.Body.String()), "declin") {
				t.Fatalf("a pending payment is never 'declined': %s", rec.Body.String())
			}
		})
	}
}

func TestTapGate_StartFailureWithNoAttemptRowIsNotDeclinedAndUnlocks(t *testing.T) {
	e := tapRig(t)
	e.noRow = true
	e.plugin.answer = func(int) string {
		return "START_FAILED"
	}
	rec := e.tender(t)
	if e.sales(t) != 0 || e.dp.Engine.TenderLock() != "" {
		t.Fatalf("a start failure moves no money and must not lock: lock=%q", e.dp.Engine.TenderLock())
	}
	e.wantText(t, rec, "tap.start_failed")
	if strings.Contains(strings.ToLower(rec.Body.String()), "declin") {
		t.Fatalf("a start failure is never 'declined': %s", rec.Body.String())
	}
	if e.broker.count("session_status") != 0 {
		t.Fatal("no attempt row means no PaymentIntent: the broker has nothing to be asked")
	}
}

// K-2 (review-batch): once an attempt row exists a tap may have happened, so a
// plugin's start_failed or decline is verified with the broker first.
func TestTapGate_FailureAfterAttemptRowIsVerifiedWithBroker(t *testing.T) {
	for _, ans := range []string{"START_FAILED", "DECLINE"} {
		t.Run(ans+" broker says paid completes the sale", func(t *testing.T) {
			e := tapRig(t)
			e.plugin.answer = func(int) string { return ans }
			e.broker.setStatus(piStatus("succeeded", 120))
			e.tender(t)
			if e.sales(t) != 1 {
				t.Fatalf("the broker says the card was charged: the sale must complete (sales=%d)", e.sales(t))
			}
			if a := e.attempt(t); a.State != data.AttemptApproved {
				t.Fatalf("attempt = %+v", a)
			}
		})
		t.Run(ans+" broker unsure locks as pending", func(t *testing.T) {
			e := tapRig(t)
			e.plugin.answer = func(int) string { return ans }
			e.broker.setStatus(piStatus("processing", 0))
			rec := e.tender(t)
			if e.sales(t) != 0 || e.dp.Engine.TenderLock() == "" {
				t.Fatalf("an unverified failure must lock as pending (lock=%q)", e.dp.Engine.TenderLock())
			}
			e.wantText(t, rec, "tap.pending.title", "tap.check_again")
			if strings.Contains(rec.Body.String(), enText(t, "tap.start_failed")) {
				t.Fatal("must not say nothing was charged")
			}
		})
		t.Run(ans+" broker down locks as pending", func(t *testing.T) {
			e := tapRig(t)
			e.plugin.answer = func(int) string { return ans }
			e.broker.setStatus("")
			e.tender(t)
			if e.dp.Engine.TenderLock() == "" {
				t.Fatal("broker unreachable after a possible tap must lock")
			}
		})
		t.Run(ans+" broker says not paid cancels and unlocks", func(t *testing.T) {
			e := tapRig(t)
			e.plugin.answer = func(int) string { return ans }
			e.broker.setStatus(piStatus("requires_payment_method", 0))
			e.tender(t)
			if e.dp.Engine.TenderLock() != "" || e.broker.count("session_cancel") != 1 {
				t.Fatalf("not paid: cancel once and unlock (lock=%q cancels=%d)", e.dp.Engine.TenderLock(), e.broker.count("session_cancel"))
			}
		})
	}
}

// A tap entry that nobody answers for took no payment: the sale must not complete.
func TestTapGate_EntryWithNoAuthorizeSubscriberNeverSells(t *testing.T) {
	e := tapRig(t)
	plugins.SharedBus(e.dp.Db).ResetSubscribers()
	if _, err := e.dp.Db.Exec(`DELETE FROM plugin_hooks WHERE plugin_id = 'p.tap'`); err != nil {
		t.Fatal(err)
	}
	e.tender(t)
	if e.sales(t) != 0 {
		t.Fatal("a tap entry with no authorize subscriber sold goods without taking a payment")
	}
}

// A tap entry whose config names no provider cannot be confirmed: never asked, never sold.
func TestTapGate_EntryWithoutProviderFailsClosed(t *testing.T) {
	e := tapRig(t)
	if _, err := e.dp.Db.Exec(`UPDATE plugin_entries SET config_json = '{"tap":true}' WHERE plugin_id = 'p.tap'`); err != nil {
		t.Fatal(err)
	}
	e.tender(t)
	if e.sales(t) != 0 || len(e.plugin.authCalls()) != 0 {
		t.Fatalf("a tap entry with no provider must be refused before any plugin runs (sales=%d calls=%d)", e.sales(t), len(e.plugin.authCalls()))
	}
}

// --- Check again ---

func TestTapCheck_CashierAllowedKeyNeverFromForm(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	key := e.lastBinding(t).AttemptKey
	rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}, "key": {"attacker-key"}, "idempotency_key": {"attacker-key"}, "payment_intent_id": {"pi_other"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("cashier check = %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range e.broker.log() {
		if strings.Contains(c, "attacker-key") || strings.Contains(c, "pi_other") {
			t.Fatalf("a form value reached the broker: %s", c)
		}
	}
	if !strings.Contains(strings.Join(e.broker.log(), "\n"), key) {
		t.Fatalf("broker was never asked about the attempt key %s: %v", key, e.broker.log())
	}
}

func TestTapCheck_NothingPendingIsConflict(t *testing.T) {
	e := tapRig(t)
	rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("no pending tap tender = %d, want 409", rec.Code)
	}
}

func TestTapCheck_SucceededApprovesOnceAndPaidAlready(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setStatus(piStatus("succeeded", 120))
	rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	e.wantText(t, rec, "tap.paid_already", "tap.continue")
	if a := e.attempt(t); a.State != data.AttemptApproved || a.PIID != "pi_k1b" {
		t.Fatalf("approved from the broker: %+v", a)
	}
	// A second check is a no-op: the attempt is approved once.
	rec = e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	e.wantText(t, rec, "tap.paid_already")
	// Pay again finishes the sale on the confirmed payment, without asking the plugin to charge again.
	before := len(e.plugin.authCalls())
	e.tender(t)
	if e.sales(t) != 1 {
		t.Fatalf("the settled tender must complete the sale")
	}
	if got := len(e.plugin.authCalls()); got != before {
		t.Fatalf("a confirmed tap must not be sent to the plugin again (%d -> %d)", before, got)
	}
}

func TestTapCheck_RequiresPaymentMethodCancelsThenUnlocks(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	e.wantText(t, rec, "tap.not_paid", "tap.result.try_again")
	if e.broker.count("session_cancel") != 1 {
		t.Fatalf("session_cancel must be sent once: %v", e.broker.log())
	}
	if e.dp.Engine.TenderLock() != "" {
		t.Fatal("the tender unlocks only once the broker says not paid")
	}
	if a := e.attempt(t); a.State != data.AttemptCancelled {
		t.Fatalf("attempt = %+v", a)
	}
}

// K-1 (review-batch): the cancel answers already_succeeded (the customer tapped
// between status and cancel): the till must show Paid, never "Not paid".
func TestTapCheck_CancelAlreadySucceededIsPaidNotNotPaid(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setSeq(piStatus("requires_payment_method", 0), piStatus("succeeded", 120))
	e.broker.setCancel(http.StatusConflict, `{"error":{"code":"already_succeeded"}}`)
	rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	e.wantText(t, rec, "tap.paid_already")
	if strings.Contains(rec.Body.String(), enText(t, "tap.not_paid")) {
		t.Fatal("must not show Not paid for a payment that succeeded")
	}
	if a := e.attempt(t); a.State != data.AttemptApproved {
		t.Fatalf("attempt must be approved: %+v", a)
	}
}

func TestTapCheck_CancelFailureStaysPending(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	e.broker.setCancel(http.StatusServiceUnavailable, `{"error":{"code":"provider_unavailable"}}`)
	rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
	if e.dp.Engine.TenderLock() == "" {
		t.Fatal("an uncancelled payment intent may still be tapped: stay locked")
	}
	if strings.Contains(rec.Body.String(), enText(t, "tap.not_paid")) {
		t.Fatal("must not show Not paid when the cancel failed")
	}
	if a := e.attempt(t); a.State == data.AttemptCancelled {
		t.Fatalf("attempt must not be closed: %+v", a)
	}
}

func TestTapResolve_NotChargedCancelAlreadySucceededIsRefused(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setSeq(piStatus("requires_payment_method", 0), piStatus("succeeded", 120))
	e.broker.setCancel(http.StatusConflict, `{"error":{"code":"already_succeeded"}}`)
	rec := e.resolve(t, e.manager, url.Values{"decision": {"not_charged"}, "override_pin": {dataAPITestManagerPIN}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("a payment that succeeded cannot be resolved as not charged: %d %s", rec.Code, rec.Body.String())
	}
	if e.dp.Engine.TenderLock() == "" {
		t.Fatal("must stay locked")
	}
	if a := e.attempt(t); a.State == data.AttemptResolvedNotCharged || a.State == data.AttemptCancelled {
		t.Fatalf("attempt must not be closed as not charged: %+v", a)
	}
}

func TestTapCheck_ProcessingOrBrokerDownStaysPending(t *testing.T) {
	for name, reply := range map[string]string{"processing": piStatus("processing", 0), "broker down": ""} {
		t.Run(name, func(t *testing.T) {
			e := tapRig(t)
			e.pending(t)
			e.broker.setStatus(reply)
			rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
			e.wantText(t, rec, "tap.pending.title", "tap.pending.still")
			if e.dp.Engine.TenderLock() == "" || e.broker.count("session_cancel") != 0 {
				t.Fatal("must stay locked and must not cancel")
			}
		})
	}
}

func TestTapCheck_ThrottledToOnePerInterval(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	tapCheckInterval = time.Hour
	before := e.broker.count("session_status")
	for i := 0; i < 3; i++ {
		rec := e.do(t, e.cashier, http.MethodPost, "/pay/tap/check", url.Values{"leg": {"0"}})
		if rec.Code != http.StatusOK {
			t.Fatalf("a throttled check is not an error: %d", rec.Code)
		}
	}
	if got := e.broker.count("session_status") - before; got != 1 {
		t.Fatalf("3 checks inside the interval reached the broker %d times, want 1", got)
	}
}

// --- manager resolve ---

func (e *tapEnv) resolve(t *testing.T, u auth.User, form url.Values) *httptest.ResponseRecorder {
	return e.do(t, u, http.MethodPost, "/manager/pay/tap/resolve", form)
}

func TestTapResolve_CashierAndMissingPINAre403(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	if rec := e.resolve(t, e.cashier, url.Values{"decision": {"not_charged"}, "override_pin": {dataAPITestManagerPIN}}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
	for name, pin := range map[string]string{"no pin": "", "wrong pin": "000000"} {
		if rec := e.resolve(t, e.manager, url.Values{"decision": {"not_charged"}, "override_pin": {pin}}); rec.Code != http.StatusForbidden {
			t.Fatalf("manager %s = %d, want 403", name, rec.Code)
		}
	}
	if e.dp.Engine.TenderLock() == "" {
		t.Fatal("a refused resolve changes nothing")
	}
}

func TestTapResolve_BrokerSucceededBlocksNotChargedOffersApprove(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setStatus(piStatus("succeeded", 120))
	rec := e.resolve(t, e.manager, url.Values{"decision": {"not_charged"}, "override_pin": {dataAPITestManagerPIN}})
	if rec.Code != http.StatusConflict || e.dp.Engine.TenderLock() == "" {
		t.Fatalf("the payment succeeded: not-charged must be refused (%d), lock=%q", rec.Code, e.dp.Engine.TenderLock())
	}
	page := e.do(t, e.manager, http.MethodGet, "/ui/pay/tap/resolve", nil)
	e.wantText(t, page, "tap.resolve.approve", "tap.resolve.status.succeeded")
	if strings.Contains(bodyText(page), enText(t, "tap.resolve.not_charged")) {
		t.Fatal("'Not charged' must not be offered when the broker says paid")
	}
	rec = e.resolve(t, e.manager, url.Values{"decision": {"approve"}, "override_pin": {dataAPITestManagerPIN}})
	e.wantText(t, rec, "tap.paid_already")
	if a := e.attempt(t); a.State != data.AttemptApproved {
		t.Fatalf("approve goes through core's confirm: %+v", a)
	}
}

func TestTapResolve_NotPaidUnlocksAndIsAudited(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	rec := e.resolve(t, e.manager, url.Values{"decision": {"not_charged"}, "override_pin": {dataAPITestManagerPIN}})
	e.wantText(t, rec, "tap.not_paid")
	if e.dp.Engine.TenderLock() != "" {
		t.Fatal("tender must unlock")
	}
	if a := e.attempt(t); a.State != data.AttemptResolvedNotCharged {
		t.Fatalf("attempt = %+v", a)
	}
	if n := countRows(t, e.dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'tap_resolve' AND actor_id = '`+e.manager.ID+`'`); n != 1 {
		t.Fatalf("want one tap_resolve audit row by the manager, got %d", n)
	}
}

func TestTapResolve_BrokerUnreachableNeedsReasonAndRecordsOverride(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	e.broker.setStatus("")
	form := url.Values{"decision": {"not_charged"}, "override_pin": {dataAPITestManagerPIN}}
	if rec := e.resolve(t, e.manager, form); rec.Code != http.StatusUnprocessableEntity || e.dp.Engine.TenderLock() == "" {
		t.Fatalf("without a reason the override is refused: %d", rec.Code)
	}
	form.Set("reason", "card_not_charged")
	rec := e.resolve(t, e.manager, form)
	e.wantText(t, rec, "tap.not_paid")
	if e.dp.Engine.TenderLock() != "" {
		t.Fatal("override unlocks")
	}
	var payload string
	if err := e.dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'tap_resolve'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"manager_override", "card_not_charged", e.manager.ID, "provider_unavailable"} {
		if !strings.Contains(payload, want) {
			t.Errorf("audit payload lacks %q: %s", want, payload)
		}
	}
}

func TestTapResolve_AmountMismatchShowsBothAmountsAndOverrides(t *testing.T) {
	e := tapRig(t)
	e.broker.setStatus(piStatus("succeeded", 119))
	e.tender(t)
	page := e.do(t, e.manager, http.MethodGet, "/ui/pay/tap/resolve", nil)
	e.wantText(t, page, "tap.amount_mismatch", "tap.amount.expected", "tap.amount.received", "tap.resolve.override")
	for _, amt := range []string{"1.20", "1.19"} {
		if !strings.Contains(page.Body.String(), amt) {
			t.Errorf("both amounts must show, missing %s: %s", amt, page.Body.String())
		}
	}
	if strings.Contains(bodyText(page), enText(t, "tap.resolve.approve")) {
		t.Fatal("a mismatch can only be accepted by override, never plain approve")
	}
}

func TestTapResolve_CashierGetsAskManagerHint(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	rec := e.do(t, e.cashier, http.MethodGet, "/ui/pay/tap/resolve", nil)
	e.wantText(t, rec, "tap.ask_manager.hint")
	if strings.Contains(bodyText(rec), enText(t, "tap.resolve.not_charged")) {
		t.Fatal("the cashier is never shown the resolve choices")
	}
}

// --- enrolment endpoint stays unwritable from the cashier route and a synced bundle ---

func TestTapRoutes_DoNotAcceptABrokerTarget(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	evil := newFakeBroker(t) // replaces the resolver: put the real one back
	e2 := e.broker
	paybroker.SetTargetResolver(func(context.Context) (paybroker.Target, bool) {
		return paybroker.Target{BaseURL: e2.srv.URL, Bearer: "cred"}, true
	})
	for _, p := range []string{"/pay/tap/check", "/manager/pay/tap/resolve"} {
		u := e.cashier
		e.do(t, u, http.MethodPost, p, url.Values{"leg": {"0"}, "endpoint_url": {evil.srv.URL}, "base_url": {evil.srv.URL}, "marketplace.endpoint_url": {evil.srv.URL}, "token": {"stolen"}})
	}
	if n := len(evil.log()); n != 0 {
		t.Fatalf("a form value redirected the broker call: %v", evil.log())
	}
}

func TestAdminBundleCannotRewriteBrokerTarget(t *testing.T) {
	_, dp := pickerRig(t)
	if _, err := dp.Db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'http://main.invalid')`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewSyncAdminRepo(dp.Db)
	rows := []map[string]any{}
	for _, k := range []string{"marketplace.endpoint_url", "marketplace.endpoint", "marketplace.token", "marketplace.device_id"} {
		rows = append(rows, map[string]any{"key": k, "value": "https://evil.invalid"})
	}
	if _, err := repo.ApplyAdminWithResult(context.Background(), data.AdminBundle{Tables: map[string][]map[string]any{"settings": rows}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'marketplace.endpoint%' OR key IN ('marketplace.token','marketplace.device_id')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a synced admin bundle wrote %d enrolment/broker keys", n)
	}
}

// --- copy ---

// A pending, mismatch or start-failure state is never "declined" (ADR-0198
// cashier rules; a "declined" that was an approval double-charges, #6439).
// Locale-driven: every shipped locale that carries these keys is checked, and
// English must carry all of them.
func TestNoDeclinedWordOnPendingOrStartFailure(t *testing.T) {
	_, file, _, _ := runtimeCallerFile()
	dir := filepath.Join(filepath.Dir(file), "..", "..", "web", "locales")
	declined := map[string][]string{
		"en": {"declin"}, "tr": {"reddedil"}, "ar": {"مرفوض", "رفض"}, "fa": {"رد شد", "رد شده"},
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sawEn := false
	for _, f := range files {
		loc := strings.TrimSuffix(filepath.Base(f), ".json")
		raw, _ := os.ReadFile(f)
		var m map[string]string
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		checked := 0
		for k, v := range m {
			if !isTapNoDeclineKey(k) {
				continue
			}
			checked++
			for _, w := range declined[loc] {
				if strings.Contains(strings.ToLower(v), w) {
					t.Errorf("%s %s = %q must not say declined", loc, k, v)
				}
			}
		}
		if loc == "en" {
			sawEn = true
			if checked < 20 {
				t.Errorf("en.json carries only %d tap pending/resolve keys, the K1b screens need them all", checked)
			}
		}
	}
	if !sawEn {
		t.Fatal("no en.json")
	}
}

func isTapNoDeclineKey(k string) bool {
	if !strings.HasPrefix(k, "tap.") {
		return false
	}
	switch {
	case strings.HasPrefix(k, "tap.result.declined"), strings.HasPrefix(k, "tap.reader."):
		return false
	}
	return true
}

func runtimeCallerFile() (uintptr, string, int, bool) { return runtime.Caller(1) }
