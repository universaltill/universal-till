package devicepay

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBridge is a scripted native side. collect blocks until release is
// closed (when set) so a test can expire the context first.
type fakeBridge struct {
	mu          sync.Mutex
	caps        string
	reply       string
	release     chan struct{}
	collects    []string
	cancels     []string
	setupReply  string
	setupCalls  []string
	eduReply    string
	cancelCalls chan string
}

func (f *fakeBridge) Capabilities() string { return f.caps }

func (f *fakeBridge) Collect(req string) string {
	f.mu.Lock()
	f.collects = append(f.collects, req)
	f.mu.Unlock()
	if f.release != nil {
		<-f.release
	}
	return f.reply
}

func (f *fakeBridge) Cancel(key string) {
	f.mu.Lock()
	f.cancels = append(f.cancels, key)
	f.mu.Unlock()
	if f.cancelCalls != nil {
		f.cancelCalls <- key
	}
}

func (f *fakeBridge) Setup(provider string) string {
	f.mu.Lock()
	f.setupCalls = append(f.setupCalls, provider)
	f.mu.Unlock()
	return f.setupReply
}

func (f *fakeBridge) ShowEducation(string) string { return f.eduReply }

func (f *fakeBridge) collectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.collects)
}

func register(t *testing.T, b Bridge) {
	t.Helper()
	SetBridge(b)
	t.Cleanup(func() { SetBridge(nil) })
}

func validRequest() Request {
	return Request{Provider: "stripe", AmountMinor: 1000, Currency: "gbp", IdempotencyKey: "k-1", Description: "Sale 42"}
}

func deadlineCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestBridgeRegistration(t *testing.T) {
	if RegisteredBridge() != nil {
		t.Fatal("no bridge is registered by default")
	}
	fb := &fakeBridge{}
	register(t, fb)
	if RegisteredBridge() != Bridge(fb) {
		t.Fatal("RegisteredBridge is not the registered bridge")
	}
	SetBridge(nil)
	if RegisteredBridge() != nil {
		t.Fatal("SetBridge(nil) must un-register")
	}
}

// ADR-0125 §3e: no foreground native shell → declined/not_foreground.
func TestCollect_NoBridge(t *testing.T) {
	got := Collect(deadlineCtx(t, time.Second), validRequest())
	if got.Status != StatusDeclined || got.Reason != "not_foreground" {
		t.Fatalf("Collect with no bridge = %+v, want declined/not_foreground", got)
	}
}

// Validation runs before the bridge is ever reached: nothing is charged
// for a malformed request.
func TestCollect_ValidationNeverCallsBridge(t *testing.T) {
	fb := &fakeBridge{reply: `{"status":"approved","provider_txn_id":"pi_1"}`}
	register(t, fb)
	cases := map[string]func(*Request){
		"zero amount":      func(r *Request) { r.AmountMinor = 0 },
		"negative amount":  func(r *Request) { r.AmountMinor = -5 },
		"currency length":  func(r *Request) { r.Currency = "gb" },
		"currency digits":  func(r *Request) { r.Currency = "g1p" },
		"empty key":        func(r *Request) { r.IdempotencyKey = "" },
		"unknown provider": func(r *Request) { r.Provider = "sumup" },
		"empty provider":   func(r *Request) { r.Provider = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := validRequest()
			mutate(&req)
			got := Collect(deadlineCtx(t, time.Second), req)
			if got.Status != StatusDeclined || got.Reason != "bad_request" {
				t.Fatalf("Collect = %+v, want declined/bad_request", got)
			}
		})
	}
	t.Run("no deadline", func(t *testing.T) {
		got := Collect(context.Background(), validRequest())
		if got.Status != StatusDeclined || got.Reason != "bad_request" {
			t.Fatalf("Collect without a deadline = %+v, want declined/bad_request", got)
		}
	})
	if n := fb.collectCount(); n != 0 {
		t.Fatalf("bridge.Collect called %d times for invalid requests, want 0", n)
	}
}

