package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/paybroker"
)

// ut-docs#6455: payment_provider_call and tap_to_pay_request.

type tapBridge struct {
	mu       sync.Mutex
	collects []string
	reply    string
	seenRow  func(key string) bool // asked at Collect time: is the attempt row already there?
	rowFirst bool
}

func (b *tapBridge) Capabilities() string { return `{}` }
func (b *tapBridge) Collect(req string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.collects = append(b.collects, req)
	if b.seenRow != nil {
		var r devicepay.Request
		_ = json.Unmarshal([]byte(req), &r)
		b.rowFirst = b.seenRow(r.IdempotencyKey)
	}
	return b.reply
}
func (b *tapBridge) Cancel(string)               {}
func (b *tapBridge) Setup(string) string         { return `{"ok":true}` }
func (b *tapBridge) ShowEducation(string) string { return `{"ok":true}` }

type brokerFixture struct {
	w    *WasmRuntime
	d    *sql.DB
	id   string
	hits *atomic.Int32
	body *atomic.Value
}

func newBrokerFixture(t *testing.T, perms ...string) *brokerFixture {
	t.Helper()
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const id = "com.test.tapplugin"
	w := newDeviceInfoRuntime(t, guest, d, id, func() {
		for _, p := range perms {
			grantPerm(t, d, id, p)
		}
	})
	var hits atomic.Int32
	var last atomic.Value
	last.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		last.Store(r.URL.Path + " " + string(b))
		_, _ = io.WriteString(rw, `{"payment_intent_id":"pi_1","status":"succeeded","amount_received_minor":500,"currency":"gbp","client_secret":"pi_1_secret","secret":"pst_X","card":{"brand":"visa","last4":"4242"}}`)
	}))
	t.Cleanup(srv.Close)
	paybroker.SetTargetResolver(func(context.Context) (paybroker.Target, bool) {
		return paybroker.Target{BaseURL: srv.URL, Bearer: "cred"}, true
	})
	t.Cleanup(func() { paybroker.SetTargetResolver(nil) })
	return &brokerFixture{w: w, d: d, id: id, hits: &hits, body: &last}
}

// markTapEntry declares entryKey of the fixture plugin a tap: true entry (what
// refreshTapEntries derives from the manifest).
func (f *brokerFixture) markTapEntry(entryKey string) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	f.w.tapEntries = map[string]map[string]bool{f.id: {entryKey: true}}
}

