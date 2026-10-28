package netreach

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"syscall"
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
		got, ok := probeURL(c.in, healthPath, false)
		if got != c.want || ok != c.ok {
			t.Errorf("probeURL(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestDefaults(t *testing.T) {
	if DefaultTimeout != 5*time.Second || DefaultTTL != 60*time.Second {
		t.Fatalf("defaults = timeout %v ttl %v, want 5s / 60s (ADR-0148 audit item 2)", DefaultTimeout, DefaultTTL)
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
	// Port 1 on loopback is refused at once; a closed ephemeral port can be
	// reused by a later listener in the same binary (ut-docs#5080).
	const addr = "127.0.0.1:1"
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
	// The status bar polls every 10 s; none of the polls inside the 60 s
	// TTL may reach the cloud (ADR-0148 audit item 2, ut-docs#3625).
	for i := 0; i < 5; i++ {
		clk.Add(10 * time.Second)
		m.Status()
	}
	clk.Add(DefaultTTL - 51*time.Second) // 59 s since the probe
	m.Status()
	time.Sleep(30 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Fatalf("re-probed within the TTL: %d hits, want 1", n)
	}
	clk.Add(2 * time.Second) // 61 s since the probe
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

// panickingTransport panics on every request, standing in for any bug deep
// in the probe's HTTP path.
type panickingTransport struct{ calls atomic.Int64 }

func (p *panickingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls.Add(1)
	panic("probe exploded")
}

// TestPanickingProbeStillClearsInFlight pins ut-docs#3304's review finding:
// the probe goroutine recovers a panic (logging.RecoverAndLog), and the
// outcome is recorded in a defer, so inFlight is cleared and a later Status
// past the TTL starts a fresh probe instead of the light freezing forever.
func TestPanickingProbeStillClearsInFlight(t *testing.T) {
	tr := &panickingTransport{}
	clk := newClock()
	m := newTestMonitor(t, "https://cloud.example.test/api", clk, &http.Client{Transport: tr})
	m.Status()
	waitSettled(t, m, Unreachable)
	clk.Add(DefaultTTL + time.Second)
	m.Status()
	deadline := time.Now().Add(5 * time.Second)
	for tr.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := tr.calls.Load(); n != 2 {
		t.Fatalf("probes after a panicking one: %d, want 2 (inFlight stuck?)", n)
	}
	waitSettled(t, m, Unreachable)
}

// errTransport fails every request, counting calls and recording the path.
type errTransport struct {
	calls atomic.Int64
	path  atomic.Value
}

func (e *errTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	e.path.Store(r.URL.Path)
	return nil, errors.New("no route to host")
}

// ut-docs#3137: Fresh is Status for decisions — Unknown unless the last
// completed probe is younger than the TTL.
func TestFreshUnknownUntilProbedThenStaleAgain(t *testing.T) {
	tr := &errTransport{}
	clk := newClock()
	m := newTestMonitor(t, "https://signer.example.test", clk, &http.Client{Transport: tr})
	if st := m.Fresh(); st != Unknown {
		t.Fatalf("first Fresh = %v, want Unknown (probe just started)", st)
	}
	waitSettled(t, m, Unreachable)
	if st := m.Fresh(); st != Unreachable {
		t.Fatalf("Fresh within TTL = %v, want Unreachable", st)
	}
	if m.Status() != Unreachable {
		t.Fatal("Status within TTL must agree")
	}
	clk.Add(DefaultTTL + time.Second)
	if st := m.Fresh(); st != Unknown {
		t.Fatalf("Fresh past TTL = %v, want Unknown", st)
	}
	// ... and Status keeps showing the last value for the status bar.
	waitSettled(t, m, Unreachable)
	if n := tr.calls.Load(); n != 2 {
		t.Fatalf("probes = %d, want 2 (stale Fresh kicks a refresh)", n)
	}
}

func TestFreshDisabledIsUnknown(t *testing.T) {
	var m *Monitor
	if m.Fresh() != Unknown {
		t.Fatal("nil Monitor Fresh must be Unknown")
	}
	if New(Options{Endpoint: "http://127.0.0.1:9"}).Fresh() != Unknown {
		t.Fatal("loopback Monitor Fresh must be Unknown")
	}
}

func TestOptionsPathProbesRoot(t *testing.T) {
	srv, _, path := testServer(t, http.StatusNotFound)
	m := New(Options{Endpoint: srv.URL + "/some/api", Path: "/", Client: srv.Client(), Now: newClock().Now, allowLoopback: true})
	m.Fresh()
	waitSettled(t, m, Reachable)
	if path.Load() != "/" {
		t.Fatalf("probed path %v, want /", path.Load())
	}
}

func TestSetCachesPerEndpointAndCaps(t *testing.T) {
	s := NewSet(SetOptions{Max: 2, Now: newClock().Now, Client: &http.Client{Transport: &errTransport{}}})
	a := s.For("https://a.example.test")
	if a == nil || s.For("https://a.example.test") != a {
		t.Fatal("same endpoint must return the cached Monitor")
	}
	b := s.For("https://b.example.test")
	if b == nil || b == a {
		t.Fatal("distinct endpoint must get its own Monitor")
	}
	if c := s.For("https://c.example.test"); c != nil {
		t.Fatal("beyond Max must be nil (disabled)")
	}
	if s.For("https://a.example.test") != a {
		t.Fatal("cached endpoint must stay reachable once the cap is hit")
	}
	if (*Monitor)(nil).Fresh() != Unknown {
		t.Fatal("nil Monitor from a full Set must read Unknown")
	}
}

func TestNilSetIsDisabledAndLoopbackRulesKept(t *testing.T) {
	var s *Set
	if s.For("https://a.example.test") != nil {
		t.Fatal("nil Set must return nil")
	}
	if m := NewSet(SetOptions{}).For("http://localhost:8080"); m.Enabled() {
		t.Fatal("loopback endpoint must stay disabled in a Set")
	}
}

func TestSetMonitorsProbeConfiguredPath(t *testing.T) {
	tr := &errTransport{}
	s := NewSet(SetOptions{Path: "/", Now: newClock().Now, Client: &http.Client{Transport: tr}})
	m := s.For("https://signer.example.test/ignored/path")
	m.Fresh()
	waitSettled(t, m, Unreachable)
	if tr.path.Load() != "/" {
		t.Fatalf("probed path %v, want /", tr.path.Load())
	}
}

func TestSetConcurrentFor(t *testing.T) {
	s := NewSet(SetOptions{Client: &http.Client{Transport: &errTransport{}}})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.For("https://a.example.test") }()
	}
	wg.Wait()
	if len(s.mons) != 1 {
		t.Fatalf("monitors = %d, want 1", len(s.mons))
	}
}

// ut-docs#3137 review, major 2: Refresh re-probes a held host once its
// result is stale, so a quiet till's next sale reads a fresh state rather
// than Unknown; a fresh result is left alone.
func TestSetRefreshReprobesStaleMonitors(t *testing.T) {
	clk := newClock()
	tr := &errTransport{}
	s := NewSet(SetOptions{TTL: 30 * time.Second, Now: clk.Now, Client: &http.Client{Transport: tr}})
	m := s.For("https://signer.example.test")
	m.Fresh()
	waitSettled(t, m, Unreachable)
	s.Refresh()
	waitSettled(t, m, Unreachable)
	if n := tr.calls.Load(); n != 1 {
		t.Fatalf("fresh result re-probed: %d calls, want 1", n)
	}
	clk.Add(31 * time.Second)
	// Fresh reads Unknown when stale, and kicks one probe itself (call 2).
	if m.Fresh() != Unknown {
		t.Fatal("stale result must read Unknown")
	}
	waitSettled(t, m, Unreachable)
	clk.Add(31 * time.Second)
	s.Refresh()
	waitSettled(t, m, Unreachable)
	if n := tr.calls.Load(); n != 3 {
		t.Fatalf("Refresh did not re-probe a stale host: %d calls, want 3", n)
	}
	if m.Fresh() != Unreachable {
		t.Fatal("after Refresh the host must read fresh")
	}
	var nilSet *Set
	nilSet.Refresh() // must not panic
}

type refusedTransport struct{ err error }

func (r refusedTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }

// ut-docs#3137 review M3: a DownOnly Monitor (the per-signer probe) reads a
// refused port or TLS failure as Reachable — the host answered — and only a
// DNS failure, timeout or no-route error as Unreachable.
func TestDownOnlyCountsOnlyNetworkDownErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want State
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}, Unreachable},
		{"timeout", context.DeadlineExceeded, Unreachable},
		{"no route", &net.OpError{Op: "dial", Err: syscall.EHOSTUNREACH}, Unreachable},
		{"net unreachable", &net.OpError{Op: "dial", Err: syscall.ENETUNREACH}, Unreachable},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, Reachable},
		{"tls", errors.New("tls: handshake failure"), Reachable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New(Options{Endpoint: "https://signer.example.test", Now: newClock().Now, DownOnly: true,
				Client: &http.Client{Transport: refusedTransport{c.err}}})
			m.Status()
			waitSettled(t, m, c.want)
		})
	}
	// Without DownOnly (the status-bar light) a refused port stays Unreachable.
	m := New(Options{Endpoint: "https://cloud.example.test", Now: newClock().Now,
		Client: &http.Client{Transport: refusedTransport{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}}})
	m.Status()
	waitSettled(t, m, Unreachable)
}
