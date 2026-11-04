package fleetlink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/universaltill/universal-till/internal/lantls"
)

// ADR-0114 amendment 2026-11-03 (ut-docs#5330 slice B2): the joining till's
// side of the pending link. These run against the real B1 PendingHub behind
// a TLS httptest server and a pinned client.

const testPairID, testPairSecret = "req-123", "s3cr3t-value"

type pendingEnv struct {
	srv   *httptest.Server
	hub   *PendingHub
	pin   string
	dials atomic.Int32 // requests that reached the handler
}

type seenReq struct {
	rawQuery, url, auth, id, secret string
	protoMajor                      int
}

// newPendingEnv starts a TLS server whose handler checks the pair headers
// (401 otherwise) and hands the request to hub.Serve unless over is set.
func newPendingEnv(t *testing.T, cfg Config, exp time.Time, over func(w http.ResponseWriter, r *http.Request, n int) bool, seen *chan seenReq) *pendingEnv {
	t.Helper()
	e := &pendingEnv{hub: NewPendingHub(cfg)}
	e.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(e.dials.Add(1))
		if seen != nil {
			select {
			case *seen <- seenReq{r.URL.RawQuery, r.URL.String(), r.Header.Get("Authorization"),
				r.Header.Get(HeaderPairRequestID), r.Header.Get(HeaderPairRequestSecret), r.ProtoMajor}:
			default:
			}
		}
		if over != nil && over(w, r, n) {
			return
		}
		if r.Header.Get(HeaderPairRequestID) != testPairID || r.Header.Get(HeaderPairRequestSecret) != testPairSecret {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		e.hub.Serve(w, r, testPairID, exp, nil)
	}))
	e.srv.EnableHTTP2 = true
	e.srv.StartTLS()
	e.pin = lantls.PinOf(e.srv.Certificate())
	t.Cleanup(func() {
		e.hub.Close()
		e.srv.CloseClientConnections()
		e.srv.Close()
	})
	return e
}

func (e *pendingEnv) target(timeout time.Duration, pin string) PendingTarget {
	if pin == "" {
		pin = e.pin
	}
	return PendingTarget{BaseURL: e.srv.URL, ID: testPairID, Secret: testPairSecret,
		HTTPClient: lantls.NewPinnedClient(timeout, pin).Client}
}

func fastPending() PendingOptions {
	return PendingOptions{Config: fastConfig(), BackoffMin: 10 * time.Millisecond, BackoffMax: 40 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second}
}

func watchAsync(ctx context.Context, tg PendingTarget, o PendingOptions) <-chan PendingResult {
	ch := make(chan PendingResult, 1)
	go func() { ch <- WatchPending(ctx, tg, o) }()
	return ch
}

func gotResult(t *testing.T, ch <-chan PendingResult) PendingResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(8 * time.Second):
		t.Fatal("WatchPending did not return")
		return PendingResult{}
	}
}

func TestPendingClient_F1_ResolvesApprovedDeniedExpired(t *testing.T) {
	for _, st := range []string{PairingApproved, PairingDenied, PairingExpired} {
		exp := time.Now().Add(time.Hour)
		if st == PairingExpired {
			exp = time.Now().Add(300 * time.Millisecond)
		}
		e := newPendingEnv(t, fastConfig(), exp, nil, nil)
		ch := watchAsync(context.Background(), e.target(0, ""), fastPending())
		if st != PairingExpired {
			waitFor(t, "link", func() bool { return e.hub.Len() == 1 })
			e.hub.Resolve(testPairID, st)
		}
		r := gotResult(t, ch)
		if r.End != PendingResolved || r.Status != st {
			t.Fatalf("%s: result %+v", st, r)
		}
	}
}

