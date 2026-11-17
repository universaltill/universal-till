package fleetlink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ADR-0114 amendment 2026-11-03 (ut-docs#5297 slice B1): the main till's
// pending link. T-numbers refer to the Architect's test list.

func pendingServe(t *testing.T, h *PendingHub, id string, exp time.Time, recheck func() string) (*fakeConn, chan struct{}) {
	t.Helper()
	f := newFakeConn()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeConn(id, f, exp, recheck)
	}()
	return f, done
}

func waitDone(t *testing.T, done chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: ServeConn did not return", what)
	}
}

func waitLen(t *testing.T, h *PendingHub, n int) {
	t.Helper()
	waitFor(t, "pending link count", func() bool { return h.Len() == n })
}

// nextPairing returns the next non-ping frame and requires it to be a
// pairing frame.
func nextPairing(t *testing.T, f *fakeConn) string {
	t.Helper()
	e := f.next(t)
	if e.Type != TypePairing {
		t.Fatalf("frame type %q, want pairing", e.Type)
	}
	var p map[string]any
	if err := json.Unmarshal(e.Payload, &p); err != nil || len(p) != 1 {
		t.Fatalf("pairing payload %s: want exactly {status}", e.Payload)
	}
	return p["status"].(string)
}

func TestPending_T1_ResolveSendsOneFrameThenCloses(t *testing.T) {
	for _, st := range []string{PairingApproved, PairingDenied} {
		h := NewPendingHub(fastConfig())
		f, done := pendingServe(t, h, "r1", time.Now().Add(time.Hour), nil)
		waitLen(t, h, 1)
		h.Resolve("r1", st)
		h.Resolve("r1", PairingExpired) // first one wins
		if got := nextPairing(t, f); got != st {
			t.Fatalf("status %q, want %q", got, st)
		}
		waitDone(t, done, st)
		if c, ok := f.closeCode(); !ok || c != CloseNormal {
			t.Fatalf("close %v %v, want 1000", c, ok)
		}
		// Nothing but pings may follow the one pairing frame.
		for {
			select {
			case b := <-f.out:
				if e, _ := Decode(b); e.Type != TypePing {
					t.Fatalf("extra frame %s", b)
				}
				continue
			default:
			}
			break
		}
		if h.Len() != 0 {
			t.Fatalf("Len %d after resolve", h.Len())
		}
	}
}

