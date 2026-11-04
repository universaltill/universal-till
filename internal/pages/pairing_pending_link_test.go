package pages

import (
	"bytes"
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
)

// The joining till's side of the pending link (ADR-0114 amendment
// 2026-11-03, ut-docs#5330 slice B2). T-numbers are the Architect's; P13 is
// the UX gate's no-op poll.

// syncBuf is a goroutine-safe log sink.
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

type seenCall struct{ method, path, rawQuery, secretHdr string }

type callLog struct {
	mu    sync.Mutex
	calls []seenCall
}

func (l *callLog) add(c seenCall) { l.mu.Lock(); l.calls = append(l.calls, c); l.mu.Unlock() }
func (l *callLog) all() []seenCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]seenCall(nil), l.calls...)
}
func (l *callLog) perID() (out []seenCall) {
	for _, c := range l.all() {
		if c.method == http.MethodGet && strings.HasPrefix(c.path, "/api/sync/pair-requests/") {
			out = append(out, c)
		}
	}
	return out
}

// recordingRT records what the joining till sends through its pinned client.
type recordingRT struct {
	inner http.RoundTripper
	log   *callLog
}

func (rt *recordingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.log.add(seenCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get(fleetlink.HeaderPairRequestSecret)})
	return rt.inner.RoundTrip(r)
}

// seams records the joiner's outbound calls and shrinks the link's timings.
func seams(t *testing.T) *callLog {
	t.Helper()
	log := &callLog{}
	prevPin, prevOpts := newPairPinnedClient, pendingLinkOptions
	newPairPinnedClient = func(expect string) *lantls.PinnedClient {
		pc := lantls.NewPinnedClient(pairJoinHTTPTimeout, expect)
		pc.Client.Transport = &recordingRT{inner: pc.Client.Transport, log: log}
		return pc
	}
	pendingLinkOptions = func() fleetlink.PendingOptions {
		cfg := fleetlink.DefaultConfig()
		cfg.PingInterval = 50 * time.Millisecond
		return fleetlink.PendingOptions{Config: cfg, BackoffMin: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond}
	}
	t.Cleanup(func() { newPairPinnedClient, pendingLinkOptions = prevPin, prevOpts })
	return log
}

// watchSignals wraps the real watchPending, signalling each link-up and each
// return.
func watchSignals(t *testing.T) (ups, ended chan struct{}) {
	t.Helper()
	ups, ended = make(chan struct{}, 16), make(chan struct{}, 16)
	orig := watchPending
	watchPending = func(ctx context.Context, tg fleetlink.PendingTarget, o fleetlink.PendingOptions) fleetlink.PendingResult {
		inner := o.OnUp
		o.OnUp = func(up bool) {
			inner(up)
			if up {
				select {
				case ups <- struct{}{}:
				default:
				}
			}
		}
		res := orig(ctx, tg, o)
		select {
		case ended <- struct{}{}:
		default:
		}
		return res
	}
	t.Cleanup(func() { watchPending = orig })
	return ups, ended
}

// fakeWatch replaces watchPending; calls counts its starts.
func fakeWatch(t *testing.T, f func(ctx context.Context, o fleetlink.PendingOptions) fleetlink.PendingResult) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	orig := watchPending
	watchPending = func(ctx context.Context, _ fleetlink.PendingTarget, o fleetlink.PendingOptions) fleetlink.PendingResult {
		calls.Add(1)
		return f(ctx, o)
	}
	t.Cleanup(func() { watchPending = orig })
	return &calls
}

func holdUp(ctx context.Context, o fleetlink.PendingOptions) fleetlink.PendingResult {
	o.OnUp(true)
	<-ctx.Done()
	o.OnUp(false)
	return fleetlink.PendingResult{End: fleetlink.PendingStopped}
}

// joinClock replaces pairingJoinNow; shift moves it forward.
func joinClock(t *testing.T) (shift func(time.Duration)) {
	t.Helper()
	var off atomic.Int64
	prev := pairingJoinNow
	pairingJoinNow = func() time.Time { return time.Now().Add(time.Duration(off.Load())) }
	t.Cleanup(func() { pairingJoinNow = prev })
	return func(d time.Duration) { off.Add(int64(d)) }
}

