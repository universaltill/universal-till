package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0148 amendment (2026-10-05, ut-docs#3673): an unpaid till that holds
// a store identity checks in once per start, and only when its version or
// today's local date differs from what its last answered unpaid check-in
// recorded (first run, first start after an update, first start of the
// day). No timer, no retry after a 402; the check-in POSTs the device
// report directly (no conditional GET), and any cloud answer (200 or 402)
// records version + date. Transport errors, 5xx and 401 record nothing.

// startupCloud records every request as "METHOD /path" and answers
// /v1/stores/sync with syncStatus (0 = 200 with a local/none block).
type startupCloud struct {
	mu         sync.Mutex
	reqs       []string
	syncStatus int
}

func (c *startupCloud) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.reqs = append(c.reqs, r.Method+" "+r.URL.Path)
		status := c.syncStatus
		c.mu.Unlock()
		if r.URL.Path != "/v1/stores/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
			return
		}
		switch status {
		case 0:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"directives":  []any{},
				"entitlement": json.RawMessage(planRequiredBlock),
			}})
		case http.StatusPaymentRequired:
			writePlanRequired(w)
		case http.StatusUnauthorized:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(unauthorizedBody("device_revoked")))
		default:
			w.WriteHeader(status)
		}
	})
}

func (c *startupCloud) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.reqs...)
}

func (c *startupCloud) count(req string) int {
	n := 0
	for _, r := range c.requests() {
		if r == req {
			n++
		}
	}
	return n
}

const (
	reqSyncPost   = "POST /v1/stores/sync"
	reqCheckinGet = "GET /v1/stores/checkin"
)

// startupDay is the pinned "today" of these tests (the till's local clock).
var startupDay = time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)

// pinOperatorNow pins the gate's wall clock and the start-up hour's clock
// to at, and returns a setter that moves both (no NTP jump).
func pinOperatorNow(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	setWall := pinWallClock(t, at)
	setMono := pinStartupClock(t, at)
	return func(n time.Time) { setWall(n); setMono(n) }
}

// pinWallClock pins operatorNow alone (the clock the date comes from).
func pinWallClock(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	var mu sync.Mutex
	cur := at
	orig := operatorNow
	operatorNow = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	t.Cleanup(func() { operatorNow = orig })
	return func(n time.Time) { mu.Lock(); cur = n; mu.Unlock() }
}

// pinStartupClock pins the start-up hour's (monotonic) clock alone.
func pinStartupClock(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	var mu sync.Mutex
	cur := at
	orig := startupClock
	startupClock = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	t.Cleanup(func() { startupClock = orig })
	return func(n time.Time) { mu.Lock(); cur = n; mu.Unlock() }
}

func pinVersion(t *testing.T, v string) {
	t.Helper()
	orig := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = orig })
}

func seedUnpaidMarkers(t *testing.T, db *sql.DB, version, date string) {
	t.Helper()
	if err := data.NewSettingsRepo(db).SetMany(context.Background(), map[string]string{
		keyUnpaidCheckinVersion: version,
		keyUnpaidCheckinDate:    date,
	}); err != nil {
		t.Fatalf("seed markers: %v", err)
	}
}

func unpaidMarkers(t *testing.T, db *sql.DB) (version, date string) {
	t.Helper()
	repo := data.NewSettingsRepo(db)
	version, _, _ = repo.Get(context.Background(), keyUnpaidCheckinVersion)
	date, _, _ = repo.Get(context.Background(), keyUnpaidCheckinDate)
	return version, date
}

// startupFixture: the real gate, an unpaid (no entitlement cache) till,
// the clock and version pinned, and a recording cloud.
func startupFixture(t *testing.T, syncStatus int) (*startupCloud, *httptest.Server, *sql.DB, func(time.Time)) {
	t.Helper()
	useRealSyncGate(t)
	resetTillAuth(t)
	setNow := pinOperatorNow(t, startupDay)
	pinVersion(t, "0.30.20")
	cloud := &startupCloud{syncStatus: syncStatus}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)
	return cloud, srv, testDB(t), setNow
}

// armStartup does what Start does before its goroutine: arm the one-shot.
func armStartup() { armStartupCheckin() }

