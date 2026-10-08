package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3161 (ADR-0121 §8): the schedule ticker. Every test drives a fake
// clock and, except the real-module test, a fake fire function — nothing
// here sleeps for an interval.

// fakeSchedClock is a manual clock: After registers a waiter that fires
// when Advance moves now past its deadline, and reports each requested
// duration on afters so a test can wait for the ticker to reach its next
// wait (the deterministic "the loop has moved on" signal).
type fakeSchedClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeSchedWaiter
	afters  chan time.Duration
}

type fakeSchedWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeSchedClock() *fakeSchedClock {
	// In the future (but within UnixNano's range), so a sale.completed
	// another test recorded on the shared bus at real time never reads as a
	// recent sale burst here.
	return &fakeSchedClock{
		now:    time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC), // within UnixNano range
		afters: make(chan time.Duration, 1024),
	}
}

func (c *fakeSchedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeSchedClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
	} else {
		c.waiters = append(c.waiters, fakeSchedWaiter{at: c.now.Add(d), ch: ch})
	}
	c.mu.Unlock()
	c.afters <- d
	return ch
}

func (c *fakeSchedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

// nextAfter waits for the ticker's next After call and returns its duration.
func (c *fakeSchedClock) nextAfter(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-c.afters:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("ticker never asked the clock for its next wait")
		return 0
	}
}

type firedTick struct {
	pluginID string
	ev       Event
}

// recordingFire is a fire seam that reports each call and, while block is
// non-nil, holds the call until block is closed (a still-running tick).
type recordingFire struct {
	fired chan firedTick
	block chan struct{}
}

func newRecordingFire() *recordingFire {
	return &recordingFire{fired: make(chan firedTick, 64)}
}

func (f *recordingFire) fire(_ context.Context, pluginID string, ev Event) {
	f.fired <- firedTick{pluginID, ev}
	if f.block != nil {
		<-f.block
	}
}

func (f *recordingFire) expect(t *testing.T) firedTick {
	t.Helper()
	select {
	case got := <-f.fired:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("expected a schedule tick to fire")
		return firedTick{}
	}
}

func (f *recordingFire) expectNone(t *testing.T) {
	t.Helper()
	select {
	case got := <-f.fired:
		t.Fatalf("unexpected tick fired: %s %s", got.pluginID, got.ev.Type)
	case <-time.After(150 * time.Millisecond):
	}
}

// schedTestRuntime is a runtime whose ticker uses clk, fire and a fixed
// jitter, with the per-tick permission check stubbed to granted.
func schedTestRuntime(t *testing.T, clk *fakeSchedClock, fire *recordingFire, jitter time.Duration) *WasmRuntime {
	t.Helper()
	w := NewWasmRuntime(t.TempDir())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.Close(ctx)
		_ = w.rt.Close(context.Background())
	})
	w.bus = NewEventBus(nil)
	w.sched.now = clk.Now
	w.sched.after = clk.After
	w.sched.jitter = func(max time.Duration) time.Duration {
		if jitter > max {
			t.Errorf("test jitter %s exceeds the schedule's max %s", jitter, max)
		}
		return jitter
	}
	w.sched.fire = fire.fire
	w.sched.granted = func(context.Context, string) (bool, error) { return true, nil }
	return w
}

// startTestSchedule starts one ticker outside Sync, on its own cancelable
// context.
func startTestSchedule(t *testing.T, w *WasmRuntime, pluginID string, s data.PluginScheduleRow) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.mu.Lock()
	gen := w.unsubGen
	w.mu.Unlock()
	w.startScheduleLoop(ctx, gen, pluginID, s)
	return cancel
}