func waitCh(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// joiner is a first-boot till with the pairing join API behind the auth
// middleware.
func newJoiner(t *testing.T) http.Handler {
	t.Helper()
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rsvc := auth.NewService(replica.Db)
	replica.AuthSvc = rsvc
	rmux := http.NewServeMux()
	registerPairingJoinAPIT(t, rmux, replica)
	return auth.Middleware(rmux, rsvc)
}

func pairStartVia(t *testing.T, rh http.Handler, base, tillID string) string {
	t.Helper()
	rec := doPostForm(t, rh, "/api/setup/pair-start", url.Values{"base_url": {base}, "till_id": {tillID}, "name": {"Bar Till"}})
	return rec.Body.String()
}

var haveRe = regexp.MustCompile(`"have":"([^"]*)"`)

func haveOf(body string) string {
	if m := haveRe.FindStringSubmatch(html.UnescapeString(body)); m != nil {
		return m[1]
	}
	return ""
}

func pollStatus(t *testing.T, rh http.Handler, have string) *httptest.ResponseRecorder {
	t.Helper()
	return doGet(t, rh, "/api/setup/pair-status?have="+url.QueryEscape(have))
}

// realRig is a joiner against the real B1 main till (pending hub, link
// route, per-id GET, limiter) over pinned TLS.
type realRig struct {
	f        *pendingLinkFixture
	rh       http.Handler
	tillID   string
	log      *callLog
	ups, end chan struct{}
}

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	f := newPendingLinkFixture(t, fixtureOpts{})
	// The fixture has no DB path, so the main till's enrolment snapshot would
	// land in ./backups of the repo root and break later tests.
	f.dp.Cfg.DBPath = filepath.Join(t.TempDir(), "primary.db")
	if err := f.dp.Settings.Set(t.Context(), "store.name", "Corner Shop"); err != nil {
		t.Fatal(err)
	}
	id, err := discovery.TillID(t.Context(), data.NewSettingsRepo(f.dp.Db))
	if err != nil {
		t.Fatal(err)
	}
	r := &realRig{f: f, tillID: id, log: seams(t)}
	r.ups, r.end = watchSignals(t)
	r.rh = newJoiner(t)
	return r
}

func (r *realRig) pendingID(t *testing.T) string {
	t.Helper()
	rows, err := r.f.repo.ListPending(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending rows %+v err %v", rows, err)
	}
	return rows[0].ID
}

