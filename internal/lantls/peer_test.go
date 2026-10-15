package lantls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// peerTill is a main till on a same-port TLS + plain listener that counts
// what arrived over each.
type peerTill struct {
	base       string // plain base URL, what sync.primary_url holds
	cert       *Cert
	tls, plain atomic.Int32
}

func newPeerTill(t *testing.T) *peerTill {
	t.Helper()
	c, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &peerTill{base: "http://" + inner.Addr().String(), cert: c}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			p.tls.Add(1)
		} else {
			p.plain.Add(1)
		}
		fmt.Fprintf(w, "tls=%v", r.TLS != nil)
	})}
	go func() { _ = srv.Serve(Listen(inner, c.TLSConfig())) }()
	t.Cleanup(func() { _ = srv.Close() })
	return p
}

// usePeer points the shared peer transport at primary/pin for one test.
func usePeer(t *testing.T, primary, pin string) {
	t.Helper()
	t.Cleanup(SetPeerSource(func(context.Context) (string, string, error) { return primary, pin, nil }))
}

func peerGet(t *testing.T, url string) (string, error) {
	t.Helper()
	resp, err := NewPeerClient(0).Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestPeerClient_PinnedMainTillIsDialledOverTLS(t *testing.T) {
	main := newPeerTill(t)
	usePeer(t, main.base+"/", main.cert.Pin())

	got, err := peerGet(t, main.base+"/api/sync/ping")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tls=true" {
		t.Fatalf("pinned replica's request arrived as %q; want TLS", got)
	}
	if main.plain.Load() != 0 {
		t.Fatalf("main till saw %d plain request(s) from a pinned replica", main.plain.Load())
	}
	if !PeerPinned(t.Context()) {
		t.Fatal("PeerPinned = false with a pin held")
	}
}

func TestPeerClient_WrongCertificateIsRefusedWithNoPlainRetry(t *testing.T) {
	main := newPeerTill(t)
	usePeer(t, main.base, strings.Repeat("0", 64))

	_, err := peerGet(t, main.base+"/api/sync/ping")
	if !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("err = %v; want ErrPinMismatch", err)
	}
	if n := main.plain.Load() + main.tls.Load(); n != 0 {
		t.Fatalf("main till served %d request(s) despite the pin mismatch", n)
	}
}

func TestPeerClient_UnpinnedReplicaStaysPlain(t *testing.T) {
	main := newPeerTill(t)
	usePeer(t, main.base, "")

	got, err := peerGet(t, main.base+"/api/sync/ping")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tls=false" {
		t.Fatalf("unpinned replica's request arrived as %q; want plain", got)
	}
	if PeerPinned(t.Context()) {
		t.Fatal("PeerPinned = true with no pin held")
	}
}

func TestPeerClient_NoSourceIsPlain(t *testing.T) {
	t.Cleanup(SetPeerSource(nil))
	main := newPeerTill(t)

	got, err := peerGet(t, main.base+"/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tls=false" {
		t.Fatalf("got %q; want plain with no source configured", got)
	}
}

// The pin belongs to the main till only: any other host the same client
// reaches (the cloud, another till) is untouched.
func TestPeerClient_OtherHostsAreUntouched(t *testing.T) {
	main := newPeerTill(t)
	usePeer(t, main.base, main.cert.Pin())
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "tls=%v", r.TLS != nil)
	}))
	t.Cleanup(other.Close)

	got, err := peerGet(t, other.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tls=false" {
		t.Fatalf("another host got %q; want it left plain", got)
	}
}

// A connection verified under one pin is never reused once the held pin
// changes.
func TestPeerClient_PinChangeDropsVerifiedConnections(t *testing.T) {
	main := newPeerTill(t)
	pin := main.cert.Pin()
	t.Cleanup(SetPeerSource(func(context.Context) (string, string, error) { return main.base, pin, nil }))

	if got, err := peerGet(t, main.base+"/"); err != nil || got != "tls=true" {
		t.Fatalf("first request: %q, %v", got, err)
	}
	pin = strings.Repeat("f", 64)
	if _, err := peerGet(t, main.base+"/"); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("after the pin changed: err = %v; want ErrPinMismatch (an idle connection was reused)", err)
	}
}

