package paybroker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// counting starts a broker stand-in that records every request it gets.
func counting(t *testing.T, reply string) (*atomic.Int32, *[]string) {
	t.Helper()
	var hits atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, r.URL.Path+" "+string(b))
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	target(t, srv.URL)
	return &hits, &bodies
}

func withAttempt(t *testing.T, a AttemptView) {
	t.Helper()
	SetAttemptLookup(func(_ context.Context, key string) (AttemptView, bool) {
		if key == a.Key {
			return a, true
		}
		return AttemptView{}, false
	})
	t.Cleanup(func() { SetAttemptLookup(nil) })
}

// ADR-0198 §3.2, every cell.
func TestOpMatrixPerCaller(t *testing.T) {
	ops := []string{"connection_token", "onboarding_status", "session_create", "session_status", "session_cancel", "refund", "onboarding_start"}
	want := map[Caller]map[string]bool{
		CallerNative: {"connection_token": true, "onboarding_status": true, "session_create": true, "session_status": true, "session_cancel": true},
		CallerHostFn: {"onboarding_status": true, "session_status": true, "session_cancel": true, "refund": true},
		CallerCore:   {"connection_token": true, "onboarding_status": true, "session_create": true, "session_status": true, "session_cancel": true},
	}
	for c, row := range want {
		for _, op := range ops {
			if got := Allowed(c, "stripe", op); got != row[op] {
				t.Errorf("Allowed(%v, stripe, %s) = %v, want %v", c, op, got, row[op])
			}
		}
	}
	for _, op := range ops {
		if Allowed(Caller(0), "stripe", op) || Allowed(Caller(99), "stripe", op) {
			t.Errorf("an unknown caller must be allowed nothing (%s)", op)
		}
	}
	if Allowed(CallerCore, "sumup", "session_status") || Allowed(CallerNative, "", "connection_token") {
		t.Error("an unknown provider must be refused")
	}
}

// A modified shell must not be able to move money: no refund for it.
func TestNativeCannotRefund(t *testing.T) {
	hits, _ := counting(t, `{}`)
	_, err := Call(context.Background(), CallerNative, "stripe", "refund", []byte(`{"payment_intent_id":"pi_1","amount_minor":100}`))
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if _, err := Call(context.Background(), CallerCore, "stripe", "refund", []byte(`{}`)); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("core refund err = %v, want ErrNotAllowed", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("refused calls reached the cloud %d times", hits.Load())
	}
}