func TestPendingJoin_P1_P2_ApprovalReachesJoinerWithinTwoSeconds(t *testing.T) {
	var logs syncBuf
	defer logging.CaptureForTest(&logs)()
	r := newRealRig(t)
	body := pairStartVia(t, r.rh, r.f.base, r.tillID)
	if !strings.Contains(body, "every 1s") {
		t.Fatalf("first waiting render must poll every 1s: %s", body)
	}
	waitCh(t, r.ups, "link up")
	have := haveOf(body)
	for range 5 { // quiet while the link is up: no upstream GET
		if rec := pollStatus(t, r.rh, have); rec.Code != http.StatusNoContent {
			t.Fatalf("idle poll: %d %s", rec.Code, rec.Body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := len(r.log.perID()); n != 0 {
		t.Fatalf("%d per-id GET(s) before approval, want 0", n)
	}
	id := r.pendingID(t)
	start := time.Now()
	r.f.approve(t, id)
	var rec *httptest.ResponseRecorder
	for {
		rec = pollStatus(t, r.rh, have)
		if strings.Contains(rec.Body.String(), "Corner Shop") {
			break
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("joiner not joined %v after approval: %d %s", time.Since(start), rec.Code, rec.Body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	gets := r.log.perID()
	if len(gets) == 0 {
		t.Fatal("no per-id GET")
	}
	secret := gets[0].secretHdr
	for _, g := range gets {
		if g.rawQuery != "" || g.secretHdr == "" {
			t.Fatalf("per-id GET %+v: want header secret and empty query", g)
		}
	}
	for _, c := range r.log.all() {
		if strings.Contains(c.path+c.rawQuery, secret) || strings.Contains(c.rawQuery, "request_secret") {
			t.Fatalf("secret in a URL: %+v", c)
		}
	}
	// P11: nothing sensitive in the log across the whole run.
	host := strings.TrimPrefix(r.f.base, "http://")
	for _, bad := range []string{id, secret, host, "X-UT-"} {
		if strings.Contains(logs.String(), bad) {
			t.Fatalf("log contains %q:\n%s", bad, logs.String())
		}
	}
}

func TestPendingJoin_P5_DenyShowsNoResponseAtOnce(t *testing.T) {
	var logs syncBuf
	defer logging.CaptureForTest(&logs)()
	r := newRealRig(t)
	body := pairStartVia(t, r.rh, r.f.base, r.tillID)
	waitCh(t, r.ups, "link up")
	id := r.pendingID(t)
	r.f.deny(t, id)
	deadline := time.Now().Add(2 * time.Second)
	var rec *httptest.ResponseRecorder
	for {
		rec = pollStatus(t, r.rh, haveOf(body))
		if rec.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deny not shown within 2 s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if strings.Contains(rec.Body.String(), "hx-trigger") || strings.Contains(rec.Body.String(), "hx-get") {
		t.Fatalf("terminal render must not poll: %s", rec.Body)
	}
	waitCh(t, r.end, "watch ended")
	if n := len(r.log.perID()); n != 0 {
		t.Fatalf("a denied request needed %d per-id GET(s), want 0", n)
	}
	if strings.Contains(logs.String(), id) {
		t.Fatalf("id in log:\n%s", logs.String())
	}
}

func TestPendingJoin_P7_LinkBlockedFallsBackToFifteenSecondPoll(t *testing.T) {
	shift := joinClock(t)
	r := newRealRig(t)
	hub := r.f.dp.PendingLinks
	r.f.dp.PendingLinks = nil // the main till answers 503 to the link
	t.Cleanup(func() { r.f.dp.PendingLinks = hub })
	body := pairStartVia(t, r.rh, r.f.base, r.tillID)
	if !strings.Contains(body, "every 1s") {
		t.Fatalf("a connecting link polls fast: %s", body)
	}
	r.f.approve(t, r.pendingID(t))
	if rec := pollStatus(t, r.rh, ""); strings.Contains(rec.Body.String(), "Corner Shop") || len(r.log.perID()) != 0 {
		t.Fatal("asked the main till before the 15 s cadence")
	}
	shift(16 * time.Second)
	rec := pollStatus(t, r.rh, "")
	if !strings.Contains(rec.Body.String(), "Corner Shop") {
		t.Fatalf("join did not complete on the 15 s cadence: %s", rec.Body)
	}
}

// fakeMain is a main till that answers just enough of the pairing API.
type fakeMain struct {
	base   string
	status atomic.Int32 // answer to the per-id GET (404 default)
	log    callLog
}

func newFakeMain(t *testing.T, tlsOn, pendingLink bool) *fakeMain {
	t.Helper()
	fm := &fakeMain{}
	fm.status.Store(http.StatusNotFound)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sync/pair-request":
			w.Header().Set("Content-Type", "application/json")
			if pendingLink {
				_, _ = w.Write([]byte(`{"data":{"id":"fake-1","pending_link":true},"error":null}`))
			} else {
				_, _ = w.Write([]byte(`{"data":{"id":"fake-1"},"error":null}`))
			}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/sync/pair-requests/"):
			fm.log.add(seenCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get(fleetlink.HeaderPairRequestSecret)})
			w.WriteHeader(int(fm.status.Load()))
		default:
			http.NotFound(w, r)
		}
	})
	if tlsOn {
		fm.base, _ = serveTLSTill(t, h)
	} else {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		fm.base = srv.URL
	}
	return fm
}

func TestPendingJoin_P3_OlderMainTillKeepsQueryFormAndFifteenSeconds(t *testing.T) {
	seams(t)
	calls := fakeWatch(t, holdUp)
	fm := newFakeMain(t, true, false)
	rh := newJoiner(t)
	body := pairStartVia(t, rh, fm.base, "till-x")
	if !strings.Contains(body, "every 15s") || calls.Load() != 0 {
		t.Fatalf("no pending_link: want every 15s and no watch (calls %d): %s", calls.Load(), body)
	}
	pollStatus(t, rh, "")
	g := fm.log.perID()
	if len(g) != 1 || !strings.HasPrefix(g[0].rawQuery, "request_secret=") || g[0].secretHdr != "" {
		t.Fatalf("older main till must get today's query form: %+v", g)
	}
}

func TestPendingJoin_P4_PlainHTTPNoLinkHeaderFormEmptyQuery(t *testing.T) {
	seams(t)
	calls := fakeWatch(t, holdUp)
	fm := newFakeMain(t, false, true)
	rh := newJoiner(t)
	body := pairStartVia(t, rh, fm.base, "till-x")
	if !strings.Contains(body, "every 15s") || calls.Load() != 0 {
		t.Fatalf("unpinned: want every 15s and no watch (calls %d): %s", calls.Load(), body)
	}
	pollStatus(t, rh, "")
	g := fm.log.perID()
	if len(g) != 1 || g[0].rawQuery != "" || g[0].secretHdr == "" {
		t.Fatalf("pending_link answer: header form with an empty query: %+v", g)
	}
}

func TestPendingJoin_P6_ThrottleWithLinkUp(t *testing.T) {
	shift := joinClock(t)
	seams(t)
	up := make(chan struct{})
	var once sync.Once
	fakeWatch(t, func(ctx context.Context, o fleetlink.PendingOptions) fleetlink.PendingResult {
		o.OnUp(true)
		once.Do(func() { close(up) })
		<-ctx.Done()
		o.OnUp(false)
		return fleetlink.PendingResult{End: fleetlink.PendingStopped}
	})
	fm := newFakeMain(t, true, true)
	rh := newJoiner(t)
	pairStartVia(t, rh, fm.base, "till-x")
	waitCh(t, up, "link up")
	for range 59 {
		shift(time.Second)
		pollStatus(t, rh, "")
	}
	if n := len(fm.log.perID()); n != 0 {
		t.Fatalf("%d GET(s) in 59 s with the link up, want 0", n)
	}
	shift(time.Second)
	pollStatus(t, rh, "")
	if n := len(fm.log.perID()); n != 1 {
		t.Fatalf("%d GET(s) at 60 s, want exactly 1 (safety net)", n)
	}
}

func TestPendingJoin_P6_ThrottleLinkNotUpAndApprovedRetry(t *testing.T) {
	shift := joinClock(t)
	seams(t)
	fakeWatch(t, func(ctx context.Context, _ fleetlink.PendingOptions) fleetlink.PendingResult {
		<-ctx.Done() // connecting, never up
		return fleetlink.PendingResult{End: fleetlink.PendingStopped}
	})
	fm := newFakeMain(t, true, true)
	rh := newJoiner(t)
	pairStartVia(t, rh, fm.base, "till-x")
	for range 60 {
		shift(time.Second)
		pollStatus(t, rh, "")
	}
	if n := len(fm.log.perID()); n != 4 {
		t.Fatalf("%d GET(s) in 60 s with the link not up, want 4 (one per 15 s)", n)
	}
}

func TestPendingJoin_P6_ApprovedFrameRetriesEveryFiveSeconds(t *testing.T) {
	shift := joinClock(t)
	seams(t)
	fakeWatch(t, func(context.Context, fleetlink.PendingOptions) fleetlink.PendingResult {
		return fleetlink.PendingResult{End: fleetlink.PendingResolved, Status: fleetlink.PairingApproved}
	})
	fm := newFakeMain(t, true, true)
	fm.status.Store(http.StatusTooManyRequests)
	rh := newJoiner(t)
	pairStartVia(t, rh, fm.base, "till-x")
	// The fake watch reports approval at once; the first tick that sees it
	// asks the main till (earlier ticks, before the watch goroutine has
	// stored the result, are still "connecting" and quiet).
	for deadline := time.Now().Add(2 * time.Second); len(fm.log.perID()) == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		pollStatus(t, rh, "")
	}
	// The first tick after approval asks at once; a 429 is retried every 5 s.
	base := len(fm.log.perID())
	if base != 1 {
		t.Fatalf("first tick after approval made %d GET(s), want 1", base)
	}
	for range 11 {
		shift(time.Second)
		pollStatus(t, rh, "")
	}
	if n := len(fm.log.perID()) - base; n != 2 {
		t.Fatalf("%d retries in 11 s, want 2 (at 5 s and 10 s)", n)
	}
}

func TestPendingJoin_P7_RefusedLinkAsksEveryTick(t *testing.T) {
	seams(t)
	fakeWatch(t, func(context.Context, fleetlink.PendingOptions) fleetlink.PendingResult {
		return fleetlink.PendingResult{End: fleetlink.PendingRefused, HTTPStatus: 401}
	})
	fm := newFakeMain(t, true, true)
	rh := newJoiner(t)
	pairStartVia(t, rh, fm.base, "till-x")
	// Wait for the first tick that sees the ended link to ask the main till.
	for deadline := time.Now().Add(2 * time.Second); len(fm.log.perID()) == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		pollStatus(t, rh, "")
	}
	base := len(fm.log.perID())
	for range 3 {
		rec := pollStatus(t, rh, "")
		if !strings.Contains(rec.Body.String(), "every 15s") && rec.Code != http.StatusNoContent {
			t.Fatalf("an ended link falls back to 15 s: %s", rec.Body)
		}
	}
	if n := len(fm.log.perID()) - base; base != 1 || n != 3 {
		t.Fatalf("first ask %d, then %d GET(s) in 3 ticks after the link ended, want 1 and 3", base, n)
	}
}

func TestPendingJoin_P8_SecondPairStartStopsTheFirstWatch(t *testing.T) {
	seams(t)
	var mu sync.Mutex
	var stopped []chan struct{}
	fakeWatch(t, func(ctx context.Context, o fleetlink.PendingOptions) fleetlink.PendingResult {
		ch := make(chan struct{})
		mu.Lock()
		stopped = append(stopped, ch)
		mu.Unlock()
		defer close(ch)
		return holdUp(ctx, o)
	})
	fm := newFakeMain(t, true, true)
	rh := newJoiner(t)
	pairStartVia(t, rh, fm.base, "till-x")
	pairStartVia(t, rh, fm.base, "till-x")
	mu.Lock()
	defer mu.Unlock()
	if len(stopped) != 2 {
		t.Fatalf("%d watches started, want 2", len(stopped))
	}
	waitCh(t, stopped[0], "first watch stopped")
	select {
	case <-stopped[1]:
		t.Fatal("the new attempt's watch was stopped")
	default:
	}
}

func TestPendingJoin_P9_ShutdownJoinsEveryWatchAndStartAfterCloseIsNil(t *testing.T) {
	fakeWatch(t, holdUp)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	r := newPendingLinkRunner(ctx, &wg)
	w1 := r.start(pendingLinkAttempt{ttlLeft: time.Hour})
	w2 := r.start(pendingLinkAttempt{ttlLeft: time.Hour})
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	waitCh(t, done, "wg.Wait after bgCtx cancel")
	for _, w := range []*pendingLinkWatch{w1, w2} {
		select {
		case <-w.done:
		default:
			t.Fatal("watch still running after shutdown")
		}
	}
	if r.start(pendingLinkAttempt{ttlLeft: time.Hour}) != nil {
		t.Fatal("start after shutdown must return nil")
	}
	(*pendingLinkWatch)(nil).stop() // nil-safe
}

func TestPendingJoin_P9_PanicInWatchIsRecoveredAndMarksEnded(t *testing.T) {
	fakeWatch(t, func(context.Context, fleetlink.PendingOptions) fleetlink.PendingResult { panic("boom") })
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	r := newPendingLinkRunner(ctx, &wg)
	w := r.start(pendingLinkAttempt{ttlLeft: time.Hour})
	w.stop()
	if !w.ended.Load() || w.up.Load() {
		t.Fatal("a panicking watch must end cleanly")
	}
	cancel()
	wg.Wait()
}

func TestPendingJoin_P10_TTLEndsTheWatchWithoutAResult(t *testing.T) {
	var ctxErr atomic.Value
	fakeWatch(t, func(ctx context.Context, o fleetlink.PendingOptions) fleetlink.PendingResult {
		res := holdUp(ctx, o)
		ctxErr.Store(ctx.Err())
		return res
	})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	r := newPendingLinkRunner(ctx, &wg)
	w := r.start(pendingLinkAttempt{ttlLeft: 50 * time.Millisecond})
	waitCh(t, w.done, "watch end at its TTL")
	if ctxErr.Load() != context.DeadlineExceeded || w.approved.Load() || w.terminal.Load() != nil || !w.ended.Load() {
		t.Fatalf("ctx err %v approved %v terminal %v", ctxErr.Load(), w.approved.Load(), w.terminal.Load())
	}
	cancel()
	wg.Wait()
}

func TestPendingJoin_P10_PairStatusTTLBranchStopsTheWatch(t *testing.T) {
	shift := joinClock(t)
	seams(t)
	ended := make(chan struct{})
	fakeWatch(t, func(ctx context.Context, o fleetlink.PendingOptions) fleetlink.PendingResult {
		defer close(ended)
		return holdUp(ctx, o)
	})
	fm := newFakeMain(t, true, true)
	rh := newJoiner(t)
	pairStartVia(t, rh, fm.base, "till-x")
	shift(pairingRequestTTL + time.Second)
	rec := pollStatus(t, rh, "")
	if strings.Contains(rec.Body.String(), "hx-trigger") {
		t.Fatalf("expired render must not poll: %s", rec.Body)
	}
	waitCh(t, ended, "watch stopped by the TTL branch")
}

// P13 (UX gate A): a poll where nothing changed leaves the DOM alone, and a
// real change still renders. The key is echoed by the browser, so several
// tabs each get their own answer.
func TestPendingJoin_P13_UnchangedPollIs204AndChangesStillRender(t *testing.T) {
	seams(t)
	release := make(chan struct{})
	fakeWatch(t, func(ctx context.Context, o fleetlink.PendingOptions) fleetlink.PendingResult {
		o.OnUp(true)
		select {
		case <-release:
		case <-ctx.Done():
		}
		o.OnUp(false)
		return fleetlink.PendingResult{End: fleetlink.PendingRefused, HTTPStatus: 401}
	})
	fm := newFakeMain(t, true, true)
	rh := newJoiner(t)
	body := pairStartVia(t, rh, fm.base, "till-x")
	have := haveOf(body)
	if !strings.HasSuffix(have, "/1s") {
		t.Fatalf("first render's key %q", have)
	}
	for range 3 {
		if rec := pollStatus(t, rh, have); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("unchanged poll: %d %q", rec.Code, rec.Body)
		}
	}
	// Another tab (no key, or a stale one) always gets the screen.
	for _, h := range []string{"", "stale/1s"} {
		if rec := pollStatus(t, rh, h); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "every 1s") {
			t.Fatalf("poll with key %q: %d %s", h, rec.Code, rec.Body)
		}
	}
	// The link ends: the interval changes, so this poll renders once...
	close(release)
	var rec *httptest.ResponseRecorder
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if rec = pollStatus(t, rh, have); rec.Code == http.StatusOK {
			break
		}
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "every 15s") {
		t.Fatalf("interval switch must render: %d %s", rec.Code, rec.Body)
	}
	// ...and the next identical poll is a no-op again.
	if rec2 := pollStatus(t, rh, haveOf(rec.Body.String())); rec2.Code != http.StatusNoContent {
		t.Fatalf("after the switch: %d %s", rec2.Code, rec2.Body)
	}
}