func TestPendingClient_F2_HeadersOnlyNoQueryNoBearer(t *testing.T) {
	seen := make(chan seenReq, 4)
	e := newPendingEnv(t, fastConfig(), time.Now().Add(time.Hour), nil, &seen)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := watchAsync(ctx, e.target(0, ""), fastPending())
	waitFor(t, "link", func() bool { return e.hub.Len() == 1 })
	s := <-seen
	if s.id != testPairID || s.secret != testPairSecret {
		t.Fatalf("headers id=%q secret=%q", s.id, s.secret)
	}
	if s.rawQuery != "" || s.auth != "" {
		t.Fatalf("query %q auth %q, want both empty", s.rawQuery, s.auth)
	}
	if strings.Contains(s.url, testPairID) || strings.Contains(s.url, testPairSecret) {
		t.Fatalf("URL %q carries a credential", s.url)
	}
	cancel()
	if r := gotResult(t, ch); r.End != PendingStopped {
		t.Fatalf("result %+v", r)
	}
}

func TestPendingClient_F3_Http1OnH2ServerAndNoClientDeadline(t *testing.T) {
	seen := make(chan seenReq, 4)
	e := newPendingEnv(t, fastConfig(), time.Now().Add(time.Hour), nil, &seen)
	// The pinned client's own Timeout is far shorter than the link's life.
	ch := watchAsync(context.Background(), e.target(80*time.Millisecond, ""), fastPending())
	waitFor(t, "link", func() bool { return e.hub.Len() == 1 })
	if s := <-seen; s.protoMajor != 1 {
		t.Fatalf("server saw HTTP/%d", s.protoMajor)
	}
	time.Sleep(400 * time.Millisecond)
	if e.hub.Len() != 1 || e.dials.Load() != 1 {
		t.Fatalf("link cut by the client timeout: links %d dials %d", e.hub.Len(), e.dials.Load())
	}
	e.hub.Resolve(testPairID, PairingApproved)
	if r := gotResult(t, ch); r.Status != PairingApproved {
		t.Fatalf("result %+v", r)
	}
}

func TestPendingClient_F4_WrongPinNeverReachesServer(t *testing.T) {
	e := newPendingEnv(t, fastConfig(), time.Now().Add(time.Hour), nil, nil)
	// (httptest servers share one certificate, so a second server's pin
	// would match; any other pin is a different certificate.)
	r := gotResult(t, watchAsync(context.Background(), e.target(0, strings.Repeat("0", len(e.pin))), fastPending()))
	if r.End != PendingRefused || e.dials.Load() != 0 {
		t.Fatalf("result %+v, handler hits %d", r, e.dials.Load())
	}
}

func TestPendingClient_F5_FinalStatusesDialOnce(t *testing.T) {
	for _, code := range []int{401, 404, 409, 500} {
		e := newPendingEnv(t, fastConfig(), time.Now().Add(time.Hour), func(w http.ResponseWriter, _ *http.Request, _ int) bool {
			http.Error(w, "x", code)
			return true
		}, nil)
		r := gotResult(t, watchAsync(context.Background(), e.target(0, ""), fastPending()))
		time.Sleep(100 * time.Millisecond)
		if r.End != PendingRefused || r.HTTPStatus != code || e.dials.Load() != 1 {
			t.Fatalf("%d: result %+v dials %d", code, r, e.dials.Load())
		}
	}
}

func TestPendingClient_F5_RetryAfterHonoured(t *testing.T) {
	for _, code := range []int{429, 503} {
		var first atomic.Int64
		e := newPendingEnv(t, fastConfig(), time.Now().Add(time.Hour), func(w http.ResponseWriter, _ *http.Request, n int) bool {
			if n == 1 {
				first.Store(time.Now().UnixNano())
				w.Header().Set("Retry-After", "1")
				http.Error(w, "slow down", code)
				return true
			}
			return false
		}, nil)
		ch := watchAsync(context.Background(), e.target(0, ""), fastPending())
		waitFor(t, "link", func() bool { return e.hub.Len() == 1 })
		if gap := time.Since(time.Unix(0, first.Load())); gap < time.Second {
			t.Fatalf("%d: redialled after %v, want >= Retry-After", code, gap)
		}
		e.hub.Resolve(testPairID, PairingApproved)
		if r := gotResult(t, ch); r.Status != PairingApproved {
			t.Fatalf("%d: %+v", code, r)
		}
	}
}