// Driven through Start against today's cloud (it answers an unpaid store
// 402): first run (nothing recorded) → exactly one call, the sync POST
// with the device report, no conditional GET; then both markers are
// recorded.
func TestStartupCheckinFirstRunPostsOnceAndRecords(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, http.StatusPaymentRequired)
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	origNew := newSchedulerFn
	t.Cleanup(func() { firstDelayNS.Store(origFirst); tickIntervalNS.Store(origTick); newSchedulerFn = origNew })
	firstDelayNS.Store(int64(time.Millisecond))
	tickIntervalNS.Store(int64(time.Hour))
	newSchedulerFn = func() *scheduler { return &scheduler{rng: func() float64 { return 0.5 }} }

	ticked := make(chan struct{})
	var once sync.Once
	hooks := Hooks{AfterTick: func(context.Context, bool, error) { once.Do(func() { close(ticked) }) }}
	ctx, cancel := context.WithCancel(context.Background())
	wg := startJoined(t, ctx, cancel, testCfg(srv.URL), db, hooks)
	select {
	case <-ticked:
	case <-time.After(3 * time.Second):
		t.Fatal("the first tick never ran")
	}
	cancel()
	waitJoined(t, wg)

	if got := cloud.requests(); len(got) != 1 || got[0] != reqSyncPost {
		t.Fatalf("start-up check-in requests = %v, want exactly [%s]", got, reqSyncPost)
	}
	if v, d := unpaidMarkers(t, db); v != "0.30.20" || d != "2026-10-05" {
		t.Fatalf("markers = (%q, %q), want (0.30.20, 2026-10-05)", v, d)
	}
}

// The POST carries the device report, version included: that is what the
// cloud records for an unpaid till (ut-docs#3716).
func TestStartupCheckinPostCarriesDeviceVersion(t *testing.T) {
	_, _, db, _ := startupFixture(t, 0)
	cloud := &fakeCloud{entitlement: json.RawMessage(planRequiredBlock)}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	armStartup()
	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if len(cloud.syncBodies) != 1 {
		t.Fatalf("sync POSTs = %d, want 1", len(cloud.syncBodies))
	}
	devs, _ := cloud.syncBodies[0]["devices"].([]any)
	if len(devs) != 1 {
		t.Fatalf("devices = %v, want one device report", cloud.syncBodies[0]["devices"])
	}
	if dev, _ := devs[0].(map[string]any); dev["version"] != "0.30.20" {
		t.Fatalf("device version = %v, want 0.30.20", dev["version"])
	}
}

func TestStartupCheckinSkippedWhenVersionAndDateCurrent(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, 0)
	seedUnpaidMarkers(t, db, "0.30.20", "2026-10-05")
	armStartup()
	contacted, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{})
	if contacted || err != nil {
		t.Fatalf("tick = (%v, %v), want a quiet gated tick", contacted, err)
	}
	if got := cloud.requests(); len(got) != 0 {
		t.Fatalf("a second start on the same day made requests %v, want none", got)
	}
}

func TestStartupCheckinAfterVersionChange(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, 0)
	seedUnpaidMarkers(t, db, "0.30.19", "2026-10-05")
	armStartup()
	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if cloud.count(reqSyncPost) != 1 || cloud.count(reqCheckinGet) != 0 {
		t.Fatalf("after an update: requests = %v, want one %s and no GET", cloud.requests(), reqSyncPost)
	}
	if v, _ := unpaidMarkers(t, db); v != "0.30.20" {
		t.Fatalf("version marker = %q, want 0.30.20", v)
	}
}

func TestStartupCheckinOnFirstStartOfDay(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, 0)
	seedUnpaidMarkers(t, db, "0.30.20", "2026-10-04")
	armStartup()
	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if cloud.count(reqSyncPost) != 1 || cloud.count(reqCheckinGet) != 0 {
		t.Fatalf("first start of the day: requests = %v, want one %s and no GET", cloud.requests(), reqSyncPost)
	}
	if _, d := unpaidMarkers(t, db); d != "2026-10-05" {
		t.Fatalf("date marker = %q, want 2026-10-05", d)
	}
}

// One check-in per start, never a timer: a till left running for two days
// (ticks every 2 minutes, days rolling over) makes no further call.
func TestStartupCheckinOncePerStartNoTimer(t *testing.T) {
	cloud, srv, db, setNow := startupFixture(t, 0)
	cfg := testCfg(srv.URL)
	armStartup()
	if _, err := tick(context.Background(), cfg, db, Hooks{}); err != nil {
		t.Fatalf("start-up tick: %v", err)
	}
	if n := cloud.count(reqSyncPost); n != 1 {
		t.Fatalf("start-up tick made %d sync POSTs, want 1", n)
	}
	cloud.mu.Lock()
	cloud.reqs = nil // the 200 lets the rest of that tick run (ADR-0148 §6)
	cloud.mu.Unlock()
	for at := startupDay.Add(2 * time.Minute); at.Before(startupDay.Add(48 * time.Hour)); at = at.Add(2 * time.Minute) {
		setNow(at)
		if contacted, err := tick(context.Background(), cfg, db, Hooks{}); contacted || err != nil {
			t.Fatalf("tick at %v = (%v, %v), want quiet", at, contacted, err)
		}
	}
	if got := cloud.requests(); len(got) != 0 {
		t.Fatalf("48h without a restart made requests %v, want none after the start-up one", got)
	}
}