func TestPendingJoin_P6_DecisionTables(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	mk := func(f func(w *pendingLinkWatch)) *pendingJoinState {
		s := &pendingJoinState{status: "waiting", lastUpstream: t0}
		if f != nil {
			s.link = &pendingLinkWatch{}
			f(s.link)
		}
		return s
	}
	cases := []struct {
		name     string
		s        *pendingJoinState
		at       time.Duration
		due      bool
		pollFast bool
	}{
		{"no link", mk(nil), time.Second, true, false},
		{"connecting <15s", mk(func(*pendingLinkWatch) {}), 14 * time.Second, false, true},
		{"connecting 15s", mk(func(*pendingLinkWatch) {}), 15 * time.Second, true, true},
		{"up 59s", mk(func(w *pendingLinkWatch) { w.up.Store(true) }), 59 * time.Second, false, true},
		{"up 60s", mk(func(w *pendingLinkWatch) { w.up.Store(true) }), 60 * time.Second, true, true},
		{"ended", mk(func(w *pendingLinkWatch) { w.ended.Store(true) }), time.Second, true, false},
		{"approved first try", mk(func(w *pendingLinkWatch) { w.approved.Store(true); w.ended.Store(true) }), time.Second, true, true},
	}
	for _, c := range cases {
		if got := c.s.upstreamDue(t0.Add(c.at)); got != c.due {
			t.Errorf("%s: upstreamDue = %v, want %v", c.name, got, c.due)
		}
		want := pairPollSlow
		if c.pollFast {
			want = pairPollFast
		}
		if got := c.s.pollEvery(); got != want {
			t.Errorf("%s: pollEvery = %s, want %s", c.name, got, want)
		}
	}
	ap := mk(func(w *pendingLinkWatch) { w.approved.Store(true) })
	ap.lastApprovedTry = t0
	if ap.upstreamDue(t0.Add(4*time.Second)) || !ap.upstreamDue(t0.Add(5*time.Second)) {
		t.Error("approved retry must wait exactly 5 s")
	}
	done := mk(func(w *pendingLinkWatch) { st := fleetlink.PairingDenied; w.terminal.Store(&st) })
	if !done.linkTerminal() || mk(nil).linkTerminal() {
		t.Error("linkTerminal")
	}
	notWaiting := mk(func(*pendingLinkWatch) {})
	notWaiting.status = "error"
	if notWaiting.pollEvery() != pairPollSlow {
		t.Error("only a waiting render polls fast")
	}
}