func TestPendingClient_F5_TransportErrorBacksOffAndRedials(t *testing.T) {
	e := newPendingEnv(t, fastConfig(), time.Now().Add(time.Hour), func(w http.ResponseWriter, _ *http.Request, n int) bool {
		if n <= 2 {
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = c.Close()
			}
			return true
		}
		return false
	}, nil)
	ch := watchAsync(context.Background(), e.target(0, ""), fastPending())
	waitFor(t, "link after two resets", func() bool { return e.hub.Len() == 1 })
	e.hub.Resolve(testPairID, PairingDenied)
	if r := gotResult(t, ch); r.Status != PairingDenied || e.dials.Load() != 3 {
		t.Fatalf("result %+v dials %d", r, e.dials.Load())
	}
}

func TestPendingClient_F6_ReplacedIsFinalButByeRedials(t *testing.T) {
	e := newPendingEnv(t, fastConfig(), time.Now().Add(time.Hour), nil, nil)
	ctx2, cancel2 := context.WithCancel(context.Background())
	first := watchAsync(context.Background(), e.target(0, ""), fastPending())
	waitFor(t, "first link", func() bool { return e.hub.Len() == 1 })
	second := watchAsync(ctx2, e.target(0, ""), fastPending())
	r := gotResult(t, first)
	if r.End != PendingRefused || r.CloseCode != CloseReplaced {
		t.Fatalf("replaced link: %+v", r)
	}
	if e.dials.Load() != 2 {
		t.Fatalf("dials %d, want 2 (no redial after 4001)", e.dials.Load())
	}
	// Hub shutdown (bye + 1001) is retryable: the client dials again.
	e.hub.Close()
	waitFor(t, "redial after bye", func() bool { return e.dials.Load() >= 3 })
	cancel2()
	if r := gotResult(t, second); r.End != PendingStopped {
		t.Fatalf("second: %+v", r)
	}
}

// rawPendingServer is a server speaking the link by hand: script runs on the
// accepted websocket.
func rawPendingServer(t *testing.T, script func(ctx context.Context, c *websocket.Conn, n int)) *pendingEnv {
	t.Helper()
	e := &pendingEnv{}
	e.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(e.dials.Add(1))
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		defer c.CloseNow()
		script(r.Context(), c, n)
	}))
	e.srv.StartTLS()
	e.pin = lantls.PinOf(e.srv.Certificate())
	t.Cleanup(func() { e.srv.CloseClientConnections(); e.srv.Close() })
	return e
}

func pcSend(ctx context.Context, c *websocket.Conn, id, typ string, payload any) {
	m, _ := newMessage(id, typ, "", payload)
	b, _ := Encode(m)
	_ = c.Write(ctx, websocket.MessageText, b)
}

func TestPendingClient_F7_PongsPingsAndIgnoresUnknown(t *testing.T) {
	gotPong := make(chan string, 1)
	e := rawPendingServer(t, func(ctx context.Context, c *websocket.Conn, _ int) {
		pcSend(ctx, c, "srv-ping-1", TypePing, nil)
		pcSend(ctx, c, "x1", "mystery", map[string]string{"a": "b"})
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if env, err := Decode(b); err == nil && env.Type == TypePong {
				gotPong <- env.ReplyTo
				break
			}
		}
		pcSend(ctx, c, "p1", TypePairing, PairingPayload{Status: PairingApproved})
		_ = c.Close(websocket.StatusNormalClosure, "")
	})
	r := gotResult(t, watchAsync(context.Background(), e.target(0, ""), fastPending()))
	select {
	case id := <-gotPong:
		if id != "srv-ping-1" {
			t.Fatalf("pong reply_to %q", id)
		}
	default:
		t.Fatal("no pong")
	}
	if r.Status != PairingApproved || e.dials.Load() != 1 {
		t.Fatalf("result %+v dials %d (unknown frame must not end the link)", r, e.dials.Load())
	}
}

