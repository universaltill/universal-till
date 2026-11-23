package paybroker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func target(t *testing.T, base string) {
	t.Helper()
	SetTargetResolver(func(context.Context) (Target, bool) {
		return Target{BaseURL: base, Bearer: "dev-cred-1"}, true
	})
	t.Cleanup(func() { SetTargetResolver(nil) })
}

func TestCall_PostsWithBearerToTheOpPath(t *testing.T) {
	var gotPath, gotAuth, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s, want POST", r.Method)
		}
		gotPath, gotAuth, gotCT = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"secret":"pst_test_123"}`)
	}))
	defer srv.Close()
	target(t, srv.URL+"/api/")

	out, err := Call(context.Background(), CallerNative, "stripe", "connection_token", []byte(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(out) != `{"secret":"pst_test_123"}` {
		t.Fatalf("body = %q, want the cloud's response unchanged", out)
	}
	if gotPath != "/api/v1/payments/stripe/connection_token" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer dev-cred-1" {
		t.Fatalf("Authorization = %q, want the device credential", gotAuth)
	}
	if gotBody != `{}` || gotCT != "application/json" {
		t.Fatalf("body %q content-type %q", gotBody, gotCT)
	}
}

func TestCall_EmptyBodyIsEmptyObject(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	target(t, srv.URL)
	if _, err := Call(context.Background(), CallerNative, "stripe", "onboarding_status", nil); err != nil {
		t.Fatal(err)
	}
	if gotBody != `{}` {
		t.Fatalf("body = %q, want {}", gotBody)
	}
}

// The allow-list is the boundary: any other provider or op is refused
// before a request is made.
func TestCall_AllowListRefusesWithoutARequest(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	target(t, srv.URL)
	for _, c := range [][2]string{
		{"stripe", "payouts"},
		{"stripe", "../admin"},
		{"stripe", ""},
		{"sumup", "connection_token"},
		{"", "refund"},
	} {
		if _, err := Call(context.Background(), CallerNative, c[0], c[1], []byte(`{}`)); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("Call(%q,%q) err = %v, want ErrNotAllowed", c[0], c[1], err)
		}
	}
	if hits != 0 {
		t.Fatalf("refused calls reached the cloud %d times", hits)
	}
}

func TestCall_NotEnrolled(t *testing.T) {
	SetTargetResolver(nil)
	if _, err := Call(context.Background(), CallerNative, "stripe", "connection_token", nil); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("no resolver: err = %v, want ErrNotEnrolled", err)
	}
	SetTargetResolver(func(context.Context) (Target, bool) { return Target{}, false })
	t.Cleanup(func() { SetTargetResolver(nil) })
	if _, err := Call(context.Background(), CallerNative, "stripe", "connection_token", nil); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("unenrolled: err = %v, want ErrNotEnrolled", err)
	}
	SetTargetResolver(func(context.Context) (Target, bool) { return Target{BaseURL: "https://x"}, true })
	if _, err := Call(context.Background(), CallerNative, "stripe", "connection_token", nil); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("no bearer: err = %v, want ErrNotEnrolled", err)
	}
}

func TestCall_Offline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing listens there now
	target(t, base)
	if _, err := Call(context.Background(), CallerNative, "stripe", "connection_token", nil); !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v, want ErrOffline", err)
	}
}

func TestCall_HTTPErrorCarriesTheCloudCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"not_onboarded","message":"connect Stripe first"}}`)
	}))
	defer srv.Close()
	target(t, srv.URL)
	_, err := Call(context.Background(), CallerCore, "stripe", "session_create", []byte(`{}`))
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusConflict || he.Code != "not_onboarded" {
		t.Fatalf("err = %#v, want HTTPError{409, not_onboarded}", err)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `<html>bad gateway</html>`)
	}))
	defer srv2.Close()
	target(t, srv2.URL)
	_, err = Call(context.Background(), CallerCore, "stripe", "session_create", []byte(`{}`))
	if !errors.As(err, &he) || he.Status != http.StatusBadGateway || he.Code != "" {
		t.Fatalf("err = %#v, want HTTPError{502, \"\"}", err)
	}
}