func TestPending_T1_NoFrameBeforeResolveButPings(t *testing.T) {
	h := NewPendingHub(fastConfig())
	f, _ := pendingServe(t, h, "r1", time.Now().Add(time.Hour), nil)
	t.Cleanup(h.Close)
	time.Sleep(200 * time.Millisecond)
	n := 0
	for len(f.out) > 0 {
		e, _ := Decode(<-f.out)
		if e.Type != TypePing {
			t.Fatalf("unexpected %q before resolve (no hello on a pending link)", e.Type)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no ping within 200ms at 50ms interval")
	}
}

func TestPending_T2_ResolveReachesOnlyItsLink(t *testing.T) {
	h := NewPendingHub(fastConfig())
	a, adone := pendingServe(t, h, "A", time.Now().Add(time.Hour), nil)
	b, _ := pendingServe(t, h, "B", time.Now().Add(time.Hour), nil)
	waitLen(t, h, 2)
	h.Resolve("A", PairingApproved)
	if got := nextPairing(t, a); got != PairingApproved {
		t.Fatalf("A got %q", got)
	}
	waitDone(t, adone, "A")
	if _, closed := b.closeCode(); closed {
		t.Fatal("B was closed by A's resolve")
	}
	for len(b.out) > 0 {
		if e, _ := Decode(<-b.out); e.Type != TypePing {
			t.Fatalf("B saw %q", e.Type)
		}
	}
	h.Resolve("nope", PairingApproved) // no link: no-op
	h.Close()
}

func TestPending_T3_StrictInboundFrames(t *testing.T) {
	bad := map[string]string{
		"hello":      `{"v":1,"id":"x","type":"hello","payload":{"till_id":"t"}}`,
		"pairing":    `{"v":1,"id":"x","type":"pairing","payload":{"status":"approved"}}`,
		"sync":       `{"v":1,"id":"x","type":"sync"}`,
		"unknown":    `{"v":1,"id":"x","type":"zzz"}`,
		"report":     `{"v":1,"id":"x","type":"report"}`,
		"error":      `{"v":1,"id":"x","type":"error","reply_to":"p"}`,
		"malformed":  `{not json`,
		"wrong v":    `{"v":2,"id":"x","type":"ping"}`,
		"no id":      `{"v":1,"type":"ping"}`,
		"long ping":  `{"v":1,"id":"` + strings.Repeat("a", 65) + `","type":"ping"}`,
		"empty text": ``,
	}
	for name, frame := range bad {
		t.Run(name, func(t *testing.T) {
			h := NewPendingHub(fastConfig())
			f, done := pendingServe(t, h, "r1", time.Now().Add(time.Hour), nil)
			waitLen(t, h, 1)
			f.in <- []byte(frame)
			waitDone(t, done, name)
			if c, _ := f.closeCode(); c != CloseProtocol {
				t.Fatalf("close code %v, want 1002", c)
			}
			if h.Len() != 0 {
				t.Fatal("link not removed")
			}
		})
	}
}

func TestPending_T3_OverTheWireBinaryAndOversize(t *testing.T) {
	h := NewPendingHub(fastConfig())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Serve(w, r, "r1", time.Now().Add(time.Hour), nil)
	}))
	t.Cleanup(func() { h.Close(); srv.Close() })
	dial := func() *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.CloseNow() })
		return c
	}
	closeStatus := func(c *websocket.Conn) websocket.StatusCode {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return websocket.CloseStatus(err)
			}
			if e, _ := Decode(b); e.Type != TypePing {
				t.Fatalf("unexpected frame %s", b)
			}
		}
	}
	c := dial()
	if err := c.Write(context.Background(), websocket.MessageBinary, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if s := closeStatus(c); s != websocket.StatusProtocolError {
		t.Fatalf("binary frame: close %d, want 1002", s)
	}
	// Oversize: the library enforces SetReadLimit and closes with 1009
	// (message too big) — still a close, which is what AC8 needs.
	waitLen(t, h, 0)
	c = dial()
	big := `{"v":1,"id":"x","type":"ping","payload":"` + strings.Repeat("a", PendingReadLimit) + `"}`
	_ = c.Write(context.Background(), websocket.MessageText, []byte(big))
	if s := closeStatus(c); s != websocket.StatusMessageTooBig {
		t.Fatalf("oversize: close %d, want 1009", s)
	}
}

func TestPending_T4_PingPongAndLiveness(t *testing.T) {
	cfg := fastConfig()
	cfg.PeerTimeout = 400 * time.Millisecond
	h := NewPendingHub(cfg)
	f, done := pendingServe(t, h, "r1", time.Now().Add(time.Hour), nil)
	waitLen(t, h, 1)
	f.send(t, mustMsg(t, "p-1", TypePing, "", nil))
	e := f.next(t)
	if e.Type != TypePong || e.ReplyTo != "p-1" {
		t.Fatalf("got %+v, want pong reply_to p-1", e)
	}
	// Silence (we never answer the server's pings) → gone.
	waitDone(t, done, "silence")
	if c, _ := f.closeCode(); c != CloseGone {
		t.Fatalf("close %v, want CloseGone", c)
	}
}

func TestPending_T4_AnyValidFrameKeepsLinkAlive(t *testing.T) {
	cfg := fastConfig()
	cfg.PeerTimeout = 300 * time.Millisecond
	h := NewPendingHub(cfg)
	f, _ := pendingServe(t, h, "r1", time.Now().Add(time.Hour), nil)
	t.Cleanup(h.Close)
	for range 10 {
		time.Sleep(100 * time.Millisecond)
		f.send(t, mustMsg(t, "pg", TypePong, "", nil))
	}
	if _, closed := f.closeCode(); closed {
		t.Fatal("a peer that keeps answering was dropped")
	}
}

func TestPending_T4_ClientByeClosesQuietly(t *testing.T) {
	h := NewPendingHub(fastConfig())
	f, done := pendingServe(t, h, "r1", time.Now().Add(time.Hour), nil)
	waitLen(t, h, 1)
	f.send(t, mustMsg(t, "b", TypeBye, "", ByePayload{Reason: ByeShutdown}))
	waitDone(t, done, "bye")
	if h.Len() != 0 {
		t.Fatal("link kept after bye")
	}
}