// run raises one core payment event, bound or not, and returns the guest's
// recorded calls.
func (f *brokerFixture) run(t *testing.T, ctx context.Context, eventType string, payload map[string]any) []map[string]any {
	t.Helper()
	body, _ := json.Marshal(payload)
	if _, err := f.w.HandleEvent(ctx, f.id, Event{ID: "ev1", Type: eventType, Timestamp: time.Now(), Payload: body}); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	raw, err := data.NewPluginRepo(f.d).StorageGet(context.Background(), f.id, "results")
	if err != nil {
		t.Fatalf("results: %v", err)
	}
	var res struct {
		Calls []map[string]any `json:"calls"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("results %s: %v", raw, err)
	}
	return res.Calls
}

func authorizeCtx() context.Context {
	return paybroker.WithBinding(context.Background(), paybroker.Binding{
		Kind: paybroker.BindAuthorize, Provider: "stripe", AttemptKey: "att:1:h", PI: "pi_1", AmountMinor: 500, Currency: "gbp",
	})
}

func code(c map[string]any) int { v, _ := c["code"].(float64); return int(v) }

func TestHostFnNoBindingNoRequest(t *testing.T) {
	f := newBrokerFixture(t, "storage", BrokerNetPermission)
	for _, op := range []string{"session_status", "session_cancel", "refund"} {
		calls := f.run(t, context.Background(), "payment.stripe_tap.authorize", map[string]any{"mode": "payment_call", "provider": "stripe", "op": op, "body": `{}`})
		if code(calls[0]) != hostErrDenied {
			t.Errorf("%s without a dispatch binding = %v, want denied", op, calls[0])
		}
	}
	// A binding on a non-gate event does not count either.
	calls := f.run(t, authorizeCtx(), "test.event", map[string]any{"mode": "payment_call", "provider": "stripe", "op": "session_status", "body": `{}`})
	if code(calls[0]) != hostErrDenied {
		t.Errorf("binding on a non-gate event = %v, want denied", calls[0])
	}
	if f.hits.Load() != 0 {
		t.Fatalf("%d requests reached the broker", f.hits.Load())
	}
}

func TestHostFnNeverReachesCreateTokenOrOnboardingStart(t *testing.T) {
	f := newBrokerFixture(t, "storage", BrokerNetPermission)
	for _, op := range []string{"session_create", "connection_token", "onboarding_start", "payouts"} {
		calls := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", map[string]any{"mode": "payment_call", "provider": "stripe", "op": op, "body": `{}`})
		if code(calls[0]) != hostErrDenied {
			t.Errorf("%s via host fn = %v, want denied", op, calls[0])
		}
	}
	if f.hits.Load() != 0 {
		t.Fatalf("%d requests reached the broker", f.hits.Load())
	}
}

func TestHostFnAnswerIsNeutralAndSecretFree(t *testing.T) {
	f := newBrokerFixture(t, "storage", BrokerNetPermission)
	calls := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", map[string]any{"mode": "payment_call", "provider": "stripe", "op": "session_status", "body": `{"idempotency_key":"evil"}`})
	if v, _ := calls[0]["val"].(string); !strings.Contains(v, `"code":"foreign_payment"`) || f.hits.Load() != 0 { // refused, never forwarded
		t.Fatalf("foreign key = %v (hits %d), want a foreign_payment answer and no request", calls[0], f.hits.Load())
	}
	calls = f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", map[string]any{"mode": "payment_call", "provider": "stripe", "op": "session_status", "body": `{}`})
	val, _ := calls[0]["val"].(string)
	for _, bad := range []string{"client_secret", "pi_1_secret", "pst_X", "secret"} {
		if strings.Contains(val, bad) {
			t.Fatalf("plugin got %q in %s", bad, val)
		}
	}
	for _, want := range []string{`"id":"pi_1"`, `"card_brand":"visa"`, `"last4":"4242"`, `"status":"succeeded"`} {
		if !strings.Contains(val, want) {
			t.Fatalf("answer lacks %s: %s", want, val)
		}
	}
	if got, _ := f.body.Load().(string); strings.Contains(got, `"idempotency_key"`) || !strings.Contains(got, `"payment_intent_id":"pi_1"`) {
		t.Fatalf("outgoing body must carry exactly the binding's PaymentIntent (the cloud refuses both ids): %s", got)
	}
}

func TestHostFnBrokerFailureIsAnAnswerNotAHostError(t *testing.T) {
	f := newBrokerFixture(t, "storage", BrokerNetPermission)
	paybroker.SetTargetResolver(nil)
	calls := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", map[string]any{"mode": "payment_call", "provider": "stripe", "op": "session_status", "body": `{}`})
	val, _ := calls[0]["val"].(string)
	if code(calls[0]) <= 0 || !strings.Contains(val, `"code":"not_enrolled"`) {
		t.Fatalf("failure = %v, want an error answer with code not_enrolled", calls[0])
	}
}

func TestPermissionCheckedEveryCall(t *testing.T) {
	f := newBrokerFixture(t, "storage", BrokerNetPermission)
	args := map[string]any{"mode": "payment_call", "provider": "stripe", "op": "session_status", "body": `{}`, "calls": 1}
	if c := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", args); code(c[0]) <= 0 {
		t.Fatalf("granted call = %v", c[0])
	}
	// Revoked between two events: the very next call is refused (no cache).
	if err := data.NewPluginRepo(f.d).SetPermission(context.Background(), f.id, BrokerNetPermission, false); err != nil {
		t.Fatal(err)
	}
	before := f.hits.Load()
	c := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", args)
	if code(c[0]) != hostErrDenied || f.hits.Load() != before {
		t.Fatalf("revoked call = %v, hits %d -> %d", c[0], before, f.hits.Load())
	}
	if rows := auditRows(t, f.d, f.id, "permission_denied"); len(rows) == 0 || rows[len(rows)-1].Permission != BrokerNetPermission {
		t.Fatalf("denial not audited: %+v", rows)
	}
	// Two calls inside ONE event each check the permission: revoke mid-run is
	// not testable from a guest, so assert the checker is invoked per call.
	if c := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", map[string]any{"mode": "payment_call", "provider": "stripe", "op": "session_status", "body": `{}`, "calls": 3}); len(c) != 3 {
		t.Fatalf("calls = %d", len(c))
	}
	if n := len(auditRows(t, f.d, f.id, "permission_denied")); n < 4 {
		t.Fatalf("every refused call must be audited, got %d rows", n)
	}
}

func TestTapRequestNeedsPermissionAndBoundAuthorize(t *testing.T) {
	br := &tapBridge{reply: `{"status":"approved","card_brand":"visa","last4":"4242"}`}
	devicepay.SetBridge(br)
	t.Cleanup(func() { devicepay.SetBridge(nil) })

	f := newBrokerFixture(t, "storage") // no device:tap_to_pay
	args := map[string]any{"mode": "tap_request", "body": `{"amount_minor":1,"currency":"gbp","idempotency_key":"x"}`}
	if c := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", args); code(c[0]) != hostErrDenied {
		t.Fatalf("no permission: %v", c[0])
	}
	grantPerm(t, f.d, f.id, TapDevicePermission)
	f.markTapEntry("stripe_tap")
	if c := f.run(t, context.Background(), "payment.stripe_tap.authorize", args); code(c[0]) != hostErrDenied {
		t.Fatalf("no binding: %v", c[0])
	}
	refundCtx := paybroker.WithBinding(context.Background(), paybroker.Binding{Kind: paybroker.BindRefund, Provider: "stripe", PI: "pi_1", MaxRefundMinor: 5, RefundKey: "r"})
	if c := f.run(t, refundCtx, "payment.stripe_tap.refund", args); code(c[0]) != hostErrDenied {
		t.Fatalf("refund binding: %v", c[0])
	}
	if len(br.collects) != 0 {
		t.Fatalf("collect ran %d times without entitlement", len(br.collects))
	}
}

func TestTapRequestUsesCoreAmountWritesRowBeforeTap(t *testing.T) {
	f := newBrokerFixture(t, "storage", TapDevicePermission)
	f.markTapEntry("stripe_tap")
	br := &tapBridge{reply: `{"status":"approved","card_brand":"visa","last4":"4242"}`}
	repo := data.NewPaymentAttemptRepo(f.d)
	br.seenRow = func(key string) bool {
		a, ok, err := repo.Get(context.Background(), key)
		return err == nil && ok && a.State == data.AttemptPending
	}
	devicepay.SetBridge(br)
	t.Cleanup(func() { devicepay.SetBridge(nil) })

	// The plugin asks for a different amount, key and currency: ignored.
	calls := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", map[string]any{"mode": "tap_request", "body": `{"amount_minor":1,"currency":"eur","idempotency_key":"evil"}`})
	if code(calls[0]) <= 0 {
		t.Fatalf("tap_request = %v", calls[0])
	}
	if len(br.collects) != 1 {
		t.Fatalf("collects = %v", br.collects)
	}
	var req devicepay.Request
	_ = json.Unmarshal([]byte(br.collects[0]), &req)
	if req.AmountMinor != 500 || req.Currency != "gbp" || req.IdempotencyKey != "att:1:h" || req.Provider != "stripe" {
		t.Fatalf("collect request = %+v, want core's amount/currency/key", req)
	}
	if !br.rowFirst {
		t.Fatal("the attempt row was not written before the tap")
	}
	if a, ok, _ := repo.Get(context.Background(), "att:1:h"); !ok || a.AmountMinor != 500 || a.State != data.AttemptPending {
		t.Fatalf("row = %+v ok=%v (advisory result must not settle it)", a, ok)
	}
	if _, ok, _ := repo.Get(context.Background(), "evil"); ok {
		t.Fatal("a plugin-named key got a row")
	}
	if f.hits.Load() != 0 {
		t.Fatal("tap_to_pay_request must not call the cloud itself")
	}
}

func TestTapRequestDeadlineIsAtMost120s(t *testing.T) {
	f := newBrokerFixture(t, "storage", TapDevicePermission)
	f.markTapEntry("stripe_tap")
	var got int64
	br := &tapBridge{reply: `{"status":"pending"}`}
	devicepay.SetBridge(bridgeFunc{tapBridge: br, onCollect: func(req string) {
		var r devicepay.Request
		_ = json.Unmarshal([]byte(req), &r)
		got = r.DeadlineUnixMs
	}})
	t.Cleanup(func() { devicepay.SetBridge(nil) })
	now := time.Now()
	f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", map[string]any{"mode": "tap_request", "body": `{}`})
	if left := time.UnixMilli(got).Sub(now); left <= 0 || left > TapGateTimeout+2*time.Second {
		t.Fatalf("collect deadline in %v, want within (0, 120s]", left)
	}
}

type bridgeFunc struct {
	*tapBridge
	onCollect func(string)
}

func (b bridgeFunc) Collect(req string) string {
	b.onCollect(req)
	return b.tapBridge.Collect(req)
}

// ut-docs#6455 G-4: tap_to_pay_request is bound to the manifest entry that
// declares tap: true. A plugin holding device:tap_to_pay but running another
// entry (a non-tap entry, or an entry key it does not own) gets -2 and the
// bridge is never called, even with a valid bound authorize dispatch.
func TestTapRequestBoundToTapTrueEntryOnly(t *testing.T) {
	f := newBrokerFixture(t, "storage", TapDevicePermission)
	br := &tapBridge{reply: `{"status":"approved","card_brand":"visa","last4":"4242"}`}
	devicepay.SetBridge(br)
	t.Cleanup(func() { devicepay.SetBridge(nil) })
	f.markTapEntry("stripe_tap")
	args := map[string]any{"mode": "tap_request", "body": `{}`}

	// The plugin's other entry (stripe_card) did not declare tap.
	if c := f.run(t, authorizeCtx(), "payment.stripe_card.authorize", args); code(c[0]) != hostErrDenied {
		t.Fatalf("non-tap entry: code %d, want %d (denied)", code(c[0]), hostErrDenied)
	}
	if len(br.collects) != 0 {
		t.Fatalf("bridge collected %d times for a non-tap entry", len(br.collects))
	}
	// A different plugin's tap entry key does not count for this plugin.
	f.w.mu.Lock()
	f.w.tapEntries = map[string]map[string]bool{"com.other.plugin": {"stripe_card": true}}
	f.w.mu.Unlock()
	if c := f.run(t, authorizeCtx(), "payment.stripe_card.authorize", args); code(c[0]) != hostErrDenied {
		t.Fatalf("another plugin's tap entry: code %d, want %d", code(c[0]), hostErrDenied)
	}
	// The declared tap entry still works.
	f.markTapEntry("stripe_tap")
	if c := f.run(t, authorizeCtx(), "payment.stripe_tap.authorize", args); code(c[0]) <= 0 {
		t.Fatalf("tap entry: %v", c[0])
	}
	if len(br.collects) != 1 {
		t.Fatalf("collects = %d, want 1", len(br.collects))
	}
}
