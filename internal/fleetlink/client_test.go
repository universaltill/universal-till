package fleetlink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ADR-0114 §2/§3/§4 (ut-docs#2735): the additional till's side of the link.
// It dials only an advertised main till, says hello, turns sync frames into
// pull kicks, reports, and reconnects with backoff — stopping for good only
// when the main till revoked it.

// swapHarness is a main till whose Hub can be replaced ("restarted") while
// the address stays the same. "Bearer good-<id>" authenticates as <id>.
type swapHarness struct {
	mu    sync.Mutex
	hub   *Hub
	cfg   Config
	dials atomic.Int32
	// before, when set, may answer the upgrade request itself (true).
	before func(w http.ResponseWriter, r *http.Request) bool
	srv    *httptest.Server
}

func newSwapHarness(t *testing.T, cfg Config) *swapHarness {
	t.Helper()
	h := &swapHarness{cfg: cfg}
	h.hub = NewHub(HubOptions{Config: cfg, Hello: testHello})
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LinkPath {
			http.NotFound(w, r)
			return
		}
		h.dials.Add(1)
		h.mu.Lock()
		before, hub := h.before, h.hub
		h.mu.Unlock()
		if before != nil && before(w, r) {
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		id, ok := strings.CutPrefix(tok, "good-")
		if !ok || id == "" || r.URL.RawQuery != "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hub.Serve(w, r, id)
	}))
	t.Cleanup(func() {
		h.currentHub().Close()
		h.srv.Close()
	})
	return h
}

func (h *swapHarness) currentHub() *Hub {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hub
}

// restart closes the hub (every link gets bye "shutdown") and starts a new one.
func (h *swapHarness) restart() {
	old := h.currentHub()
	old.Close()
	h.mu.Lock()
	h.hub = NewHub(HubOptions{Config: h.cfg, Hello: testHello})
	h.mu.Unlock()
}

// clientRec records every callback the client makes.
type clientRec struct {
	mu      sync.Mutex
	hellos  []Hello
	syncs   [][]Scope
	lost    []string
	revoked int
	alive   int
}

func (r *clientRec) get() (hellos int, syncs [][]Scope, lost []string, revoked int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hellos), append([][]Scope(nil), r.syncs...), append([]string(nil), r.lost...), r.revoked
}

type testTarget struct {
	mu  sync.Mutex
	tgt Target
	ok  bool
}

func (tt *testTarget) set(t Target) {
	tt.mu.Lock()
	tt.tgt, tt.ok = t, true
	tt.mu.Unlock()
}

func (tt *testTarget) get(context.Context) (Target, bool) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return tt.tgt, tt.ok
}

func fastClientOptions(cfg Config, tt *testTarget, rec *clientRec, level *atomic.Int32) ClientOptions {
	return ClientOptions{
		Config:       cfg,
		BackoffMin:   10 * time.Millisecond,
		BackoffMax:   80 * time.Millisecond,
		ReportEvery:  time.Hour,
		RecheckEvery: 20 * time.Millisecond,
		Target:       tt.get,
		Probe: func(context.Context, Target) (int, error) {
			return int(level.Load()), nil
		},
		Hello: func(context.Context) Hello {
			return Hello{TillID: "till-2", Version: "v1.0.0", SyncProtocol: SyncProtocolLevel}
		},
		Report: func(context.Context) Report {
			return Report{Version: "v1.0.0", UpdateState: "idle", PushQueueDepth: 3}
		},
		OnHello: func(_ context.Context, h Hello) {
			rec.mu.Lock()
			rec.hellos = append(rec.hellos, h)
			rec.mu.Unlock()
		},
		OnSync: func(_ context.Context, s []Scope) {
			rec.mu.Lock()
			rec.syncs = append(rec.syncs, s)
			rec.mu.Unlock()
		},
		OnLost: func(_ context.Context, cause string) {
			rec.mu.Lock()
			rec.lost = append(rec.lost, cause)
			rec.mu.Unlock()
		},
		OnRevoked: func(context.Context) {
			rec.mu.Lock()
			rec.revoked++
			rec.mu.Unlock()
		},
		WhileLinked: func(context.Context) {
			rec.mu.Lock()
			rec.alive++
			rec.mu.Unlock()
		},
	}
}