// No answer from the cloud records nothing, so the next trigger tries again.
func TestStartupCheckinWithoutAnswerRecordsNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int // 0 = the server is gone (transport error)
	}{
		{name: "transport_error"},
		{name: "bad_gateway", status: http.StatusBadGateway},
		{name: "unavailable", status: http.StatusServiceUnavailable},
		{name: "too_many_requests", status: http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, srv, db, _ := startupFixture(t, tc.status)
			if tc.status == 0 {
				srv.Close()
			}
			armStartup()
			if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err == nil {
				t.Fatal("tick = nil error, want the start-up check-in's failure")
			}
			if v, d := unpaidMarkers(t, db); v != "" || d != "" {
				t.Fatalf("markers = (%q, %q) after no answer, want none", v, d)
			}
		})
	}
}

// A 402 is an answer: the cloud saw the till, so version + date are
// recorded and the next start on the same day makes no call.
func TestStartupCheckin402RecordsMarkers(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, http.StatusPaymentRequired)
	armStartup()
	_, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{})
	if _, ok := planRequired(err); !ok {
		t.Fatalf("tick err = %v, want the 402", err)
	}
	if got := cloud.requests(); len(got) != 1 || got[0] != reqSyncPost {
		t.Fatalf("requests = %v, want exactly [%s]", got, reqSyncPost)
	}
	if v, d := unpaidMarkers(t, db); v != "0.30.20" || d != "2026-10-05" {
		t.Fatalf("markers = (%q, %q), want (0.30.20, 2026-10-05)", v, d)
	}
	armStartup() // a second start the same day
	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if n := len(cloud.requests()); n != 1 {
		t.Fatalf("second start the same day made %d new requests, want 0", n-1)
	}
}

// A 402 inside an operator window closes the window: no retry until the
// next trigger.
func TestPlanRequiredClosesOperatorWindow(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, http.StatusPaymentRequired)
	cfg := testCfg(srv.URL)
	OpenOperatorWindow(15 * time.Minute)
	_, err := tick(context.Background(), cfg, db, Hooks{})
	if _, ok := planRequired(err); !ok {
		t.Fatalf("tick err = %v, want the 402", err)
	}
	if got := cloud.requests(); len(got) != 1 || got[0] != reqSyncPost {
		t.Fatalf("in-window unpaid check-in requests = %v, want exactly [%s] (no GET)", got, reqSyncPost)
	}
	if operatorWindowOpen(operatorNow()) {
		t.Fatal("the operator window is still open after a 402")
	}
	if v, d := unpaidMarkers(t, db); v != "0.30.20" || d != "2026-10-05" {
		t.Fatalf("markers = (%q, %q), want an in-window answer recorded too", v, d)
	}
	if contacted, err := tick(context.Background(), cfg, db, Hooks{}); contacted || err != nil {
		t.Fatalf("next tick = (%v, %v), want quiet", contacted, err)
	}
	if n := len(cloud.requests()); n != 1 {
		t.Fatalf("after the 402 the till made %d more requests, want 0", n-1)
	}
}

// An unpaid in-window check-in the cloud answers records the markers.
func TestOperatorWindowAnswerRecordsMarkers(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, 0)
	OpenOperatorWindow(2 * time.Minute)
	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if cloud.count(reqCheckinGet) != 0 || cloud.count(reqSyncPost) != 1 {
		t.Fatalf("requests = %v, want one POST and no GET", cloud.requests())
	}
	if v, d := unpaidMarkers(t, db); v != "0.30.20" || d != "2026-10-05" {
		t.Fatalf("markers = (%q, %q), want (0.30.20, 2026-10-05)", v, d)
	}
}