func waitSchedActive(t *testing.T, w *WasmRuntime, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for w.schedActive.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("active schedule tickers = %d, want %d", w.schedActive.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSchedule_FiresAtEveryPlusJitter(t *testing.T) {
	clk := newFakeSchedClock()
	fire := newRecordingFire()
	w := schedTestRuntime(t, clk, fire, 7*time.Second)
	const pluginID = "com.test.tick"
	startTestSchedule(t, w, pluginID, data.PluginScheduleRow{PluginID: pluginID, Event: "com.test.tick.sync.tick", EveryS: 60, JitterS: 10})

	if d := clk.nextAfter(t); d != 67*time.Second {
		t.Fatalf("first wait = %s, want every_s+jitter = 67s", d)
	}
	start := clk.Now()
	clk.Advance(66 * time.Second)
	fire.expectNone(t) // one second early: nothing yet

	clk.Advance(time.Second)
	got := fire.expect(t)
	if got.pluginID != pluginID || got.ev.Type != "com.test.tick.sync.tick" {
		t.Fatalf("fired %s %s, want %s com.test.tick.sync.tick", got.pluginID, got.ev.Type, pluginID)
	}
	if !got.ev.Scheduled || got.ev.Hop != 0 || got.ev.ID == "" {
		t.Fatalf("tick event = %+v; want Scheduled, hop 0 and an id", got.ev)
	}
	if !got.ev.Timestamp.Equal(start.Add(67 * time.Second)) {
		t.Fatalf("tick timestamp = %s, want the due time %s", got.ev.Timestamp, start.Add(67*time.Second))
	}
	var payload struct {
		ScheduledAt string `json:"scheduled_at"`
	}
	if err := json.Unmarshal(got.ev.Payload, &payload); err != nil || payload.ScheduledAt != start.Add(67*time.Second).Format(time.RFC3339) {
		t.Fatalf("tick payload = %s (%v), want scheduled_at of the due time", got.ev.Payload, err)
	}

	// It keeps ticking: the next wait is again every_s+jitter.
	if d := clk.nextAfter(t); d != 67*time.Second {
		t.Fatalf("second wait = %s, want 67s", d)
	}
	clk.Advance(67 * time.Second)
	fire.expect(t)
}

// jitter_s = 0 waits exactly every_s, with the real jitter source.
func TestSchedule_ZeroJitterIsExact(t *testing.T) {
	clk := newFakeSchedClock()
	fire := newRecordingFire()
	w := schedTestRuntime(t, clk, fire, 0)
	w.sched.jitter = defaultScheduleJitter
	startTestSchedule(t, w, "com.test.exact", data.PluginScheduleRow{PluginID: "com.test.exact", Event: "com.test.exact.tick", EveryS: 30})
	for i := 0; i < 3; i++ {
		if d := clk.nextAfter(t); d != 30*time.Second {
			t.Fatalf("wait %d = %s, want exactly 30s", i, d)
		}
		clk.Advance(30 * time.Second)
		fire.expect(t)
	}
}

// The real jitter source is uniform over [0, max], both ends included.
func TestDefaultScheduleJitterStaysWithinBounds(t *testing.T) {
	if got := defaultScheduleJitter(0); got != 0 {
		t.Fatalf("jitter(0) = %s, want 0", got)
	}
	const max = 3 * time.Nanosecond
	seen := map[time.Duration]bool{}
	for i := 0; i < 2000; i++ {
		j := defaultScheduleJitter(max)
		if j < 0 || j > max {
			t.Fatalf("jitter(%s) = %s, outside [0, %s]", max, j, max)
		}
		seen[j] = true
	}
	if len(seen) != 4 {
		t.Fatalf("jitter(%s) over 2000 draws hit %v; want every value 0..3ns", max, seen)
	}
	for i := 0; i < 2000; i++ {
		if j := defaultScheduleJitter(60 * time.Second); j < 0 || j > 60*time.Second {
			t.Fatalf("jitter(60s) = %s, outside [0, 60s]", j)
		}
	}
}

// A tick due while the previous run of the same schedule is still going is
// skipped, not queued; the schedule fires again once that run ends.
func TestSchedule_SkipsTickWhilePreviousRunStillRunning(t *testing.T) {
	clk := newFakeSchedClock()
	fire := newRecordingFire()
	fire.block = make(chan struct{})
	w := schedTestRuntime(t, clk, fire, 0)
	var unblock sync.Once
	release := func() { unblock.Do(func() { close(fire.block) }) }
	t.Cleanup(release) // runs before Close, so a failed test never wedges it
	startTestSchedule(t, w, "com.test.busy", data.PluginScheduleRow{PluginID: "com.test.busy", Event: "com.test.busy.tick", EveryS: 30})

	clk.nextAfter(t)
	clk.Advance(30 * time.Second)
	fire.expect(t) // run 1 starts and stays blocked

	clk.nextAfter(t)
	clk.Advance(30 * time.Second)
	clk.nextAfter(t) // the loop moved past tick 2 ...
	fire.expectNone(t)

	release() // run 1 ends (later runs pass the closed channel)
	deadline := time.Now().Add(5 * time.Second)
	for w.schedRunsInFlight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("run 1 never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	clk.Advance(30 * time.Second)
	fire.expect(t) // tick 3 fires again
}

// A tick due during a sale commit burst waits until saleBurstPause has
// passed since the last sale.completed, then fires — deferred, never
// dropped.
func TestSchedule_PausesDuringSaleBurstThenFires(t *testing.T) {
	clk := newFakeSchedClock()
	fire := newRecordingFire()
	w := schedTestRuntime(t, clk, fire, 0)
	startTestSchedule(t, w, "com.test.burst", data.PluginScheduleRow{PluginID: "com.test.burst", Event: "com.test.burst.tick", EveryS: 60})

	clk.nextAfter(t)
	start := clk.Now()
	clk.Advance(59 * time.Second)
	w.bus.markSaleCompleted(clk.Now()) // a sale 1 s before the tick is due
	clk.Advance(time.Second)
	if d := clk.nextAfter(t); d != saleBurstPause-time.Second {
		t.Fatalf("deferral = %s, want the rest of the pause window %s", d, saleBurstPause-time.Second)
	}
	fire.expectNone(t)

	// Another sale lands while deferred: the burst goes on, so does the wait.
	clk.Advance(time.Second)
	w.bus.markSaleCompleted(clk.Now())
	clk.Advance(saleBurstPause - 2*time.Second)
	if d := clk.nextAfter(t); d != 2*time.Second {
		t.Fatalf("second deferral = %s, want 2s", d)
	}
	fire.expectNone(t)

	clk.Advance(2 * time.Second)
	got := fire.expect(t)
	var payload struct {
		ScheduledAt string `json:"scheduled_at"`
	}
	_ = json.Unmarshal(got.ev.Payload, &payload)
	if payload.ScheduledAt != start.Add(60*time.Second).Format(time.RFC3339) {
		t.Fatalf("scheduled_at = %q, want the original due time", payload.ScheduledAt)
	}
}

// PublishSaleCompleted is what marks a sale commit for the pause.
func TestPublishSaleCompleted_RecordsSaleCommitTime(t *testing.T) {
	bus := NewEventBus(managerTestDB(t))
	if !bus.lastSaleCompletedAt().IsZero() {
		t.Fatal("fresh bus reports a sale")
	}
	before := time.Now()
	_, _ = bus.PublishSaleCompleted(context.Background(), SaleCompletedEvent{SaleID: "s1"})
	got := bus.lastSaleCompletedAt()
	if got.Before(before) || got.After(time.Now()) {
		t.Fatalf("last sale time %s not within the publish call", got)
	}
}

// A schedule event may legally end in `.ask` (`<id>.foo.ask`); it must still
// be ordinary work, never sale-path — ADR-0121 §2: schedule ticks never
// take a reserved slot — and gets no event-class deadline floor.
func TestSchedule_EventNeverTakesReservedSlot(t *testing.T) {
	if isSalePathCall(Event{Type: "com.test.reserved.foo.ask", Scheduled: true}) {
		t.Fatal("a scheduled .ask event is sale-path")
	}
	if isSalePathCall(Event{Type: "com.test.reserved.pay.authorize", Scheduled: true}) {
		t.Fatal("a scheduled .authorize event is sale-path")
	}
	if !isSalePathCall(Event{Type: "com.test.reserved.foo.ask"}) {
		t.Fatal("control: a core-raised .ask is sale-path")
	}

	// Through the real call gate: the plugin's one ordinary slot is held,
	// only the reserved one is free. A core .ask still runs on it; the same
	// name as a schedule tick waits for an ordinary slot and gives up.
	const pluginID = "com.test.reserved"
	w, hold := queuedTestRuntime(t, pluginID)
	defer hold()
	if _, err := w.HandleEvent(context.Background(), pluginID, Event{Type: pluginID + ".foo.ask"}); err != nil {
		t.Fatalf("control: core .ask on the reserved slot: %v", err)
	}
	w.queueWait = 150 * time.Millisecond
	_, err := w.handleQueuedEvent(context.Background(), pluginID, Event{Type: pluginID + ".foo.ask", Scheduled: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("scheduled .ask with only the reserved slot free: err = %v, want no slot", err)
	}

	// No payment-gate deadline floor either, even with net granted.
	w.hasNet[pluginID] = true
	if got := w.timeoutForEvent(pluginID, Event{Type: pluginID + ".pay.authorize", Scheduled: true}); got != w.netTimeout {
		t.Fatalf("scheduled .authorize deadline = %s, want the plain base %s", got, w.netTimeout)
	}
}

// A per-tick check: a schedule whose permission is revoked live stops at
// its next tick without firing, even before any re-Sync.
func TestSchedule_StopsAtNextTickWhenPermissionRevokedLive(t *testing.T) {
	clk := newFakeSchedClock()
	fire := newRecordingFire()
	w := schedTestRuntime(t, clk, fire, 0)
	var mu sync.Mutex
	granted := true
	w.sched.granted = func(context.Context, string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return granted, nil
	}
	startTestSchedule(t, w, "com.test.live", data.PluginScheduleRow{PluginID: "com.test.live", Event: "com.test.live.tick", EveryS: 30})
	clk.nextAfter(t)
	clk.Advance(30 * time.Second)
	fire.expect(t)

	mu.Lock()
	granted = false
	mu.Unlock()
	clk.nextAfter(t)
	clk.Advance(30 * time.Second)
	waitSchedActive(t, w, 0)
	fire.expectNone(t)
}

// A permission-check error fails closed for that tick only.
func TestSchedule_PermissionCheckErrorSkipsTick(t *testing.T) {
	clk := newFakeSchedClock()
	fire := newRecordingFire()
	w := schedTestRuntime(t, clk, fire, 0)
	var mu sync.Mutex
	fail := true
	w.sched.granted = func(context.Context, string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return false, errors.New("database is locked")
		}
		return true, nil
	}
	startTestSchedule(t, w, "com.test.err", data.PluginScheduleRow{PluginID: "com.test.err", Event: "com.test.err.tick", EveryS: 30})
	clk.nextAfter(t)
	clk.Advance(30 * time.Second)
	clk.nextAfter(t)
	fire.expectNone(t)
	if w.schedActive.Load() != 1 {
		t.Fatal("ticker stopped on a transient permission-check error")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	clk.Advance(30 * time.Second)
	fire.expect(t)
}

// ---- Through Sync, against the real schema and a real module ----

// schedSyncFixture installs a real wasm plugin (the hostfn guest) with one
// schedule, ready for Sync. grant controls the `schedule` permission row:
// "granted", "declared" (not granted) or "" (not declared).
func schedSyncFixture(t *testing.T, pluginID, grant string) (*WasmRuntime, *sql.DB, *fakeSchedClock) {
	t.Helper()
	guest := buildHostfnGuest(t)
	db := managerTestDB(t)
	base := t.TempDir()
	w := NewWasmRuntime(base)
	clk := newFakeSchedClock()
	w.sched.now = clk.Now
	w.sched.after = clk.After
	w.sched.jitter = func(time.Duration) time.Duration { return 0 }
	t.Cleanup(func() {
		w.Close(context.Background())
		_ = w.rt.Close(context.Background())
	})

	seedInstalledPlugin(t, db, pluginID, "Sched", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'storage', 1)`, pluginID+"-st", pluginID); err != nil {
		t.Fatal(err)
	}
	switch grant {
	case "granted", "declared":
		g := 0
		if grant == "granted" {
			g = 1
		}
		if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'schedule', ?)`, pluginID+"-sc", pluginID, g); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO plugin_schedules (plugin_id, event, every_s, jitter_s) VALUES (?, ?, 30, 0)`, pluginID, pluginID+".tick"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileWithParents(filepath.Join(base, pluginID, "1.0.0", "plugin.wasm"), raw); err != nil {
		t.Fatal(err)
	}
	return w, db, clk
}

// The real path: Sync starts the ticker, the tick runs the REAL module
// through handleQueuedEvent with no hook row for the event, and the guest
// sees it (it records a results key in its own storage).
func TestSchedule_SyncTicksRealModule(t *testing.T) {
	const pluginID = "com.test.schedreal"
	w, db, clk := schedSyncFixture(t, pluginID, "granted")
	ctx := context.Background()
	w.Sync(ctx, db)
	if got := w.schedActive.Load(); got != 1 {
		t.Fatalf("active tickers after Sync = %d, want 1", got)
	}
	repo := data.NewPluginRepo(db)
	if raw, _ := repo.StorageGet(ctx, pluginID, "results"); raw != nil {
		t.Fatalf("guest ran before any tick: %s", raw)
	}
	if d := clk.nextAfter(t); d != 30*time.Second {
		t.Fatalf("wait = %s, want 30s", d)
	}
	clk.Advance(30 * time.Second)
	deadline := time.Now().Add(20 * time.Second)
	for {
		raw, err := repo.StorageGet(ctx, pluginID, "results")
		if err == nil && raw != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the real module never ran the tick (last err %v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSchedule_NoTickerWithoutGrantedSchedulePermission(t *testing.T) {
	for _, grant := range []string{"declared", ""} {
		t.Run("permission="+grant, func(t *testing.T) {
			w, db, _ := schedSyncFixture(t, "com.test.nogrant", grant)
			w.Sync(context.Background(), db)
			w.mu.Lock()
			_, loaded := w.modules["com.test.nogrant"]
			w.mu.Unlock()
			if !loaded {
				t.Fatal("control: module did not load")
			}
			if got := w.schedActive.Load(); got != 0 {
				t.Fatalf("active tickers = %d, want 0 without the schedule grant", got)
			}
		})
	}
}

// Every way a plugin stops being allowed to tick stops its ticker: disable,
// permission revoke, uninstall (each followed by the Sync that
// Manager.Reload runs) and Close.
func TestSchedule_StopsOnDisableRevokeUninstallAndClose(t *testing.T) {
	cases := []struct {
		name string
		stop func(t *testing.T, w *WasmRuntime, db *sql.DB, pluginID string)
	}{
		{"disable", func(t *testing.T, w *WasmRuntime, db *sql.DB, id string) {
			if _, err := db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = ?`, id); err != nil {
				t.Fatal(err)
			}
			w.Sync(context.Background(), db)
		}},
		{"revoke", func(t *testing.T, w *WasmRuntime, db *sql.DB, id string) {
			if err := RevokePermission(context.Background(), db, id, "schedule"); err != nil {
				t.Fatal(err)
			}
			w.Sync(context.Background(), db)
		}},
		{"uninstall", func(t *testing.T, w *WasmRuntime, db *sql.DB, id string) {
			if err := UninstallPlugin(context.Background(), db, id); err != nil {
				t.Fatal(err)
			}
			w.Sync(context.Background(), db)
		}},
		{"close", func(t *testing.T, w *WasmRuntime, _ *sql.DB, _ string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			w.Close(ctx)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const pluginID = "com.test.schedstop"
			w, db, clk := schedSyncFixture(t, pluginID, "granted")
			fire := newRecordingFire()
			w.sched.fire = fire.fire
			w.Sync(context.Background(), db)
			if got := w.schedActive.Load(); got != 1 {
				t.Fatalf("control: active tickers after Sync = %d, want 1", got)
			}
			clk.nextAfter(t)
			clk.Advance(30 * time.Second)
			fire.expect(t) // ticking

			tc.stop(t, w, db, pluginID)
			waitSchedActive(t, w, 0)
			clk.Advance(time.Hour)
			fire.expectNone(t)
		})
	}
}