func TestPendingClient_F7_SilentServerIsRedialledAtPeerTimeout(t *testing.T) {
	e := rawPendingServer(t, func(ctx context.Context, c *websocket.Conn, _ int) {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	})
	o := fastPending()
	o.Config.PeerTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	ch := watchAsync(ctx, e.target(0, ""), o)
	waitFor(t, "redial after silence", func() bool { return e.dials.Load() >= 2 })
	cancel()
	if r := gotResult(t, ch); r.End != PendingStopped {
		t.Fatalf("result %+v", r)
	}
}

func TestPendingClient_F7_UnknownPairingStatusIsFinal(t *testing.T) {
	e := rawPendingServer(t, func(ctx context.Context, c *websocket.Conn, _ int) {
		pcSend(ctx, c, "p1", TypePairing, PairingPayload{Status: "maybe"})
		time.Sleep(200 * time.Millisecond)
	})
	r := gotResult(t, watchAsync(context.Background(), e.target(0, ""), fastPending()))
	if r.End != PendingRefused || e.dials.Load() != 1 {
		t.Fatalf("result %+v dials %d", r, e.dials.Load())
	}
}

func TestPendingClient_F8_CancelEndsPromptlyAnywhere(t *testing.T) {
	runtime.GC()
	base := runtime.NumGoroutine()
	cases := map[string]func(w http.ResponseWriter, r *http.Request, n int) bool{
		"dial": func(_ http.ResponseWriter, r *http.Request, _ int) bool { <-r.Context().Done(); return true },
		"live": nil,
		"wait": func(w http.ResponseWriter, _ *http.Request, _ int) bool {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "x", 503)
			return true
		},
	}
	for name, over := range cases {
		e := newPendingEnv(t, fastConfig(), time.Now().Add(time.Hour), over, nil)
		ctx, cancel := context.WithCancel(context.Background())
		ch := watchAsync(ctx, e.target(0, ""), fastPending())
		waitFor(t, name, func() bool { return e.dials.Load() >= 1 })
		if name == "live" {
			waitFor(t, "link", func() bool { return e.hub.Len() == 1 })
		}
		time.Sleep(50 * time.Millisecond)
		start := time.Now()
		cancel()
		r := gotResult(t, ch)
		if r.End != PendingStopped || time.Since(start) > time.Second {
			t.Fatalf("%s: %+v after %v", name, r, time.Since(start))
		}
	}
	settleGoroutines(t, base+2, "pending client")
}

func TestPendingClient_F9_PlainHTTPRefusedWithoutDial(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	r := WatchPending(context.Background(), PendingTarget{BaseURL: srv.URL, ID: testPairID, Secret: testPairSecret,
		HTTPClient: srv.Client()}, fastPending())
	if r.End != PendingRefused || hits.Load() != 0 {
		t.Fatalf("result %+v hits %d", r, hits.Load())
	}
	if _, ok := pendingLinkURL("http://x:1"); ok {
		t.Fatal("ws:// accepted")
	}
	if u, ok := pendingLinkURL("https://x:1/"); !ok || u != "wss://x:1"+LinkPath {
		t.Fatalf("got %q %v", u, ok)
	}
}

func TestPendingClient_F10_FullJitterBounds(t *testing.T) {
	var wg sync.WaitGroup
	for attempt := -1; attempt <= 40; attempt++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ceil := 30 * time.Second
			if attempt >= 1 && attempt <= 5 {
				ceil = time.Second << (attempt - 1)
			}
			for range 200 {
				if d := fullJitter(attempt, time.Second, 30*time.Second); d < 0 || d > ceil {
					t.Errorf("attempt %d: %v outside [0,%v]", attempt, d, ceil)
					return
				}
			}
		}()
	}
	wg.Wait()
}