// A 401 on the start-up check-in records nothing (no real answer about
// this till) and later ticks stay quiet: no hourly lock-out retry.
func TestStartupCheckin401ThenQuiet(t *testing.T) {
	cloud, srv, db, setNow := startupFixture(t, http.StatusUnauthorized)
	cfg := testCfg(srv.URL)
	sched := &scheduler{rng: func() float64 { return 0.5 }, auth: tillAuth}
	armStartup()
	_, err := tick(context.Background(), cfg, db, Hooks{})
	var se *statusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tick err = %v, want the 401", err)
	}
	if v, d := unpaidMarkers(t, db); v != "" || d != "" {
		t.Fatalf("markers = (%q, %q) after a 401, want none", v, d)
	}
	at := startupDay
	for i := 0; i < 24; i++ { // a day of hourly lock-out waits
		at = at.Add(sched.next(err))
		setNow(at)
		var contacted bool
		contacted, err = tick(context.Background(), cfg, db, Hooks{})
		if contacted || err != nil {
			t.Fatalf("tick %d = (%v, %v), want quiet", i, contacted, err)
		}
	}
	if n := len(cloud.requests()); n != 1 {
		t.Fatalf("after the 401 the till made %d more requests, want 0", n-1)
	}
}

// No store identity → no call (ADR-0015's lazy registration unchanged),
// and the one-shot is spent: a registration later in the run doesn't turn
// the next tick into a start-up check-in.
func TestStartupCheckinUnregisteredConsumesFlag(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, 0)
	unregistered := &config.Config{}
	unregistered.DBPath = "/nonexistent"
	armStartup()
	if contacted, err := tick(context.Background(), unregistered, db, Hooks{}); contacted || err != nil {
		t.Fatalf("unregistered tick = (%v, %v), want quiet", contacted, err)
	}
	if startupCheckinArmedAt.Load() != nil {
		t.Fatal("an unregistered first tick did not disarm the start-up check-in")
	}
	if contacted, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); contacted || err != nil {
		t.Fatalf("tick after registering = (%v, %v), want quiet", contacted, err)
	}
	if got := cloud.requests(); len(got) != 0 {
		t.Fatalf("requests = %v, want none", got)
	}
}

// A settings read failure fails quiet: no call, no error.
func TestStartupCheckinSettingsErrorFailsQuiet(t *testing.T) {
	cloud, srv, _, _ := startupFixture(t, 0)
	noSettings, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = noSettings.Close() })
	armStartup()
	if contacted, err := tick(context.Background(), testCfg(srv.URL), noSettings, Hooks{}); contacted || err != nil {
		t.Fatalf("tick = (%v, %v), want quiet", contacted, err)
	}
	if got := cloud.requests(); len(got) != 0 {
		t.Fatalf("requests = %v, want none", got)
	}
}

// The paid path is unchanged: the conditional GET first, and no unpaid
// markers.
func TestStartupPaidTillKeepsConditionalGet(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, 0)
	seedEntitlement(t, db, "shop", "active", time.Now())
	armStartup()
	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got := cloud.requests()
	if len(got) < 2 || got[0] != reqCheckinGet || got[1] != reqSyncPost {
		t.Fatalf("paid till requests = %v, want the GET then the POST", got)
	}
	if v, d := unpaidMarkers(t, db); v != "" || d != "" {
		t.Fatalf("paid path wrote unpaid markers (%q, %q)", v, d)
	}
}

// A Pi without a real-time clock boots with yesterday's time: the first
// tick sees the recorded date and makes no call. Once NTP moves the wall
// clock to the new day (the start-up hour, on the monotonic clock, still
// running), the next gated tick makes the one attempt; after it, none.
func TestStartupCheckinWaitsForClockCorrection(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, http.StatusPaymentRequired)
	cfg := testCfg(srv.URL)
	seedUnpaidMarkers(t, db, "0.30.20", "2026-10-05")
	setWall := pinWallClock(t, startupDay) // restored yesterday's time
	setMono := pinStartupClock(t, startupDay)
	armStartup()
	if contacted, err := tick(context.Background(), cfg, db, Hooks{}); contacted || err != nil {
		t.Fatalf("tick with the clock behind = (%v, %v), want quiet", contacted, err)
	}
	if n := len(cloud.requests()); n != 0 {
		t.Fatalf("clock behind: %d requests, want 0", n)
	}
	setWall(startupDay.Add(22 * time.Hour)) // NTP: it is really the next morning
	setMono(startupDay.Add(4 * time.Minute))
	if _, err := tick(context.Background(), cfg, db, Hooks{}); err == nil {
		t.Fatal("after the clock correction: want the start-up attempt (a 402)")
	}
	if got := cloud.requests(); len(got) != 1 || got[0] != reqSyncPost {
		t.Fatalf("after the clock correction: requests = %v, want exactly [%s]", got, reqSyncPost)
	}
	if _, d := unpaidMarkers(t, db); d != "2026-10-06" {
		t.Fatalf("date marker = %q, want 2026-10-06", d)
	}
	setMono(startupDay.Add(6 * time.Minute))
	if contacted, err := tick(context.Background(), cfg, db, Hooks{}); contacted || err != nil {
		t.Fatalf("tick after the attempt = (%v, %v), want quiet", contacted, err)
	}
	if n := len(cloud.requests()); n != 1 {
		t.Fatalf("%d requests after the attempt, want none", n-1)
	}
}

