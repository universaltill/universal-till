package lantls

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func testCert(t *testing.T) *Cert {
	t.Helper()
	c, err := loadOrCreate(filepath.Join(t.TempDir(), "tls"), t0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// servePinned serves on a sniffing listener with ConnContext wired the way
// the server wires it, and echoes the pin the handler sees.
func servePinned(t *testing.T, c *Cert) string {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "pin=%s", ServedPin(r.Context()))
		}),
		ConnContext: ConnContext(c),
	}
	go func() { _ = srv.Serve(Listen(inner, c.TLSConfig())) }()
	t.Cleanup(func() { _ = srv.Close() })
	return inner.Addr().String()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestPin_IsPinOfTheServedLeaf(t *testing.T) {
	c := testCert(t)
	if c.Pin() == "" || c.Pin() != PinOf(c.leaf) {
		t.Fatalf("Pin() = %q, PinOf(leaf) = %q", c.Pin(), PinOf(c.leaf))
	}
}

func TestServedPin_OnlyOnTLSConnections(t *testing.T) {
	c := testCert(t)
	addr := servePinned(t, c)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != "pin=" {
		t.Errorf("plain HTTP: %q, want no served pin", got)
	}

	pc := NewPinnedClient(0, "")
	resp, err = pc.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := body(t, resp), "pin="+c.Pin(); got != want {
		t.Errorf("TLS: %q, want %q", got, want)
	}
	if pc.Pin() != c.Pin() {
		t.Errorf("client saw pin %q, want %q", pc.Pin(), c.Pin())
	}
}

func TestServedPin_NilCertStampsNothing(t *testing.T) {
	ctx := ConnContext(nil)(context.Background(), tls.Server(nil, &tls.Config{}))
	if got := ServedPin(ctx); got != "" {
		t.Errorf("nil cert: served pin %q, want none", got)
	}
}

func TestPinnedClient_EnforcesExpectedPin(t *testing.T) {
	c := testCert(t)
	addr := servePinned(t, c)

	ok := NewPinnedClient(0, c.Pin())
	resp, err := ok.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("right pin refused: %v", err)
	}
	resp.Body.Close()

	other := testCert(t) // a MITM's certificate: different key, different pin
	bad := NewPinnedClient(0, other.Pin())
	_, err = bad.Get("https://" + addr + "/")
	if !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("wrong pin: err = %v, want ErrPinMismatch", err)
	}
	if bad.Pin() != "" {
		t.Errorf("a refused certificate's pin was recorded: %q", bad.Pin())
	}
}

func TestPinnedClient_PlainHTTPServerIsAnError(t *testing.T) {
	// An older main till (no TLS): the caller falls back to plain HTTP.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	pc := NewPinnedClient(0, "")
	if _, err := pc.Get("https://" + srv.Listener.Addr().String() + "/"); err == nil {
		t.Fatal("TLS to a plain-HTTP server succeeded")
	}
	if pc.Pin() != "" {
		t.Errorf("pin %q recorded without a handshake", pc.Pin())
	}
}

func TestHTTPSBase(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"http://192.168.1.5:8080", "https://192.168.1.5:8080", true},
		{"http://till.local:8080/", "https://till.local:8080", true},
		{"https://192.168.1.5:8080", "", false}, // already TLS: not our LAN upgrade
		{"ftp://x", "", false},
		{"::", "", false},
	} {
		got, ok := HTTPSBase(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("HTTPSBase(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestExchange_PlainOKOnlyWhenTheTillTookNoRequest(t *testing.T) {
	newReq := func(url string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}

	// A till without TLS answers the ClientHello as a bad HTTP request:
	// connected, nothing written — plain HTTP may be tried.
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer plain.Close()
	if _, ok, err := NewPinnedClient(0, "").Exchange(newReq("https://" + plain.Listener.Addr().String() + "/")); err == nil || !ok {
		t.Errorf("plain-HTTP till: err=%v plainOK=%v, want an error with plainOK", err, ok)
	}

	// Nobody listening: a plain retry would only fail the same way.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()
	if _, ok, err := NewPinnedClient(0, "").Exchange(newReq("https://" + dead + "/")); err == nil || ok {
		t.Errorf("dead address: err=%v plainOK=%v, want no fallback", err, ok)
	}

	// The request reached the till and the answer was lost: a plain retry
	// would submit it twice.
	c := testCert(t)
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release })}
	go func() { _ = srv.Serve(Listen(inner, c.TLSConfig())) }()
	defer func() { close(release); _ = srv.Close() }()
	if _, ok, err := NewPinnedClient(500*time.Millisecond, "").Exchange(newReq("https://" + inner.Addr().String() + "/")); err == nil || ok {
		t.Errorf("request written, answer lost: err=%v plainOK=%v, want no fallback", err, ok)
	}

	// A certificate that isn't the pinned one is never "no TLS here".
	addr := servePinned(t, c)
	if _, ok, err := NewPinnedClient(0, testCert(t).Pin()).Exchange(newReq("https://" + addr + "/")); !errors.Is(err, ErrPinMismatch) || ok {
		t.Errorf("pin mismatch: err=%v plainOK=%v, want ErrPinMismatch without fallback", err, ok)
	}
}

func TestPinnedClient_DoesNotFollowRedirects(t *testing.T) {
	c := testCert(t)
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.RedirectHandler("http://192.0.2.1/elsewhere", http.StatusFound)}
	go func() { _ = srv.Serve(Listen(inner, c.TLSConfig())) }()
	t.Cleanup(func() { _ = srv.Close() })
	resp, err := NewPinnedClient(0, c.Pin()).Get("https://" + inner.Addr().String() + "/?secret=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d: the redirect was followed", resp.StatusCode)
	}
}
