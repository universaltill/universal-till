package netreach

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#3095: the status-bar light must say "No internet" when the till's
// network is up but the cloud host is not reachable. These pin the probe:
// non-blocking, cached with a TTL, single-flight, disabled for loopback.

// fakeClock is a settable Now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)} }

// waitSettled waits (without starting a probe) until no probe is in flight
// and the cached state is want.
func waitSettled(t *testing.T, m *Monitor, want State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, inFlight := m.snapshot()
		if !inFlight && st == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, inFlight := m.snapshot()
	t.Fatalf("state = %v (in flight %v), want %v", st, inFlight, want)
}

// testServer serves any path, counts /healthz hits and records paths.
func testServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64, *atomic.Value) {
	t.Helper()
	var hits atomic.Int64
	var path atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		path.Store(r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &path
}

// newTestMonitor points a Monitor at srv while treating its 127.0.0.1 host
// as a remote one (the loopback rule would otherwise disable it).
func newTestMonitor(t *testing.T, endpoint string, clk *fakeClock, client *http.Client) *Monitor {
	t.Helper()
	m := New(Options{Endpoint: endpoint, Client: client, Now: clk.Now, allowLoopback: true})
	if !m.Enabled() {
		t.Fatalf("monitor for %q is disabled", endpoint)
	}
	return m
}

func TestProbeURL(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"https://cloud.universaltill.com/api", "https://cloud.universaltill.com/healthz", true},
		{"https://cloud.universaltill.com/api/", "https://cloud.universaltill.com/healthz", true},
		{"http://cloud.example.com:8081/api?x=1#f", "http://cloud.example.com:8081/healthz", true},
		{"  https://cloud.example.com  ", "https://cloud.example.com/healthz", true},
		{"", "", false},
		{"   ", "", false},
		{"not a url", "", false},
		{"://bad", "", false},
		{"ftp://cloud.example.com/api", "", false},
		{"https:///api", "", false},
		{"http://localhost:8081/api", "", false},
		{"http://LOCALHOST/api", "", false},
		{"http://127.0.0.1:8081/api", "", false},
		{"http://127.1.2.3/api", "", false},
		{"http://[::1]:8081/api", "", false},
	} {
		got, ok := probeURL(c.in, false)
		if got != c.want || ok != c.ok {
			t.Errorf("probeURL(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestDefaults(t *testing.T) {
	if DefaultTimeout != 5*time.Second || DefaultTTL != 10*time.Second {
		t.Fatalf("defaults = timeout %v ttl %v, want 5s / 10s", DefaultTimeout, DefaultTTL)
	}
}

// recordingTransport fails the test if any request is made.
type recordingTransport struct{ calls atomic.Int64 }

func (rt *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	return nil, errors.New("no request expected")
}

func TestDisabledIsUnknownAndNeverProbes(t *testing.T) {
	for _, ep := range []string{"", "garbage ::", "ftp://x/api", "http://127.0.0.1:8081/api", "http://localhost/api", "http://[::1]/api"} {
		rt := &recordingTransport{}
		m := New(Options{Endpoint: ep, Client: &http.Client{Transport: rt}})
		if m.Enabled() {
			t.Errorf("%q: enabled, want disabled", ep)
		}
		for i := 0; i < 3; i++ {
			if st := m.Status(); st != Unknown {
				t.Errorf("%q: Status = %v, want Unknown", ep, st)
			}
		}
		time.Sleep(20 * time.Millisecond)
		if n := rt.calls.Load(); n != 0 {
			t.Errorf("%q: %d requests made, want none", ep, n)
		}
	}
	var nilM *Monitor
	if nilM.Status() != Unknown || nilM.Enabled() {
		t.Error("nil Monitor must be disabled and Unknown")
	}
}

func TestReachableAfterProbe(t *testing.T) {
	srv, hits, path := testServer(t, http.StatusOK)
	m := newTestMonitor(t, srv.URL+"/api", newClock(), srv.Client())
	if st := m.Status(); st != Unknown {
		t.Fatalf("first Status = %v, want Unknown (probe just started)", st)
	}
	waitSettled(t, m, Reachable)
	if m.Status() != Reachable {
		t.Fatal("Status after probe is not Reachable")
	}
	if hits.Load() != 1 || path.Load() != "/healthz" {
		t.Fatalf("hits %d path %v, want 1 at /healthz", hits.Load(), path.Load())
	}
}

func TestServerErrorStillReachable(t *testing.T) {
	srv, _, _ := testServer(t, http.StatusInternalServerError)
	m := newTestMonitor(t, srv.URL+"/api", newClock(), srv.Client())
	m.Status()
	waitSettled(t, m, Reachable)
}

func TestRefusedIsUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	m := newTestMonitor(t, "http://"+addr+"/api", newClock(), &http.Client{})
	m.Status()
	waitSettled(t, m, Unreachable)
}

func TestHangingServerNonBlockingThenUnreachable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	m := New(Options{Endpoint: srv.URL + "/api", Client: srv.Client(), Now: newClock().Now,
		Timeout: 150 * time.Millisecond, allowLoopback: true})
	start := time.Now()
	for i := 0; i < 5; i++ {
		if st := m.Status(); st != Unknown {
			t.Fatalf("Status = %v while the probe hangs, want Unknown", st)
		}
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("Status blocked for %v; it must never wait on the network", d)
	}
	waitSettled(t, m, Unreachable)
}

func TestTTLRespected(t *testing.T) {
	srv, hits, _ := testServer(t, http.StatusOK)
	clk := newClock()
	m := newTestMonitor(t, srv.URL+"/api", clk, srv.Client())
	m.Status()
	waitSettled(t, m, Reachable)
	clk.Add(9 * time.Second)
	for i := 0; i < 5; i++ {
		m.Status()
	}
	time.Sleep(30 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Fatalf("re-probed within the TTL: %d hits, want 1", n)
	}
	clk.Add(2 * time.Second) // 11 s since the probe
	if st := m.Status(); st != Reachable {
		t.Fatalf("stale Status = %v, want the cached Reachable while re-probing", st)
	}
	waitSettled(t, m, Reachable)
	if n := hits.Load(); n != 2 {
		t.Fatalf("after the TTL: %d hits, want 2", n)
	}
}

func TestSingleFlight(t *testing.T) {
	var hits atomic.Int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
	}))
	t.Cleanup(srv.Close)
	m := newTestMonitor(t, srv.URL+"/api", newClock(), srv.Client())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Status() }()
	}
	wg.Wait()
	// Let the one probe reach the server before releasing it.
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	waitSettled(t, m, Reachable)
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d probes for 50 concurrent Status calls, want 1", n)
	}
}

func TestStateJSON(t *testing.T) {
	if Unknown.Reachable() != nil {
		t.Error("Unknown must map to nil")
	}
	if p := Reachable.Reachable(); p == nil || !*p {
		t.Error("Reachable must map to true")
	}
	if p := Unreachable.Reachable(); p == nil || *p {
		t.Error("Unreachable must map to false")
	}
}

// snapshot reads the cached state and whether a probe runs, without
// starting one (test-only: production reads go through Status).
func (m *Monitor) snapshot() (State, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.inFlight
}