// startClient runs c until the test ends and proves Run returned.
func startClient(t *testing.T, c *Client) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Client.Run did not return after cancel")
		}
	})
	return cancel
}

func advertised() *atomic.Int32 {
	var l atomic.Int32
	l.Store(1)
	return &l
}

func TestClient_HelloExchangeSyncKickAndReport(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	startClient(t, c)

	waitFor(t, "linked", c.Linked)
	hub := h.currentHub()
	p := hub.Peer("till-2")
	if p == nil {
		t.Fatal("hub has no link for till-2")
	}
	waitFor(t, "replica hello at the main till", func() bool { _, ok := p.Hello(); return ok })
	if got, _ := p.Hello(); got.Role != "replica" || got.TillID != "till-2" || got.SyncProtocol != SyncProtocolLevel {
		t.Fatalf("replica hello = %+v", got)
	}
	waitFor(t, "main hello delivered", func() bool { n, _, _, _ := rec.get(); return n == 1 })
	rec.mu.Lock()
	mh := rec.hellos[0]
	rec.mu.Unlock()
	if mh.Role != "main" || mh.Cursors.Admin != "a1" {
		t.Fatalf("main hello = %+v", mh)
	}
	waitFor(t, "report on connect", func() bool {
		r, ok := hub.Report("till-2")
		return ok && r.UpdateState == "idle" && r.PushQueueDepth == 3 && !r.TLSPinned
	})

	hub.Nudge(ScopeAdmin, ScopeStock)
	waitFor(t, "sync kick", func() bool { _, s, _, _ := rec.get(); return len(s) > 0 })
	_, s, _, _ := rec.get()
	if len(s[0]) != 2 || s[0][0] != ScopeAdmin || s[0][1] != ScopeStock {
		t.Fatalf("sync scopes = %v", s[0])
	}
	waitFor(t, "WhileLinked tick", func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.alive > 0 })
}

func TestClient_OlderMainWithoutLinkIsNeverDialled(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	var level atomic.Int32 // 0: ping carries no link
	c := NewClient(fastClientOptions(cfg, tt, &clientRec{}, &level))
	startClient(t, c)
	time.Sleep(150 * time.Millisecond)
	if n := h.dials.Load(); n != 0 {
		t.Fatalf("dialled an older main till %d times", n)
	}
	if c.Linked() {
		t.Fatal("linked without a link")
	}
	// Once the main till is updated and advertises link: 1, it links.
	level.Store(1)
	waitFor(t, "linked after the main till advertises", c.Linked)
}

func TestClient_NotAReplicaNeverDials(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	c := NewClient(fastClientOptions(cfg, &testTarget{}, &clientRec{}, advertised()))
	startClient(t, c)
	time.Sleep(100 * time.Millisecond)
	if n := h.dials.Load(); n != 0 {
		t.Fatalf("a till with no main till dialled %d times", n)
	}
}

func TestClient_ReconnectsAfterMainTillRestartWithoutCountingAFailure(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	startClient(t, c)
	waitFor(t, "linked", c.Linked)

	h.restart() // bye "shutdown", then a fresh hub on the same address
	waitFor(t, "relinked to the restarted main till", func() bool {
		return h.currentHub().Peer("till-2") != nil && c.Linked()
	})
	waitFor(t, "second hello", func() bool { n, _, _, _ := rec.get(); return n >= 2 })
	if _, _, lost, _ := rec.get(); len(lost) != 0 {
		t.Fatalf("a bye counted as a failed contact: %v", lost)
	}
}

func TestClient_RevokedStopsUntilThePairingChanges(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	startClient(t, c)
	waitFor(t, "linked", c.Linked)

	h.currentHub().Disconnect("till-2") // close code 4003
	waitFor(t, "revoked surfaced", func() bool { _, _, _, r := rec.get(); return r == 1 })
	before := h.dials.Load()
	time.Sleep(200 * time.Millisecond) // many backoff periods
	if n := h.dials.Load(); n != before {
		t.Fatalf("revoked client redialled %d times", n-before)
	}
	if c.Linked() {
		t.Fatal("still linked after revoke")
	}
	if _, _, lost, _ := rec.get(); len(lost) != 0 {
		t.Fatalf("revoke counted as a failed contact: %v", lost)
	}

	// Re-paired: a new bearer is a new pairing — link again.
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-3"})
	c.Redial()
	waitFor(t, "linked after re-pairing", func() bool { return h.currentHub().Peer("till-3") != nil })
}