func TestHostFnNoDispatchNoRequest(t *testing.T) {
	hits, _ := counting(t, `{}`)
	for _, op := range []string{"session_status", "session_cancel", "refund"} {
		_, err := Call(context.Background(), CallerHostFn, "stripe", op, []byte(`{"payment_intent_id":"pi_1","amount_minor":5}`))
		if !errors.Is(err, ErrNoDispatch) {
			t.Fatalf("%s with no binding: err = %v, want ErrNoDispatch", op, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("unbound host fn calls reached the cloud %d times", hits.Load())
	}
}

// A binding of the wrong kind or provider is no binding.
func TestHostFnBindingKindAndProviderAreChecked(t *testing.T) {
	hits, _ := counting(t, `{}`)
	auth := WithBinding(context.Background(), Binding{Kind: BindAuthorize, Provider: "stripe", AttemptKey: "k1", PI: "pi_1"})
	if _, err := Call(auth, CallerHostFn, "stripe", "refund", []byte(`{"amount_minor":1}`)); !errors.Is(err, ErrNoDispatch) {
		t.Fatalf("refund under an authorize binding: err = %v", err)
	}
	ref := WithBinding(context.Background(), Binding{Kind: BindRefund, Provider: "stripe", PI: "pi_1", MaxRefundMinor: 500, RefundKey: "refund:s:t:1"})
	if _, err := Call(ref, CallerHostFn, "stripe", "session_cancel", []byte(`{}`)); !errors.Is(err, ErrNoDispatch) {
		t.Fatalf("cancel under a refund binding: err = %v", err)
	}
	other := WithBinding(context.Background(), Binding{Kind: BindAuthorize, Provider: "sumup", AttemptKey: "k1", PI: "pi_1"})
	if _, err := Call(other, CallerHostFn, "stripe", "session_status", []byte(`{}`)); !errors.Is(err, ErrNoDispatch) {
		t.Fatalf("status under another provider's binding: err = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests reached the cloud", hits.Load())
	}
}

func TestHostFnRefundCappedByTender(t *testing.T) {
	hits, bodies := counting(t, `{"refund_id":"re_1","status":"succeeded","amount_minor":300}`)
	ctx := WithBinding(context.Background(), Binding{Kind: BindRefund, Provider: "stripe", PI: "pi_9", MaxRefundMinor: 500, Currency: "gbp", RefundKey: "refund:sale1:tender2:1"})
	for _, body := range []string{`{"amount_minor":501}`, `{"amount_minor":0}`, `{"amount_minor":-5}`, `{}`, `not json`} {
		if _, err := Call(ctx, CallerHostFn, "stripe", "refund", []byte(body)); err == nil {
			t.Fatalf("refund body %q was accepted", body)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a bad amount reached the cloud %d times", hits.Load())
	}
	if _, err := Call(ctx, CallerHostFn, "stripe", "refund", []byte(`{"amount_minor":300,"refund_key":"plugin-chosen","note":"x"}`)); err != nil {
		t.Fatal(err)
	}
	// Core built the body: the plugin's key and extras are not forwarded.
	got := (*bodies)[0]
	for _, want := range []string{"/v1/payments/stripe/refund", `"payment_intent_id":"pi_9"`, `"refund_key":"refund:sale1:tender2:1"`, `"amount_minor":300`} {
		if !strings.Contains(got, want) {
			t.Fatalf("body %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "plugin-chosen") || strings.Contains(got, `"note"`) {
		t.Fatalf("plugin-supplied fields were forwarded: %q", got)
	}
}

// F2 (tester-batch): each of the three refund binding checks (kind, PI, key)
// alone must refuse the refund before anything reaches the cloud.
func TestHostFnRefundRequiresKindPIAndKey(t *testing.T) {
	hits, _ := counting(t, `{"refund_id":"re_1","status":"succeeded"}`)
	for name, b := range map[string]Binding{
		"authorize kind":   {Kind: BindAuthorize, Provider: "stripe", PI: "pi_9", MaxRefundMinor: 500, RefundKey: "k"},
		"empty PI":         {Kind: BindRefund, Provider: "stripe", PI: "", MaxRefundMinor: 500, RefundKey: "k"},
		"empty refund key": {Kind: BindRefund, Provider: "stripe", PI: "pi_9", MaxRefundMinor: 500, RefundKey: ""},
	} {
		ctx := WithBinding(context.Background(), b)
		if _, err := Call(ctx, CallerHostFn, "stripe", "refund", []byte(`{"amount_minor":10}`)); !errors.Is(err, ErrNoDispatch) {
			t.Fatalf("%s: err = %v, want ErrNoDispatch", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("an unbound refund reached the cloud %d times", hits.Load())
	}
}

func TestHostFnForeignPIRefused(t *testing.T) {
	hits, _ := counting(t, `{}`)
	ref := WithBinding(context.Background(), Binding{Kind: BindRefund, Provider: "stripe", PI: "pi_mine", MaxRefundMinor: 500, RefundKey: "k"})
	if _, err := Call(ref, CallerHostFn, "stripe", "refund", []byte(`{"payment_intent_id":"pi_other","amount_minor":10}`)); !errors.Is(err, ErrForeignPI) {
		t.Fatalf("refund of a foreign PI: err = %v, want ErrForeignPI", err)
	}
	auth := WithBinding(context.Background(), Binding{Kind: BindAuthorize, Provider: "stripe", AttemptKey: "k1", PI: "pi_mine"})
	for _, op := range []string{"session_status", "session_cancel"} {
		for _, body := range []string{`{"payment_intent_id":"pi_other"}`, `{"idempotency_key":"k2"}`, `{"attempt_key":"k2"}`} {
			if _, err := Call(auth, CallerHostFn, "stripe", op, []byte(body)); !errors.Is(err, ErrForeignPI) {
				t.Fatalf("%s %s: err = %v, want ErrForeignPI", op, body, err)
			}
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests reached the cloud", hits.Load())
	}
}

func TestHostFnStatusBodyComesFromTheBinding(t *testing.T) {
	_, bodies := counting(t, `{"status":"succeeded"}`)
	ctx := WithBinding(context.Background(), Binding{Kind: BindAuthorize, Provider: "stripe", AttemptKey: "att-1", PI: "pi_7"})
	if _, err := Call(ctx, CallerHostFn, "stripe", "session_status", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(ctx, CallerHostFn, "stripe", "session_cancel", nil); err != nil {
		t.Fatal(err)
	}
	if got := (*bodies)[0]; !strings.Contains(got, `"idempotency_key":"att-1"`) || !strings.Contains(got, `"payment_intent_id":"pi_7"`) {
		t.Fatalf("status body = %q", got)
	}
	if got := (*bodies)[1]; !strings.HasPrefix(got, "/v1/payments/stripe/session_cancel ") || !strings.Contains(got, `"payment_intent_id":"pi_7"`) {
		t.Fatalf("cancel body = %q", got)
	}
}

// Cancel with no PI to name must not send a body that names nothing.
func TestHostFnCancelNeedsAPI(t *testing.T) {
	hits, _ := counting(t, `{}`)
	ctx := WithBinding(context.Background(), Binding{Kind: BindAuthorize, Provider: "stripe", AttemptKey: "att-1"})
	if _, err := Call(ctx, CallerHostFn, "stripe", "session_cancel", nil); !errors.Is(err, ErrNoDispatch) {
		t.Fatalf("err = %v, want ErrNoDispatch", err)
	}
	if hits.Load() != 0 {
		t.Fatal("request sent")
	}
}

func TestOnboardingStatusNeedsNoBindingButBodyIsEmpty(t *testing.T) {
	_, bodies := counting(t, `{"onboarded":true}`)
	if _, err := Call(context.Background(), CallerHostFn, "stripe", "onboarding_status", []byte(`{"store_id":"other"}`)); err != nil {
		t.Fatal(err)
	}
	if got := (*bodies)[0]; got != "/v1/payments/stripe/onboarding_status {}" {
		t.Fatalf("got %q, want the plugin's body dropped", got)
	}
}

func TestCreateMustMatchAttempt(t *testing.T) {
	hits, _ := counting(t, `{"payment_intent_id":"pi_1"}`)
	create := func(body string) error {
		_, err := Call(context.Background(), CallerNative, "stripe", "session_create", []byte(body))
		return err
	}
	// No lookup wired: fail closed.
	if err := create(`{"idempotency_key":"k","amount_minor":500,"currency":"gbp"}`); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("no lookup: err = %v, want ErrNoAttempt", err)
	}
	withAttempt(t, AttemptView{Key: "k", Provider: "stripe", AmountMinor: 500, Currency: "GBP", Pending: true})
	for _, body := range []string{
		`{"idempotency_key":"k","amount_minor":501,"currency":"gbp"}`,
		`{"idempotency_key":"k","amount_minor":500,"currency":"eur"}`,
		`{"idempotency_key":"other","amount_minor":500,"currency":"gbp"}`,
		`{"amount_minor":500,"currency":"gbp"}`,
		`{"idempotency_key":"k","attempt_key":"x","amount_minor":500,"currency":"gbp"}`,
		`nope`,
	} {
		if err := create(body); !errors.Is(err, ErrNoAttempt) {
			t.Fatalf("create %s: err = %v, want ErrNoAttempt", body, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a mismatching create reached the cloud %d times", hits.Load())
	}
	if err := create(`{"idempotency_key":"k","amount_minor":500,"currency":"gbp","description":"x"}`); err != nil {
		t.Fatalf("matching create: %v", err)
	}
	if err := create(`{"attempt_key":"k","amount_minor":500,"currency":"GBP"}`); err != nil {
		t.Fatalf("matching create by attempt_key: %v", err)
	}
	// A settled attempt cannot create another PI.
	withAttempt(t, AttemptView{Key: "k", Provider: "stripe", AmountMinor: 500, Currency: "gbp", Pending: false})
	if err := create(`{"idempotency_key":"k","amount_minor":500,"currency":"gbp"}`); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("settled attempt: err = %v, want ErrNoAttempt", err)
	}
}

func TestResponseToWASMStripsSecrets(t *testing.T) {
	in := `{"payment_intent_id":"pi_1","client_secret":"pi_1_secret_X","secret":"pst_SECRET","status":"succeeded",
	"amount_minor":500,"amount_received_minor":500,"currency":"gbp","card":{"brand":"visa","last4":"4242","fingerprint":"fp","number":"4242424242424242"},
	"last_error_code":"","extra":{"client_secret":"deep"}}`
	out := string(FilterForWASM([]byte(in)))
	for _, bad := range []string{"client_secret", "pst_SECRET", "secret", "fingerprint", "4242424242424242", "extra", "deep"} {
		if strings.Contains(out, bad) {
			t.Fatalf("filtered answer still has %q: %s", bad, out)
		}
	}
	for _, want := range []string{`"id":"pi_1"`, `"status":"succeeded"`, `"amount_received_minor":500`, `"card_brand":"visa"`, `"last4":"4242"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("filtered answer lacks %q: %s", want, out)
		}
	}
	if got := string(FilterForWASM([]byte(`not json`))); got != `{}` {
		t.Fatalf("unparseable answer must filter to {}, got %q", got)
	}
}

// Through Call: a host fn caller gets the filtered answer, others the raw one.
func TestHostFnCallReturnsFilteredAnswer(t *testing.T) {
	counting(t, `{"status":"succeeded","client_secret":"pi_x_secret_Y","amount_minor":5}`)
	ctx := WithBinding(context.Background(), Binding{Kind: BindAuthorize, Provider: "stripe", AttemptKey: "k", PI: "pi_1"})
	out, err := Call(ctx, CallerHostFn, "stripe", "session_status", nil)
	if err != nil || strings.Contains(string(out), "secret") {
		t.Fatalf("host fn answer = %s, %v", out, err)
	}
	raw, err := Call(context.Background(), CallerCore, "stripe", "session_status", []byte(`{"idempotency_key":"k"}`))
	if err != nil || !strings.Contains(string(raw), "client_secret") {
		t.Fatalf("core answer must be unchanged, got %s, %v", raw, err)
	}
}