func TestCall_ResponseCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"pad":"`+strings.Repeat("a", MaxResponseBytes)+`"}`)
	}))
	defer srv.Close()
	target(t, srv.URL)
	if _, err := Call(context.Background(), CallerNative, "stripe", "session_status", []byte(`{}`)); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}

	exact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", MaxResponseBytes-2)+`{}`)
	}))
	defer exact.Close()
	target(t, exact.URL)
	if _, err := Call(context.Background(), CallerNative, "stripe", "session_status", []byte(`{}`)); err != nil {
		t.Fatalf("a response of exactly the cap: %v", err)
	}
}

// Bodies carry the connection-token secret and the PaymentIntent client
// secret: neither may reach the log, at any level, on success or failure.
func TestCall_NeverLogsABody(t *testing.T) {
	var logs syncBuf
	restore := logging.CaptureForTest(&logs)
	defer restore()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/refund") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_request","detail":"SECRET-ERR-BODY"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"secret":"pst_SECRET-RESPONSE","client_secret":"pi_x_secret_SECRET2"}`)
	}))
	defer srv.Close()
	target(t, srv.URL)
	if _, err := Call(context.Background(), CallerCore, "stripe", "session_create", []byte(`{"description":"SECRET-REQUEST"}`)); err != nil {
		t.Fatal(err)
	}
	_, _ = Call(WithBinding(context.Background(), Binding{Kind: BindRefund, Provider: "stripe", PI: "pi_1", MaxRefundMinor: 100, RefundKey: "k"}), CallerHostFn, "stripe", "refund", []byte(`{"amount_minor":5}`))
	got := logs.String()
	for _, s := range []string{"SECRET-RESPONSE", "SECRET2", "SECRET-REQUEST", "SECRET-ERR-BODY", "dev-cred-1"} {
		if strings.Contains(got, s) {
			t.Fatalf("log contains %q:\n%s", s, got)
		}
	}
	if !strings.Contains(got, "stripe/session_create") {
		t.Fatalf("log should name provider/op; got:\n%s", got)
	}
}

// A redirect from the broker target must never be followed: the device
// credential would travel to wherever the (possibly hijacked) cloud points.
func TestCall_RedirectIsNotFollowedAndBearerNeverForwarded(t *testing.T) {
	var hits int
	var gotAuth string
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = io.WriteString(w, `{"leaked":true}`)
	}))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer first.Close()
	target(t, first.URL)

	out, err := Call(context.Background(), CallerNative, "stripe", "connection_token", []byte(`{}`))
	if err == nil {
		t.Fatalf("Call followed a redirect and returned %q, want an error", out)
	}
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusFound {
		t.Fatalf("err = %#v, want HTTPError{302}", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 || gotAuth != "" {
		t.Fatalf("redirect target got %d request(s), Authorization %q; want none", hits, gotAuth)
	}
}

// Review (ut-docs#3049): the device credential, the connection-token secret
// and the PaymentIntent client secret only ever travel over TLS. A plain
// http endpoint is refused before any request, except on loopback (tests,
// a developer's own machine).
func TestCall_PlainHTTPEndpointIsRefused(t *testing.T) {
	for _, base := range []string{"http://cloud.example.invalid/api", "ftp://cloud.example.invalid", "cloud.example.invalid/api", "://bad"} {
		target(t, base)
		if _, err := Call(context.Background(), CallerNative, "stripe", "connection_token", nil); !errors.Is(err, ErrInsecureEndpoint) {
			t.Fatalf("endpoint %q: err = %v, want ErrInsecureEndpoint", base, err)
		}
	}
}

// Review (ut-docs#3049): the request body is capped like the response, so
// the WASM host fn that will share this forwarder (#6455) cannot be used to
// push an arbitrarily large body at the cloud with the till's credential.
func TestCall_RequestCap(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	target(t, srv.URL)
	big := []byte(`{"description":"` + strings.Repeat("x", MaxRequestBytes) + `"}`)
	if _, err := Call(context.Background(), CallerCore, "stripe", "session_create", big); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("oversized body: err = %v, want ErrRequestTooLarge", err)
	}
	if hits != 0 {
		t.Fatalf("an oversized body reached the cloud %d times", hits)
	}
	ok := []byte(`{"description":"` + strings.Repeat("x", 1024) + `"}`)
	if _, err := Call(context.Background(), CallerCore, "stripe", "session_create", ok); err != nil {
		t.Fatalf("a 1 KiB body: err = %v, want nil", err)
	}
}

// ADR-0198 Amendment 2026-11-23 (D4): a refusal "device_role_unrecorded" is
// remembered for a short while so the till can say "set this till up again"
// instead of a generic start failure; any successful broker call clears it.
func TestCall_RoleUnrecordedRefusalIsRemembered(t *testing.T) {
	ResetRoleUnrecorded()
	t.Cleanup(ResetRoleUnrecorded)
	code, status := "device_role_unrecorded", http.StatusForbidden
	for _, c := range []string{"device_role_unconfirmed"} {
		code = c
		ResetRoleUnrecorded()
		srv0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"`+c+`"}}`)
		}))
		target(t, srv0.URL)
		_, _ = Call(context.Background(), CallerCore, "stripe", "session_create", []byte(`{}`))
		srv0.Close()
		if !RoleUnrecordedRecently() {
			t.Fatalf("%s refusal not remembered", c)
		}
	}
	code = "device_role_unrecorded"
	ResetRoleUnrecorded()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code == "" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"code":"`+code+`"}}`)
	}))
	defer srv.Close()
	target(t, srv.URL)
	if RoleUnrecordedRecently() {
		t.Fatal("flag set before any call")
	}
	_, _ = Call(context.Background(), CallerCore, "stripe", "session_create", []byte(`{}`))
	if !RoleUnrecordedRecently() {
		t.Fatal("device_role_unrecorded refusal not remembered")
	}
	code = ""
	if _, err := Call(context.Background(), CallerCore, "stripe", "onboarding_status", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if RoleUnrecordedRecently() {
		t.Fatal("a successful call must clear the flag")
	}
}