func TestPending_T5_OneLinkPerRequestAndCap(t *testing.T) {
	h := NewPendingHub(fastConfig())
	first, firstDone := pendingServe(t, h, "r1", time.Now().Add(time.Hour), nil)
	waitLen(t, h, 1)
	second, _ := pendingServe(t, h, "r1", time.Now().Add(time.Hour), nil)
	waitDone(t, firstDone, "replaced link")
	if c, _ := first.closeCode(); c != CloseReplaced {
		t.Fatalf("first closed %v, want CloseReplaced", c)
	}
	waitLen(t, h, 1)
	if _, closed := second.closeCode(); closed {
		t.Fatal("the newer link was closed")
	}
	// Resolve now reaches the newer link only.
	h.Resolve("r1", PairingApproved)
	if got := nextPairing(t, second); got != PairingApproved {
		t.Fatalf("got %q", got)
	}

	h2 := NewPendingHub(fastConfig())
	t.Cleanup(h2.Close)
	for i := range MaxPendingLinks {
		pendingServe(t, h2, "id"+string(rune('a'+i)), time.Now().Add(time.Hour), nil)
	}
	waitLen(t, h2, MaxPendingLinks)
	ninth, ninthDone := pendingServe(t, h2, "idz", time.Now().Add(time.Hour), nil)
	waitDone(t, ninthDone, "9th link")
	if c, _ := ninth.closeCode(); c != CloseTryAgain {
		t.Fatalf("9th link closed %v, want CloseTryAgain", c)
	}
	if h2.Len() != MaxPendingLinks {
		t.Fatalf("Len %d", h2.Len())
	}
}

func TestPending_T5_ServeAnswers503WithRetryAfterOverCapAndAfterClose(t *testing.T) {
	cfg := fastConfig()
	cfg.RetryAfter = 30 * time.Second
	h := NewPendingHub(cfg)
	for i := range MaxPendingLinks {
		pendingServe(t, h, "id"+string(rune('a'+i)), time.Now().Add(time.Hour), nil)
	}
	waitLen(t, h, MaxPendingLinks)
	rec := httptest.NewRecorder()
	h.Serve(rec, httptest.NewRequest(http.MethodGet, "/api/sync/link", nil), "new", time.Now().Add(time.Hour), nil)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("over cap: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	h.Close()
	rec = httptest.NewRecorder()
	h.Serve(rec, httptest.NewRequest(http.MethodGet, "/api/sync/link", nil), "ida", time.Now().Add(time.Hour), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after Close: %d", rec.Code)
	}
}

func TestPending_T6_ExpiryTimerSendsExpired(t *testing.T) {
	h := NewPendingHub(fastConfig())
	f, done := pendingServe(t, h, "r1", time.Now().Add(150*time.Millisecond), nil)
	if got := nextPairing(t, f); got != PairingExpired {
		t.Fatalf("got %q", got)
	}
	waitDone(t, done, "expiry")
	if c, _ := f.closeCode(); c != CloseNormal {
		t.Fatalf("close %v", c)
	}
}

func TestPending_T7_RecheckAtRegistrationClosesTheRace(t *testing.T) {
	h := NewPendingHub(fastConfig())
	f, done := pendingServe(t, h, "r1", time.Now().Add(time.Hour), func() string { return PairingApproved })
	if got := nextPairing(t, f); got != PairingApproved {
		t.Fatalf("got %q", got)
	}
	waitDone(t, done, "race")

	g, gdone := pendingServe(t, h, "r2", time.Now().Add(time.Hour), func() string { return "" })
	waitLen(t, h, 1)
	if _, closed := g.closeCode(); closed {
		t.Fatal("an unresolved recheck must leave the link open")
	}
	h.Close()
	waitDone(t, gdone, "close")
}

// Fix (a): the per-link expiry timer rechecks first, so an approve that landed
// in the last second is reported, not contradicted by `expired`.
func TestPending_ExpiryRechecksBeforeSayingExpired(t *testing.T) {
	for name, tc := range map[string]struct{ second, want string }{
		"approved_in_last_second": {PairingApproved, PairingApproved},
		"still_pending":           {"", PairingExpired},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewPendingHub(fastConfig())
			var calls atomic.Int32
			f, done := pendingServe(t, h, "r1", time.Now().Add(150*time.Millisecond), func() string {
				if calls.Add(1) == 1 {
					return "" // registration recheck: still pending
				}
				return tc.second
			})
			if got := nextPairing(t, f); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			waitDone(t, done, "expiry")
		})
	}
}