func TestClient_UnauthorizedDialStops(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "revoked-bearer"})
	rec := &clientRec{}
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	startClient(t, c)
	waitFor(t, "revoked surfaced", func() bool { _, _, _, r := rec.get(); return r == 1 })
	time.Sleep(150 * time.Millisecond)
	if n := h.dials.Load(); n != 1 {
		t.Fatalf("dialled %d times with a bearer the main till refused", n)
	}
}

func TestClient_HeartbeatLossCountsAFailedContact(t *testing.T) {
	cfg := fastConfig()
	cfg.PingInterval = 20 * time.Millisecond
	cfg.PeerTimeout = 150 * time.Millisecond
	h := newSwapHarness(t, cfg)
	// A "main till" that accepts the link and then goes silent: no hello,
	// no ping, never reads.
	var mu sync.Mutex
	var held []*websocket.Conn
	h.before = func(w http.ResponseWriter, r *http.Request) bool {
		ws, err := websocket.Accept(w, r, nil)
		if err == nil {
			mu.Lock()
			held = append(held, ws)
			mu.Unlock()
		}
		return true
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.CloseNow()
		}
	})
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	startClient(t, c)
	waitFor(t, "lost link reported", func() bool { _, _, lost, _ := rec.get(); return len(lost) > 0 })
	if c.Linked() {
		t.Fatal("linked without a hello")
	}
}

func TestClient_RedialsTheNewAddressAfterASwitch(t *testing.T) {
	cfg := fastConfig()
	a := newSwapHarness(t, cfg)
	b := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: a.srv.URL, Bearer: "good-till-2"})
	c := NewClient(fastClientOptions(cfg, tt, &clientRec{}, advertised()))
	startClient(t, c)
	waitFor(t, "linked to A", func() bool { return a.currentHub().Peer("till-2") != nil })

	tt.set(Target{BaseURL: b.srv.URL, Bearer: "good-till-2"}) // re-discovery switched sync.primary_url
	c.Redial()
	waitFor(t, "linked to B", func() bool { return b.currentHub().Peer("till-2") != nil })
	waitFor(t, "A released", func() bool { return a.currentHub().Peer("till-2") == nil })
}

func TestClient_TargetChangeIsNoticedWithoutARedialCall(t *testing.T) {
	cfg := fastConfig()
	a := newSwapHarness(t, cfg)
	b := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: a.srv.URL, Bearer: "good-till-2"})
	c := NewClient(fastClientOptions(cfg, tt, &clientRec{}, advertised()))
	startClient(t, c)
	waitFor(t, "linked to A", func() bool { return a.currentHub().Peer("till-2") != nil })
	tt.set(Target{BaseURL: b.srv.URL, Bearer: "good-till-2"})
	waitFor(t, "linked to B on the recheck", func() bool { return b.currentHub().Peer("till-2") != nil })
}

func TestClient_HonoursRetryAfterOn503(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	var first atomic.Bool
	var firstAt, secondAt atomic.Int64
	h.before = func(w http.ResponseWriter, r *http.Request) bool {
		if first.CompareAndSwap(false, true) {
			firstAt.Store(time.Now().UnixNano())
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many till links", http.StatusServiceUnavailable)
			return true
		}
		secondAt.CompareAndSwap(0, time.Now().UnixNano())
		return false
	}
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	c := NewClient(fastClientOptions(cfg, tt, &clientRec{}, advertised()))
	startClient(t, c)
	deadline := time.Now().Add(3 * time.Second)
	for !c.Linked() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !c.Linked() {
		t.Fatal("never linked after the 503")
	}
	if gap := time.Duration(secondAt.Load() - firstAt.Load()); gap < 900*time.Millisecond {
		t.Fatalf("redialled %v after a 503 with Retry-After: 1", gap)
	}
}

