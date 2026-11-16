package devicepay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSource struct {
	base, bearer string
	ok           bool
}

func (f fakeSource) Target(context.Context) (string, string, bool) { return f.base, f.bearer, f.ok }

// newCaller points a Caller at a TLS test server, trusting only its cert.
func newCaller(t *testing.T, h http.Handler) (*Caller, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	c := &Caller{
		Source: fakeSource{base: srv.URL + "/api", bearer: "dev-bearer-secret", ok: true},
		Client: srv.Client(),
	}
	return c, srv
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
	return e.Code
}

func TestCall_FourOpsMapToFixedEndpoints(t *testing.T) {
	cases := []struct{ op, method, path string }{
		{"onboarding_link", "POST", "/api/v1/payments/stripe/connect/onboarding-link"},
		{"connect_status", "GET", "/api/v1/payments/stripe/connect/status"},
		{"location", "POST", "/api/v1/payments/stripe/terminal/location"},
		{"connection_token", "POST", "/api/v1/payments/stripe/terminal/connection-token"},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			var gotM, gotP, gotAuth, gotBody string
			c, _ := newCaller(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				gotM, gotP, gotAuth, gotBody = r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)
				_, _ = w.Write([]byte(`{"secret":"abc"}`))
			}))
			out, err := c.Call(context.Background(), tc.op, `{"a":1}`)
			if err != nil {
				t.Fatal(err)
			}
			if out != `{"secret":"abc"}` {
				t.Fatalf("out=%q", out)
			}
			if gotM != tc.method || gotP != tc.path {
				t.Fatalf("got %s %s", gotM, gotP)
			}
			if gotAuth != "Bearer dev-bearer-secret" {
				t.Fatalf("auth=%q", gotAuth)
			}
			if tc.method == "POST" && gotBody != `{"a":1}` {
				t.Fatalf("body=%q", gotBody)
			}
			if tc.method == "GET" && gotBody != "" {
				t.Fatalf("GET carried a body %q", gotBody)
			}
		})
	}
}

func TestCall_UnknownOpRefusedBeforeAnyRequest(t *testing.T) {
	var hits atomic.Int32
	c, _ := newCaller(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	for _, op := range []string{"", "payment_intent", "../x", "location/../../x", "https://evil/x", "LOCATION"} {
		if code := codeOf(t, func() error { _, e := c.Call(context.Background(), op, "{}"); return e }()); code != CodeBadRequest {
			t.Fatalf("op %q: code %s", op, code)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("a refused op reached the network")
	}
}

func TestCall_NotEnrolledOrPlainHTTPRefused(t *testing.T) {
	c := &Caller{Source: fakeSource{ok: false}}
	if codeOf(t, func() error { _, e := c.Call(context.Background(), "location", "{}"); return e }()) != CodeOffline {
		t.Fatal("unenrolled should be offline/not linked")
	}
	c = &Caller{Source: fakeSource{base: "http://cloud.example/api", bearer: "b", ok: true}}
	if codeOf(t, func() error { _, e := c.Call(context.Background(), "location", "{}"); return e }()) != CodeCloudError {
		t.Fatal("plain http must be refused")
	}
	c = &Caller{Source: fakeSource{base: "https://cloud.example/api", bearer: "", ok: true}}
	if codeOf(t, func() error { _, e := c.Call(context.Background(), "location", "{}"); return e }()) != CodeOffline {
		t.Fatal("no bearer should refuse")
	}
}

func TestCall_StatusMapping(t *testing.T) {
	cases := map[int]string{
		402: CodeCloudError,
		403: CodeCloudError,
		409: CodeConnectIncomplete,
		500: CodeCloudError,
		404: CodeCloudError,
	}
	for status, want := range cases {
		status := status
		c, _ := newCaller(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"x"}`))
		}))
		_, err := c.Call(context.Background(), "connection_token", "{}")
		if got := codeOf(t, err); got != want {
			t.Fatalf("status %d: got %s want %s", status, got, want)
		}
	}
}

func TestCall_NoRedirects(t *testing.T) {
	var followed atomic.Int32
	c, srv := newCaller(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Add(1)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	_ = srv
	_, err := c.Call(context.Background(), "connection_token", "{}")
	if err == nil || followed.Load() != 0 {
		t.Fatalf("redirect followed=%d err=%v", followed.Load(), err)
	}
}

func TestCall_CapsAndNoBearerInErrors(t *testing.T) {
	c, _ := newCaller(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponse+10)))
	}))
	_, err := c.Call(context.Background(), "connection_token", "{}")
	if codeOf(t, err) != CodeCloudError {
		t.Fatalf("oversize response: %v", err)
	}
	if strings.Contains(err.Error(), "dev-bearer-secret") {
		t.Fatal("bearer leaked into error")
	}
	_, err = c.Call(context.Background(), "connection_token", strings.Repeat("y", maxRequest+1))
	if codeOf(t, err) != CodeBadRequest {
		t.Fatalf("oversize body: %v", err)
	}
}

func TestCall_TimeoutIsOffline(t *testing.T) {
	c, _ := newCaller(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	c.Timeout = 50 * time.Millisecond
	_, err := c.Call(context.Background(), "connection_token", "{}")
	if codeOf(t, err) != CodeOffline {
		t.Fatalf("err=%v", err)
	}
}

func TestCall_ConnectionErrorHidesBearer(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	client := srv.Client()
	base := srv.URL
	srv.Close() // now unreachable
	c := &Caller{Source: fakeSource{base: base, bearer: "dev-bearer-secret", ok: true}, Client: client}
	_, err := c.Call(context.Background(), "location", "{}")
	if codeOf(t, err) != CodeOffline || strings.Contains(err.Error(), "dev-bearer-secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestDefault_SetSourceAndRestore(t *testing.T) {
	if _, err := Call(context.Background(), "location", "{}"); codeOf(t, err) != CodeOffline {
		t.Fatalf("no source registered must read as not linked: %v", err)
	}
	restore := SetSource(fakeSource{ok: false})
	restore()
	if _, err := Call(context.Background(), "location", "{}"); codeOf(t, err) != CodeOffline {
		t.Fatalf("restore: %v", err)
	}
}