// A re-Sync with nothing changed replaces the tickers rather than adding a
// second set: one schedule, one ticker, one fire per due tick.
func TestSchedule_ResyncReplacesTickers(t *testing.T) {
	const pluginID = "com.test.resync"
	w, db, clk := schedSyncFixture(t, pluginID, "granted")
	fire := newRecordingFire()
	w.sched.fire = fire.fire
	w.Sync(context.Background(), db)
	clk.nextAfter(t)
	w.Sync(context.Background(), db)
	clk.nextAfter(t)
	waitSchedActive(t, w, 1)
	clk.Advance(30 * time.Second)
	fire.expect(t)
	fire.expectNone(t)
}

// Skip-while-running holds across a re-Sync: a run admitted before the
// Sync keeps going, and the next generation's ticker for the same schedule
// skips until it ends instead of overlapping it.
func TestSchedule_SkipWhileRunningSpansResync(t *testing.T) {
	clk := newFakeSchedClock()
	fire := newRecordingFire()
	fire.block = make(chan struct{})
	w := schedTestRuntime(t, clk, fire, 0)
	var unblock sync.Once
	release := func() { unblock.Do(func() { close(fire.block) }) }
	t.Cleanup(release)
	s := data.PluginScheduleRow{PluginID: "com.test.span", Event: "com.test.span.tick", EveryS: 30}

	stopOld := startTestSchedule(t, w, "com.test.span", s)
	clk.nextAfter(t)
	clk.Advance(30 * time.Second)
	fire.expect(t) // old generation's run, still going
	clk.nextAfter(t)
	stopOld()
	waitSchedActive(t, w, 0)

	startTestSchedule(t, w, "com.test.span", s)
	clk.nextAfter(t)
	clk.Advance(30 * time.Second)
	clk.nextAfter(t)
	fire.expectNone(t)

	release()
	deadline := time.Now().Add(5 * time.Second)
	for w.schedRunsInFlight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("old run never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	clk.Advance(30 * time.Second)
	fire.expect(t)
}

// A backward wall-clock step after a sale leaves the sale mark in the
// future. The tick must not wait for the clock to catch up (review of
// ut-docs#3161): a window ending more than saleBurstPause ahead is stale.
func TestSchedule_BackwardClockStepDoesNotStallTicker(t *testing.T) {
	clk := newFakeSchedClock()
	fire := newRecordingFire()
	w := schedTestRuntime(t, clk, fire, 0)
	startTestSchedule(t, w, "com.test.clockstep", data.PluginScheduleRow{PluginID: "com.test.clockstep", Event: "com.test.clockstep.tick", EveryS: 60})

	clk.nextAfter(t)
	// A sale recorded at a wall time 20 min ahead of the (stepped-back) clock.
	w.bus.markSaleCompleted(clk.Now().Add(20 * time.Minute))
	clk.Advance(60 * time.Second)
	fire.expect(t)
}

// Sync's permission check is not audited: a plugin still awaiting its
// `schedule` grant must not write a denial row on every Reload (review of
// ut-docs#3161). The per-tick check stays audited.
func TestSchedule_SyncPermissionCheckWritesNoDenialAudit(t *testing.T) {
	w, db, _ := schedSyncFixture(t, "com.test.noaudit", "declared")
	w.Sync(context.Background(), db)
	w.Sync(context.Background(), db)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'permission_denied' AND entity_id = ?`, "com.test.noaudit").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("permission_denied audit rows after two Syncs = %d, want 0", n)
	}
	if got := w.schedActive.Load(); got != 0 {
		t.Fatalf("active tickers = %d, want 0 without the grant", got)
	}
}

// A plugin whose module fails to load gets no ticker, even with schedules
// stored and the permission granted.
func TestSchedule_NoTickerWhenModuleFailsToLoad(t *testing.T) {
	const pluginID = "com.test.schedbroken"
	w, db, _ := schedSyncFixture(t, pluginID, "granted")
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './missing.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatal(err)
	}
	w.Sync(context.Background(), db)
	w.mu.Lock()
	_, loaded := w.modules[pluginID]
	w.mu.Unlock()
	if loaded {
		t.Fatal("control: module loaded from a missing file")
	}
	if got := w.schedActive.Load(); got != 0 {
		t.Fatalf("active tickers = %d, want 0 for a plugin that failed to load", got)
	}
}