// The native side gets the request as JSON with the context's deadline.
func TestCollect_PassesRequestAndDeadline(t *testing.T) {
	fb := &fakeBridge{reply: `{"status":"approved","provider_txn_id":"pi_1","charge_id":"ch_1","card_brand":"visa","last4":"4242"}`}
	register(t, fb)
	ctx := deadlineCtx(t, 30*time.Second)
	dl, _ := ctx.Deadline()
	got := Collect(ctx, validRequest())
	want := Result{Status: StatusApproved, ProviderTxnID: "pi_1", ChargeID: "ch_1", CardBrand: "visa", Last4: "4242"}
	if got != want {
		t.Fatalf("Collect = %+v, want %+v", got, want)
	}
	var sent Request
	if err := json.Unmarshal([]byte(fb.collects[0]), &sent); err != nil {
		t.Fatalf("bridge got non-JSON request %q: %v", fb.collects[0], err)
	}
	if sent.DeadlineUnixMs != dl.UnixMilli() || sent.IdempotencyKey != "k-1" || sent.AmountMinor != 1000 {
		t.Fatalf("bridge got %+v, want deadline %d and the request fields", sent, dl.UnixMilli())
	}
}

// ADR-0125 §3d: the context ends first → Cancel(key) on the native side,
// and the outcome is pending (the tap may still have gone through).
func TestCollect_TimeoutCancelsAndIsPending(t *testing.T) {
	release := make(chan struct{})
	fb := &fakeBridge{reply: `{"status":"cancelled","reason":"timeout"}`, release: release, cancelCalls: make(chan string, 1)}
	register(t, fb)
	t.Cleanup(func() { close(release) })
	got := Collect(deadlineCtx(t, 50*time.Millisecond), validRequest())
	if got.Status != StatusPending || got.Reason != "timeout" {
		t.Fatalf("Collect after the deadline = %+v, want pending/timeout", got)
	}
	select {
	case key := <-fb.cancelCalls:
		if key != "k-1" {
			t.Fatalf("Cancel(%q), want the request's idempotency key", key)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge.Cancel was never called on timeout")
	}
}

// #6439: an unreadable native result is never a decline.
func TestCollect_BadNativeResultIsPending(t *testing.T) {
	cases := map[string]string{
		"not json":        `approved`,
		"empty":           ``,
		"unknown status":  `{"status":"maybe"}`,
		"unknown field":   `{"status":"declined","pan":"4242424242424242"}`,
		"trailing data":   `{"status":"declined"} {"status":"approved"}`,
		"approved no txn": `{"status":"approved"}`,
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			register(t, &fakeBridge{reply: reply})
			got := Collect(deadlineCtx(t, time.Second), validRequest())
			if got.Status != StatusPending || got.Reason != "bad_native_result" {
				t.Fatalf("Collect with native reply %q = %+v, want pending/bad_native_result", reply, got)
			}
		})
	}
}

// The four ADR §3f statuses pass through as the native side reported them.
func TestCollect_PassesThroughKnownStatuses(t *testing.T) {
	for _, reply := range []string{
		`{"status":"declined","decline_code":"offline_pin_required"}`,
		`{"status":"cancelled","reason":"customer"}`,
		`{"status":"pending","reason":"outcome_unknown"}`,
	} {
		register(t, &fakeBridge{reply: reply})
		got := Collect(deadlineCtx(t, time.Second), validRequest())
		var want Result
		_ = json.Unmarshal([]byte(reply), &want)
		if got != want {
			t.Fatalf("Collect(%s) = %+v, want %+v", reply, got, want)
		}
	}
}