// A panic in recheck (net/http would recover it) must not leak the id's slot.
func TestPending_PanicInRecheckFreesTheSlot(t *testing.T) {
	h := NewPendingHub(fastConfig())
	f := newFakeConn()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		h.ServeConn("r1", f, time.Now().Add(time.Hour), func() string { panic("boom") })
	}()
	waitDone(t, done, "panic")
	waitLen(t, h, 0)
	h.Close()
}

func TestPending_T8_CloseSaysByeAndWaits(t *testing.T) {
	h := NewPendingHub(fastConfig())
	a, _ := pendingServe(t, h, "A", time.Now().Add(time.Hour), nil)
	b, _ := pendingServe(t, h, "B", time.Now().Add(time.Hour), nil)
	waitLen(t, h, 2)
	h.Close()
	for _, f := range []*fakeConn{a, b} {
		e := f.next(t)
		var bye ByePayload
		if e.Type != TypeBye || json.Unmarshal(e.Payload, &bye) != nil || bye.Reason != ByeShutdown {
			t.Fatalf("got %+v, want bye shutdown", e)
		}
		if c, ok := f.closeCode(); !ok || c != CloseShutdown {
			t.Fatalf("close %v %v", c, ok)
		}
	}
	if h.Len() != 0 {
		t.Fatalf("Len %d after Close", h.Len())
	}
	// A late link after Close is refused at once.
	late, ldone := pendingServe(t, h, "C", time.Now().Add(time.Hour), nil)
	waitDone(t, ldone, "late")
	if c, _ := late.closeCode(); c != CloseShutdown {
		t.Fatalf("late link close %v", c)
	}
}

func TestPending_T8_SlowPeerIsDropped(t *testing.T) {
	cfg := fastConfig()
	cfg.PingInterval = time.Hour
	h := NewPendingHub(cfg)
	f := newFakeConn()
	f.gate = make(chan struct{}) // no write ever completes
	done := make(chan struct{})
	go func() { defer close(done); h.ServeConn("r1", f, time.Now().Add(time.Hour), nil) }()
	waitLen(t, h, 1)
	h.Resolve("r1", PairingApproved)
	waitDone(t, done, "slow peer")
	if c, _ := f.closeCode(); c != CloseSlow {
		t.Fatalf("close %v, want CloseSlow", c)
	}
}

// settleGoroutines waits for the goroutine count to fall back to base.
func settleGoroutines(t *testing.T, base int, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("%s: goroutines %d > base %d\n%s", what, runtime.NumGoroutine(), base, buf)
}

func TestPending_T9_NoGoroutineLeak(t *testing.T) {
	ends := map[string]func(h *PendingHub, f *fakeConn){
		"approve":    func(h *PendingHub, _ *fakeConn) { h.Resolve("r", PairingApproved) },
		"deny":       func(h *PendingHub, _ *fakeConn) { h.Resolve("r", PairingDenied) },
		"disconnect": func(_ *PendingHub, f *fakeConn) { f.Close(CloseNormal, "client gone") },
		"shutdown":   func(h *PendingHub, _ *fakeConn) { h.Close() },
		"expiry":     nil, // armed through expiresAt below
		"replaced": func(h *PendingHub, _ *fakeConn) {
			pendingServe(t, h, "r", time.Now().Add(time.Hour), nil) // same id: the first link is replaced
		},
	}
	runtime.GC()
	base := runtime.NumGoroutine()
	for name, end := range ends {
		h := NewPendingHub(fastConfig())
		exp := time.Now().Add(time.Hour)
		if end == nil {
			exp = time.Now().Add(100 * time.Millisecond)
		}
		f, done := pendingServe(t, h, "r", exp, nil)
		waitLen(t, h, 1)
		if end != nil {
			end(h, f)
		}
		waitDone(t, done, name)
		h.Close()
		settleGoroutines(t, base, name)
	}
}

func TestPending_T9_OverTheWireNoLeakAfterClientDrop(t *testing.T) {
	runtime.GC()
	base := runtime.NumGoroutine()
	h := NewPendingHub(fastConfig())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Serve(w, r, "r1", time.Now().Add(time.Hour), nil)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	waitLen(t, h, 1)
	_ = c.CloseNow()
	waitLen(t, h, 0)
	h.Close()
	srv.Close()
	srv.CloseClientConnections()
	settleGoroutines(t, base+2, "wire") // http keep-alive bookkeeping may linger briefly
}
