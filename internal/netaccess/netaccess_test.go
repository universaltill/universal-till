package netaccess

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// demoOn turns the process-wide demo switch on for one test and restores
// the previous value afterwards. Tests in this package that flip it are not
// parallel: the switch is process-wide by design (see SetDemo).
func demoOn(t *testing.T) {
	t.Helper()
	prev := Demo()
	SetDemo(true)
	t.Cleanup(func() { SetDemo(prev) })
}

func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// Demo off: the client is an ordinary client with the given timeout — the
// request reaches the server and the timeout is carried through unchanged.
func TestNewClient_DemoOffBehavesLikeAPlainClient(t *testing.T) {
	SetDemo(false)
	srv, hits := countingServer(t)
	c := NewClient(3 * time.Second)
	if c.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %v, want 3s", c.Timeout)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get with demo off: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("status %d, hits %d; want 200 and exactly one hit", resp.StatusCode, hits.Load())
	}
}

// Demo on: every request fails with ErrDemoDenied and never reaches the
// server — including one made on a client built BEFORE demo was switched
// on (package-level clients are built at init, before config is read).
func TestNewClient_DemoOnDeniesEveryRequest(t *testing.T) {
	SetDemo(false)
	srv, hits := countingServer(t)
	early := NewClient(0) // built before the switch flips, like a package-level var
	demoOn(t)
	late := NewClient(time.Second)
	for name, c := range map[string]*http.Client{"built before demo": early, "built after demo": late} {
		_, err := c.Get(srv.URL)
		if !errors.Is(err, ErrDemoDenied) {
			t.Errorf("%s: err = %v, want ErrDemoDenied", name, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server saw %d request(s) in demo mode, want 0", n)
	}
}

// A caller's own transport (custom TLS, pooling) is used when demo is off
// and bypassed entirely when it is on.
func TestNewClientWithTransport_UsesBaseOffAndDeniesOn(t *testing.T) {
	SetDemo(false)
	var baseCalls atomic.Int32
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		baseCalls.Add(1)
		return &http.Response{StatusCode: http.StatusTeapot, Body: http.NoBody, Request: r}, nil
	})
	c := NewClientWithTransport(time.Second, base)
	resp, err := c.Get("http://example.invalid/")
	if err != nil || resp.StatusCode != http.StatusTeapot {
		t.Fatalf("demo off: resp=%v err=%v, want the base transport's 418", resp, err)
	}
	demoOn(t)
	if _, err := c.Get("http://example.invalid/"); !errors.Is(err, ErrDemoDenied) {
		t.Fatalf("demo on: err = %v, want ErrDemoDenied", err)
	}
	if n := baseCalls.Load(); n != 1 {
		t.Fatalf("base transport called %d times, want 1 (demo-on request must not reach it)", n)
	}
}

// CloseIdleConnections still reaches the underlying transport, as it would
// on a plain client.
func TestNewClientWithTransport_ForwardsCloseIdleConnections(t *testing.T) {
	base := &closeIdleRecorder{}
	NewClientWithTransport(0, base).CloseIdleConnections()
	if !base.closed {
		t.Fatal("CloseIdleConnections did not reach the base transport")
	}
}

func TestDialContext_DemoOffDialsDemoOnDenies(t *testing.T) {
	SetDemo(false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepts atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			c.Close()
		}
	}()
	dial := DialContext(time.Second, 0)
	conn, err := dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("demo off dial: %v", err)
	}
	conn.Close()

	demoOn(t)
	if _, err := dial(context.Background(), "tcp", ln.Addr().String()); !errors.Is(err, ErrDemoDenied) {
		t.Fatalf("demo on dial: err = %v, want ErrDemoDenied", err)
	}
	// Give a stray connection a moment to be accepted before counting.
	time.Sleep(50 * time.Millisecond)
	if n := accepts.Load(); n != 1 {
		t.Fatalf("listener accepted %d connection(s), want exactly 1 (the demo-off dial)", n)
	}
}

func TestStartService_RunsWhenDemoOffSkipsWhenOn(t *testing.T) {
	var seen []string
	restore := ObserveServices(func(name string, started bool) {
		if started {
			seen = append(seen, "started:"+name)
		} else {
			seen = append(seen, "skipped:"+name)
		}
	})
	defer restore()

	SetDemo(false)
	ran := 0
	if !StartService("svc-a", func() { ran++ }) {
		t.Fatal("StartService reported not started with demo off")
	}
	demoOn(t)
	if StartService("svc-b", func() { ran++ }) {
		t.Fatal("StartService reported started with demo on")
	}
	if ran != 1 {
		t.Fatalf("start ran %d times, want 1 (only the demo-off call)", ran)
	}
	want := []string{"started:svc-a", "skipped:svc-b"}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("observer saw %v, want %v", seen, want)
	}
}

func TestObserveServices_RestoreRemovesObserver(t *testing.T) {
	SetDemo(false)
	calls := 0
	restore := ObserveServices(func(string, bool) { calls++ })
	restore()
	StartService("svc", func() {})
	if calls != 0 {
		t.Fatalf("observer called %d times after restore, want 0", calls)
	}
}

// BaseTransport sees through the guard so a test can still pin the knobs of
// the transport a caller supplied (cloudsync's idle timeout, marketplace's
// TLS config) — and reports http.DefaultTransport for a client built
// without one.
func TestBaseTransport(t *testing.T) {
	custom := &http.Transport{}
	if got := BaseTransport(NewClientWithTransport(0, custom).Transport); got != custom {
		t.Fatalf("BaseTransport = %#v, want the caller's transport", got)
	}
	if got := BaseTransport(NewClient(0).Transport); got != http.DefaultTransport {
		t.Fatalf("BaseTransport of NewClient = %#v, want http.DefaultTransport", got)
	}
	if got := BaseTransport(custom); got != custom {
		t.Fatalf("BaseTransport of a plain transport = %#v, want it unchanged", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeIdleRecorder struct{ closed bool }

func (c *closeIdleRecorder) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}
func (c *closeIdleRecorder) CloseIdleConnections() { c.closed = true }