// The Go and Swift sides share one golden JSON shape (§4.2): a field renamed
// on either side fails here or in the Swift golden test.
func TestResultGoldenJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "result_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("golden result does not decode strictly: %v", err)
	}
	want := Result{Status: "approved", ProviderTxnID: "pi_123", ChargeID: "ch_123", CardBrand: "visa", Last4: "4242", DeclineCode: "", Reason: ""}
	if r != want {
		t.Fatalf("golden decoded to %+v, want %+v", r, want)
	}
	rawReq, err := os.ReadFile(filepath.Join("testdata", "request_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req Request
	dec = json.NewDecoder(bytes.NewReader(rawReq))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("golden request does not decode strictly: %v", err)
	}
	wantReq := Request{Provider: "stripe", AmountMinor: 1000, Currency: "gbp", IdempotencyKey: "sale-42-try-1", Description: "Sale 42", DeadlineUnixMs: 1763300000000}
	if req != wantReq {
		t.Fatalf("golden request decoded to %+v, want %+v", req, wantReq)
	}
}

func TestCapabilities(t *testing.T) {
	if got := Capabilities(); len(got) != 0 {
		t.Fatalf("Capabilities with no bridge = %v, want empty", got)
	}
	register(t, &fakeBridge{caps: `{"tap_to_pay":{"stripe":{"ready":false,"state":"needs_setup","reason":"terms_not_accepted","progress":0}}}`})
	got := Capabilities()
	want := ProviderCap{Ready: false, State: "needs_setup", Reason: "terms_not_accepted"}
	if len(got) != 1 || got["stripe"] != want {
		t.Fatalf("Capabilities = %+v, want stripe=%+v", got, want)
	}
	register(t, &fakeBridge{caps: `garbage`})
	if got := Capabilities(); len(got) != 0 {
		t.Fatalf("Capabilities with an unreadable report = %v, want empty", got)
	}
}

func TestSetupAndEducation(t *testing.T) {
	ctx := deadlineCtx(t, time.Second)
	if got := Setup(ctx, "stripe"); got.OK || got.Reason != "not_foreground" {
		t.Fatalf("Setup with no bridge = %+v", got)
	}
	fb := &fakeBridge{setupReply: `{"ok":true}`, eduReply: `{"ok":false,"reason":"ios_too_old"}`}
	register(t, fb)
	if got := Setup(ctx, "sumup"); got.OK || got.Reason != "bad_request" {
		t.Fatalf("Setup(sumup) = %+v, want bad_request", got)
	}
	if len(fb.setupCalls) != 0 {
		t.Fatal("Setup reached the bridge for an unknown provider")
	}
	if got := Setup(ctx, "stripe"); !got.OK {
		t.Fatalf("Setup = %+v, want ok", got)
	}
	if got := ShowEducation(ctx, "stripe"); got.OK || got.Reason != "ios_too_old" {
		t.Fatalf("ShowEducation = %+v, want ios_too_old", got)
	}
	fb.setupReply = `{"ok":true,"secret":"x"}`
	if got := Setup(ctx, "stripe"); got.OK || got.Reason != "bad_native_result" {
		t.Fatalf("Setup with an unknown field = %+v, want bad_native_result", got)
	}
}

func TestSetup_ContextEnds(t *testing.T) {
	release := make(chan struct{})
	register(t, &blockingSetup{release: release})
	t.Cleanup(func() { close(release) })
	got := Setup(deadlineCtx(t, 30*time.Millisecond), "stripe")
	if got.OK || got.Reason != "timeout" {
		t.Fatalf("Setup after the deadline = %+v, want timeout", got)
	}
}

type blockingSetup struct {
	fakeBridge
	release chan struct{}
}

func (b *blockingSetup) Setup(string) string { <-b.release; return `{"ok":true}` }