// A main till address with no port is upgraded on the same port (80), not
// moved to 443.
func TestPeerUpgrade_KeepsThePort(t *testing.T) {
	for in, want := range map[string]string{
		"http://192.168.1.5:8080/api/sync/admin": "https://192.168.1.5:8080/api/sync/admin",
		"http://till.local/api/sync/admin":       "https://till.local:80/api/sync/admin",
		"https://192.168.1.5:8080/x":             "https://192.168.1.5:8080/x",
	} {
		req, err := http.NewRequest(http.MethodGet, in, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := upgradeToPeerTLS(req).URL.String(); got != want {
			t.Errorf("upgrade(%s) = %s; want %s", in, got, want)
		}
		if req.URL.String() != in {
			t.Errorf("upgrade mutated the caller's request: %s", req.URL)
		}
		if out := upgradeToPeerTLS(req); out.Host != "" && out.Host != req.URL.Host {
			t.Errorf("upgrade(%s) sends Host %q; want the original %q", in, out.Host, req.URL.Host)
		}
	}
}

func TestSamePeerHost(t *testing.T) {
	cases := []struct {
		reqURL, primary string
		want            bool
	}{
		{"http://10.0.0.2:8080/api/sync/admin", "http://10.0.0.2:8080", true},
		{"http://10.0.0.2:8080/x", "http://10.0.0.2:8080/", true},
		{"https://10.0.0.2:8080/x", "http://10.0.0.2:8080", true},
		{"http://TILL.local:8080/x", "http://till.local:8080", true},
		{"http://till.local/x", "http://till.local:80", true},
		{"http://10.0.0.2:8081/x", "http://10.0.0.2:8080", false},
		{"http://10.0.0.3:8080/x", "http://10.0.0.2:8080", false},
		{"https://cloud.example/x", "http://10.0.0.2:8080", false},
		{"http://10.0.0.2:8080/x", "", false},
		{"http://10.0.0.2:8080/x", "::not a url", false},
	}
	for _, c := range cases {
		req, err := http.NewRequest(http.MethodGet, c.reqURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := samePeerHost(req.URL, c.primary); got != c.want {
			t.Errorf("samePeerHost(%s, %q) = %v; want %v", c.reqURL, c.primary, got, c.want)
		}
	}
}

// An unreadable pin is never read as "not pinned": the request fails, and
// nothing (least of all a bearer) goes out over plain HTTP.
func TestPeerClient_UnreadablePinFailsClosed(t *testing.T) {
	main := newPeerTill(t)
	t.Cleanup(SetPeerSource(func(context.Context) (string, string, error) {
		return "", "", errors.New("database is locked")
	}))

	if _, err := peerGet(t, main.base+"/api/sync/admin"); err == nil {
		t.Fatal("request went out with the pin unreadable")
	}
	if n := main.plain.Load() + main.tls.Load(); n != 0 {
		t.Fatalf("main till served %d request(s) with the pin unreadable", n)
	}
	if PeerPinned(t.Context()) {
		t.Fatal("PeerPinned = true with the pin unreadable")
	}
}

// usePeerVar points the peer transport at primary and *pin, read per request,
// so a re-proof hook can change the held pin mid-test.
func usePeerVar(t *testing.T, primary string, pin *atomic.Pointer[string]) {
	t.Helper()
	t.Cleanup(SetPeerSource(func(context.Context) (string, string, error) { return primary, *pin.Load(), nil }))
}

// ADR-0114 §7 (ut-docs#4410): a certificate matching no held pin triggers a
// re-proof; a valid one adopts the new pin and the request is retried at
// once, over TLS, with the bearer only ever sent on the verified retry.
func TestPeerClient_ReproofAdoptsNewPinAndRetries(t *testing.T) {
	main := newPeerTill(t)
	old := strings.Repeat("0", 64)
	var pin atomic.Pointer[string]
	pin.Store(&old)
	usePeerVar(t, main.base, &pin)
	var calls atomic.Int32
	var gotPrimary, gotHeld string
	t.Cleanup(SetPeerReproof(func(_ context.Context, primaryURL, heldPin string) error {
		calls.Add(1)
		gotPrimary, gotHeld = primaryURL, heldPin
		p := main.cert.Pin()
		pin.Store(&p)
		return nil
	}))

	req, err := http.NewRequest(http.MethodPost, main.base+"/api/sync/push", strings.NewReader("journal"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := NewPeerClient(0).Do(req)
	if err != nil {
		t.Fatalf("request after a valid re-proof: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "tls=true" {
		t.Fatalf("retried request arrived as %q; want TLS", body)
	}
	if calls.Load() != 1 || gotPrimary != main.base || gotHeld != old {
		t.Fatalf("re-proof calls=%d primary=%q held=%q; want 1, %q, the old pin", calls.Load(), gotPrimary, gotHeld, main.base)
	}
	if main.tls.Load() != 1 || main.plain.Load() != 0 {
		t.Fatalf("main till served tls=%d plain=%d; want exactly the one verified retry", main.tls.Load(), main.plain.Load())
	}
}

// The retried request carries its body again (GetBody), not an empty one.
func TestPeerClient_ReproofRetryResendsTheBody(t *testing.T) {
	c, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var got atomic.Pointer[string]
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		got.Store(&s)
	})}
	go func() { _ = srv.Serve(Listen(inner, c.TLSConfig())) }()
	t.Cleanup(func() { _ = srv.Close() })
	base := "http://" + inner.Addr().String()
	old := strings.Repeat("0", 64)
	var pin atomic.Pointer[string]
	pin.Store(&old)
	usePeerVar(t, base, &pin)
	t.Cleanup(SetPeerReproof(func(context.Context, string, string) error {
		p := c.Pin()
		pin.Store(&p)
		return nil
	}))

	resp, err := NewPeerClient(0).Post(base+"/api/sync/push", "application/json", strings.NewReader(`{"n":1}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if g := got.Load(); g == nil || *g != `{"n":1}` {
		t.Fatalf("retried body = %v; want the original", g)
	}
}

// A failed re-proof keeps the old pin: the request fails exactly as a
// mismatch did before (unreachable → banner → re-discovery), and the device
// presenting the certificate never sees a request.
func TestPeerClient_FailedReproofKeepsTheMismatch(t *testing.T) {
	main := newPeerTill(t)
	usePeer(t, main.base, strings.Repeat("0", 64))
	t.Cleanup(SetPeerReproof(func(context.Context, string, string) error {
		return errors.New("proof does not match this till's pairing")
	}))

	if _, err := peerGet(t, main.base+"/api/sync/ping"); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("err = %v; want ErrPinMismatch", err)
	}
	if n := main.plain.Load() + main.tls.Load(); n != 0 {
		t.Fatalf("main till served %d request(s) after a failed re-proof", n)
	}
}

// A re-proof that reports success but leaves the held pin unchanged is not
// retried: the retry would only mismatch again.
func TestPeerClient_ReproofWithoutNewPinIsNotRetried(t *testing.T) {
	main := newPeerTill(t)
	usePeer(t, main.base, strings.Repeat("0", 64))
	var calls atomic.Int32
	t.Cleanup(SetPeerReproof(func(context.Context, string, string) error {
		calls.Add(1)
		return nil
	}))

	if _, err := peerGet(t, main.base+"/"); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("err = %v; want ErrPinMismatch", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("re-proof ran %d times; want 1", calls.Load())
	}
}

// A body that can't be replayed is not retried; the next request uses the
// adopted pin.
func TestPeerClient_ReproofDoesNotReplayAnUnrewindableBody(t *testing.T) {
	main := newPeerTill(t)
	old := strings.Repeat("0", 64)
	var pin atomic.Pointer[string]
	pin.Store(&old)
	usePeerVar(t, main.base, &pin)
	t.Cleanup(SetPeerReproof(func(context.Context, string, string) error {
		p := main.cert.Pin()
		pin.Store(&p)
		return nil
	}))

	req, err := http.NewRequest(http.MethodPost, main.base+"/", io.NopCloser(strings.NewReader("x")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPeerClient(0).Do(req); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("err = %v; want the original ErrPinMismatch", err)
	}
	if got, err := peerGet(t, main.base+"/"); err != nil || got != "tls=true" {
		t.Fatalf("next request: %q, %v; want TLS under the adopted pin", got, err)
	}
}