func TestClient_ReplacedLinkBacksOffInsteadOfFighting(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	opts := fastClientOptions(cfg, tt, &clientRec{}, advertised())
	opts.BackoffMax = 400 * time.Millisecond
	c := NewClient(opts)
	startClient(t, c)
	waitFor(t, "linked", c.Linked)

	// Another connection claims till-2: the client's link gets 4001.
	hub := h.currentHub()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := http.Header{"Authorization": {"Bearer good-till-2"}}
	other, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http")+LinkPath, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer other.CloseNow()
	waitFor(t, "client unlinked", func() bool { return !c.Linked() })
	dials := h.dials.Load()
	time.Sleep(150 * time.Millisecond) // < BackoffMax/2
	if n := h.dials.Load(); n != dials {
		t.Fatalf("replaced client redialled after %d ms — fighting the newer link", 150)
	}
	_ = hub
}

func TestClient_ShutdownSaysBye(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	c := NewClient(fastClientOptions(cfg, tt, &clientRec{}, advertised()))
	cancel := startClient(t, c)
	waitFor(t, "linked", c.Linked)
	cancel()
	waitFor(t, "main till saw the link go", func() bool { return h.currentHub().Len() == 0 })
}

func TestClient_LinkChangedSignalsBothWays(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	c := NewClient(fastClientOptions(cfg, tt, &clientRec{}, advertised()))
	ch := c.LinkChanged()
	startClient(t, c)
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no LinkChanged on link up")
	}
	h.currentHub().Disconnect("till-2")
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no LinkChanged on link down")
	}
	if c.Linked() {
		t.Fatal("still linked")
	}
}

func TestBackoff_FullJitterWithinBounds(t *testing.T) {
	c := NewClient(ClientOptions{BackoffMin: time.Second, BackoffMax: 30 * time.Second})
	for attempt, ceil := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 6: 30 * time.Second, 40: 30 * time.Second} {
		for range 200 {
			if d := c.backoff(attempt); d < 0 || d > ceil {
				t.Fatalf("attempt %d: %v outside [0, %v]", attempt, d, ceil)
			}
		}
	}
	d := DefaultClientOptions()
	if d.BackoffMin != time.Second || d.BackoffMax != 30*time.Second || d.ReportEvery != 5*time.Minute {
		t.Fatalf("defaults drifted from ADR-0114: %+v", d)
	}
}

func TestLinkURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://192.168.1.5:8080":  "ws://192.168.1.5:8080/api/sync/link",
		"http://192.168.1.5:8080/": "ws://192.168.1.5:8080/api/sync/link",
		"https://main.local:8443":  "wss://main.local:8443/api/sync/link",
		"ftp://nope":               "",
		"":                         "",
	} {
		if got, _ := linkURL(in); got != want {
			t.Errorf("linkURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// §4: backoff is per outage. Five failed dials, then a link that comes up
// and later drops without a close frame — the next redial starts over at
// BackoffMin, not at the sixth step of the old outage.
func TestClient_BackoffRestartsAfterAnEstablishedLink(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tgt := Target{BaseURL: h.srv.URL, Bearer: "good-till-2"}
	tt.set(tgt)
	c := NewClient(fastClientOptions(cfg, tt, &clientRec{}, advertised()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		delay   time.Duration
		attempt int
	}
	out := make(chan result, 1)
	go func() {
		d, a := c.attempt(ctx, tgt, 5)
		out <- result{d, a}
	}()
	waitFor(t, "linked", c.Linked)
	p := h.currentHub().Peer("till-2")
	if p == nil {
		t.Fatal("hub has no link for till-2")
	}
	_ = p.conn.(wsConn).c.CloseNow() // the main till vanishes mid-link
	select {
	case r := <-out:
		if r.attempt != 1 || r.delay > c.opts.BackoffMin {
			t.Fatalf("after an established link: attempt %d, delay %v; want attempt 1 within %v", r.attempt, r.delay, c.opts.BackoffMin)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attempt did not return after the link dropped")
	}
}