// P12: the template emits exactly the two literals and only a live link
// polls fast.
func TestPendingJoin_P12_TemplateIntervals(t *testing.T) {
	chdirRoot(t)
	render := func(f func(w http.ResponseWriter, r *http.Request)) string {
		rec := httptest.NewRecorder()
		f(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		return rec.Body.String()
	}
	live := &pendingJoinState{status: "waiting", commitment: "c", primaryTillID: "t", link: &pendingLinkWatch{}}
	if b := render(func(w http.ResponseWriter, r *http.Request) {
		pairWaitWaiting(w, r, "/api/sync/pair-status", live, false)
	}); !strings.Contains(b, `hx-trigger="every 1s"`) {
		t.Fatalf("live link: %s", b)
	}
	live.link.ended.Store(true)
	nolink := &pendingJoinState{status: "waiting", commitment: "c", primaryTillID: "t"}
	for name, s := range map[string]*pendingJoinState{"ended": live, "no link": nolink} {
		if b := render(func(w http.ResponseWriter, r *http.Request) { pairWaitWaiting(w, r, "/api/sync/pair-status", s, false) }); !strings.Contains(b, `hx-trigger="every 15s"`) {
			t.Fatalf("%s: %s", name, b)
		}
	}
	for _, st := range []string{"error", "joined", "expired", "idle"} {
		b := render(func(w http.ResponseWriter, r *http.Request) {
			pairWaitViewPolling(w, r, "/api/sync/pair-status", st, "", "", "x", st == "error")
		})
		if strings.Contains(b, "every 1s") || (st == "error") != strings.Contains(b, `every 15s`) {
			t.Fatalf("%s: %s", st, b)
		}
	}
}