// Exactly one attempt per start: a failed one is not retried in the hour.
func TestStartupCheckinFailedAttemptNotRetried(t *testing.T) {
	cloud, srv, db, setNow := startupFixture(t, http.StatusBadGateway)
	cfg := testCfg(srv.URL)
	armStartup()
	if _, err := tick(context.Background(), cfg, db, Hooks{}); err == nil {
		t.Fatal("want the failed start-up attempt's error")
	}
	for i := 1; i <= 20; i++ {
		setNow(startupDay.Add(time.Duration(i) * 2 * time.Minute))
		if contacted, err := tick(context.Background(), cfg, db, Hooks{}); contacted || err != nil {
			t.Fatalf("tick %d after the failure = (%v, %v), want quiet", i, contacted, err)
		}
	}
	if n := len(cloud.requests()); n != 1 {
		t.Fatalf("%d requests, want only the one failed attempt", n)
	}
	if v, d := unpaidMarkers(t, db); v != "" || d != "" {
		t.Fatalf("markers = (%q, %q) after a failure, want none", v, d)
	}
}

// Past the first hour the trigger ends without a call, even when due.
func TestStartupCheckinEndsAfterAnHour(t *testing.T) {
	cloud, srv, db, setNow := startupFixture(t, 0)
	armStartup()
	setNow(startupDay.Add(startupCheckinWindow + time.Minute))
	if contacted, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); contacted || err != nil {
		t.Fatalf("tick past the hour = (%v, %v), want quiet", contacted, err)
	}
	if got := cloud.requests(); len(got) != 0 {
		t.Fatalf("past the hour: requests = %v, want none", got)
	}
	if startupCheckinArmedAt.Load() != nil {
		t.Fatal("the start-up trigger is still armed past the hour")
	}
}

// Review finding (ut-docs#3673): a 402 closes only the window it answered.
// An operator action that opens a new window while the POST is in flight
// (Check for a paid plan, Register now) must survive the 402, or the kick
// it queued is swallowed by the Retry-After wait and the action does
// nothing.
func TestPlanRequiredKeepsWindowOpenedDuringPost(t *testing.T) {
	useRealSyncGate(t)
	resetTillAuth(t)
	pinOperatorNow(t, startupDay)
	pinVersion(t, "0.30.20")
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stores/sync" && posts.Add(1) == 1 {
			OpenOperatorWindow(2 * time.Minute) // the operator acts mid-POST
		}
		writePlanRequired(w)
	}))
	t.Cleanup(srv.Close)
	db := testDB(t)
	armStartup()
	if _, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err == nil {
		t.Fatal("tick err = nil, want the 402")
	}
	if !operatorWindowOpen(operatorNow()) {
		t.Fatal("the window opened during the POST was closed by the 402 it did not cause")
	}
}

// Review finding (ut-docs#3673): a till whose cache still says paid learns
// of the lapse from the GET's 402. That answer is this start's contact: the
// start-up trigger must not add a second call on the next tick, whatever
// Retry-After the cloud sent.
func TestPaidTillLapse402EndsStartupTrigger(t *testing.T) {
	useRealSyncGate(t)
	resetTillAuth(t)
	pinOperatorNow(t, startupDay)
	pinVersion(t, "0.30.20")
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired) // no Retry-After: an older cloud or a proxy
		_, _ = w.Write([]byte(planRequiredBody()))
	}))
	t.Cleanup(srv.Close)
	db := testDB(t)
	seedEntitlement(t, db, "shop", "active", time.Now())
	armStartup()
	cfg := testCfg(srv.URL)
	if _, err := tick(context.Background(), cfg, db, Hooks{}); err == nil {
		t.Fatal("tick err = nil, want the 402")
	}
	if _, err := tick(context.Background(), cfg, db, Hooks{}); err != nil {
		t.Fatalf("second tick err = %v, want quiet", err)
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("requests this start = %d, want 1 (the lapse 402 was this start's contact)", n)
	}
}