// Review (ut-docs#3049): a context that has already ended never starts a
// native collect. Otherwise Cancel(key) can reach the native side before
// the collect it is meant to stop, and the tap sheet opens for a payment
// the till has already given up on.
func TestCollect_EndedContextNeverReachesTheBridge(t *testing.T) {
	fb := &fakeBridge{reply: `{"status":"approved","provider_txn_id":"pi_1"}`}
	register(t, fb)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	cancel()
	got := Collect(ctx, validRequest())
	if got.Status != StatusPending || got.Reason != ReasonContextCanceled {
		t.Fatalf("Collect on an ended context = %+v, want pending/context_cancelled", got)
	}
	time.Sleep(20 * time.Millisecond) // a goroutine started in error would have run by now
	if n := fb.collectCount(); n != 0 {
		t.Fatalf("the bridge was asked to collect %d times on an ended context", n)
	}
}

// fakeResolver is a scripted native resolve_unknown side.
type fakeResolver struct {
	mu    sync.Mutex
	reply string
	keys  []string
	outs  []string
}

func (f *fakeResolver) Resolve(key, outcome string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	f.outs = append(f.outs, outcome)
	return f.reply
}

type actor bool

func (a actor) IsManager() bool { return bool(a) }

func registerResolver(t *testing.T, r Resolver) {
	t.Helper()
	SetResolver(r)
	t.Cleanup(func() { SetResolver(nil) })
}

// ut-docs#6533: resolve_unknown says "the earlier payment was NOT charged";
// only a manager may say it. A cashier is refused in Go, before the native
// side is reached, whatever the native side would do.
func TestResolveUnknown_CashierRefusedBeforeTheNativeSide(t *testing.T) {
	fr := &fakeResolver{reply: `{"ok":true}`}
	registerResolver(t, fr)
	got := Resolve(deadlineCtx(t, time.Second), actor(false), "stripe", "k1", OutcomeNotCharged)
	if got.OK || got.Reason != ReasonForbidden {
		t.Fatalf("cashier ResolveUnknown = %+v, want forbidden", got)
	}
	if got := Resolve(deadlineCtx(t, time.Second), nil, "stripe", "k1", OutcomeNotCharged); got.OK || got.Reason != ReasonForbidden {
		t.Fatalf("anonymous ResolveUnknown = %+v, want forbidden", got)
	}
	if len(fr.keys) != 0 {
		t.Fatalf("the native side was reached %d times for a refused caller", len(fr.keys))
	}
}

func TestResolveUnknown_ManagerReachesTheNativeSide(t *testing.T) {
	fr := &fakeResolver{reply: `{"ok":true}`}
	registerResolver(t, fr)
	got := Resolve(deadlineCtx(t, time.Second), actor(true), "stripe", "k1", OutcomeNotCharged)
	if !got.OK || len(fr.keys) != 1 || fr.keys[0] != "k1" {
		t.Fatalf("manager ResolveUnknown = %+v keys %v", got, fr.keys)
	}
}

func TestResolveUnknown_Validation(t *testing.T) {
	ctx := deadlineCtx(t, time.Second)
	if got := Resolve(ctx, actor(true), "stripe", "k1", OutcomeNotCharged); got.OK || got.Reason != ReasonNotForeground {
		t.Fatalf("no resolver = %+v, want not_foreground", got)
	}
	fr := &fakeResolver{reply: `{"ok":true}`}
	registerResolver(t, fr)
	if got := Resolve(ctx, actor(true), "sumup", "k1", OutcomeNotCharged); got.OK || got.Reason != ReasonBadRequest {
		t.Fatalf("unknown provider = %+v, want bad_request", got)
	}
	if got := Resolve(ctx, actor(true), "stripe", "", OutcomeNotCharged); got.OK || got.Reason != ReasonBadRequest {
		t.Fatalf("empty key = %+v, want bad_request", got)
	}
	fr.reply = `{"ok":true,"extra":1}`
	if got := Resolve(ctx, actor(true), "stripe", "k1", OutcomeNotCharged); got.OK || got.Reason != ReasonBadNativeResult {
		t.Fatalf("unreadable native reply = %+v, want bad_native_result", got)
	}
	if len(fr.keys) != 1 {
		t.Fatalf("validation failures reached the native side: %v", fr.keys)
	}
}

func TestResolveUnknown_ContextEnds(t *testing.T) {
	release := make(chan struct{})
	registerResolver(t, resolverFunc(func(string) string { <-release; return `{"ok":true}` }))
	t.Cleanup(func() { close(release) })
	got := Resolve(deadlineCtx(t, 30*time.Millisecond), actor(true), "stripe", "k1", OutcomeNotCharged)
	if got.OK || got.Reason != ReasonTimeout {
		t.Fatalf("ResolveUnknown after the deadline = %+v, want timeout", got)
	}
}

type resolverFunc func(string) string

func (f resolverFunc) Resolve(k, _ string) string { return f(k) }

// Resolve(key, outcome) carries the manager's answer to the native side;
func TestResolve_OutcomeReachesTheNativeSide(t *testing.T) {
	fr := &fakeResolver{reply: `{"ok":true}`}
	registerResolver(t, fr)
	ctx := deadlineCtx(t, time.Second)
	for _, out := range []string{OutcomeNotCharged, OutcomeCharged} {
		if got := Resolve(ctx, actor(true), "stripe", "k1", out); !got.OK {
			t.Fatalf("Resolve(%s) = %+v", out, got)
		}
	}
	if got := Resolve(ctx, actor(true), "stripe", "k2", OutcomeNotCharged); !got.OK {
		t.Fatalf("Resolve = %+v", got)
	}
	want := []string{"not_charged", "charged", "not_charged"}
	for i, w := range want {
		if fr.outs[i] != w {
			t.Fatalf("outcomes = %v, want %v", fr.outs, want)
		}
	}
}

func TestResolve_UnknownOutcomeAndCashierNeverReachNative(t *testing.T) {
	fr := &fakeResolver{reply: `{"ok":true}`}
	registerResolver(t, fr)
	ctx := deadlineCtx(t, time.Second)
	for _, out := range []string{"", "refunded", "NOT_CHARGED", "charged "} {
		if got := Resolve(ctx, actor(true), "stripe", "k1", out); got.OK || got.Reason != ReasonBadRequest {
			t.Fatalf("outcome %q = %+v, want bad_request", out, got)
		}
	}
	if got := Resolve(ctx, actor(false), "stripe", "k1", OutcomeCharged); got.OK || got.Reason != ReasonForbidden {
		t.Fatalf("cashier = %+v, want forbidden", got)
	}
	if got := Resolve(ctx, nil, "stripe", "k1", OutcomeCharged); got.OK || got.Reason != ReasonForbidden {
		t.Fatalf("no actor = %+v, want forbidden", got)
	}
	if len(fr.keys) != 0 {
		t.Fatalf("refused calls reached native: %v", fr.keys)
	}
}

// TestResolveHasOnlyTheManagerResolveCaller pins review item A3
// (ut-docs#6533/#3048): a wrong "not charged" answer can double-charge a
// customer, so the only production caller of Resolve is the manager resolve
// route of #6455 K1b, which holds the signed-in auth.User (what satisfies
// Actor) AFTER the manager PIN and the broker's own answer. A mobile.* method
// or any other route cannot know who is asking. Whoever adds a caller updates
// `allowed` below with the file and confirms it passes a real auth.User.
func TestResolveHasOnlyTheManagerResolveCaller(t *testing.T) {
	allowed := map[string]bool{"internal/pages/tap_pending.go": true} // slash-separated repo-relative paths
	root := filepath.Join("..", "..")
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "build", "vendor", "devicepay":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), "devicepay.Resolve(") {
			rel, _ := filepath.Rel(root, p)
			if rel = filepath.ToSlash(rel); !allowed[rel] {
				found = append(found, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("devicepay.Resolve is called from %v: add it to `allowed` only once the caller passes the signed-in auth.User (manager-gated, never an HTTP route)", found)
	}
}
