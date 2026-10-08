package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Plugin schedules (ADR-0121 §8, ut-docs#3161): "the host fires each
// schedules[] event as an ordinary event on its interval (min 30 s,
// jittered), skips a tick while the previous one still runs, and pauses all
// ticks during a sale commit burst. No free-running guest threads."
//
// Each Sync stops the previous generation's tickers and starts one goroutine
// per persisted schedule of every wasm plugin that is active, loaded and
// holds the `schedule` permission. A tick goes to that plugin alone, through
// handleQueuedEvent — never the bus, so no hook row is needed and no other
// plugin sees it — and is ordinary work (Event.Scheduled): never a reserved
// sale-path slot, never an event-class deadline floor. The manifest parser
// (validateABI3Fields) has already enforced every_s >= 30, 0 <= jitter_s <=
// every_s and the plugin's own event namespace.

// schedulePermission gates the ticks (ADR-0121 §2 permission table).
const schedulePermission = "schedule"

// saleBurstPause: a tick that falls due within this long after the latest
// sale.completed waits until that much time has passed with no new sale,
// then fires. Checkout commits back-to-back while a queue is served; 3 s
// covers the gap between two tenders at a busy till without delaying a
// 30 s+ schedule noticeably. A tick is deferred, never dropped. This is
// load shaping, not checkout's guarantee: the mark is set only when
// sale.completed is published (after the commit), and a tick already
// running is not stopped — the reserved sale-path call slot is what keeps
// a tick from ever blocking a sale (ADR-0121 §2).
const saleBurstPause = 3 * time.Second

// scheduleDeps are the ticker's seams; a nil field means the real one.
type scheduleDeps struct {
	now     func() time.Time
	after   func(time.Duration) <-chan time.Time
	jitter  func(max time.Duration) time.Duration                    // uniform in [0, max]
	granted func(ctx context.Context, pluginID string) (bool, error) // live `schedule` check
	fire    func(ctx context.Context, pluginID string, ev Event)     // runs one tick
}

// defaultScheduleJitter draws uniformly from [0, max], both ends included.
func defaultScheduleJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max) + 1)) // #nosec G404 -- spreading load, not security
}

// resolved returns w.sched with every nil seam replaced by the real one.
func (w *WasmRuntime) resolvedScheduleDeps() scheduleDeps {
	d := w.sched
	if d.now == nil {
		d.now = time.Now
	}
	if d.after == nil {
		d.after = time.After
	}
	if d.jitter == nil {
		d.jitter = defaultScheduleJitter
	}
	if d.granted == nil {
		d.granted = w.scheduleGranted
	}
	if d.fire == nil {
		d.fire = w.fireScheduled
	}
	return d
}

// scheduleGranted is the live per-tick permission check (audited on a
// denial, like every other CheckPermission caller).
func (w *WasmRuntime) scheduleGranted(ctx context.Context, pluginID string) (bool, error) {
	w.mu.Lock()
	db := w.db
	w.mu.Unlock()
	if db == nil {
		return false, fmt.Errorf("no database")
	}
	return CheckPermissionGranted(ctx, db, pluginID, schedulePermission)
}

// fireScheduled runs one tick on the plugin as queued, ordinary work: it
// waits for an ordinary call slot like a drained bus event (ut-docs#3171).
func (w *WasmRuntime) fireScheduled(ctx context.Context, pluginID string, ev Event) {
	if _, err := w.handleQueuedEvent(ctx, pluginID, ev); err != nil {
		if errors.Is(err, context.Canceled) {
			logging.L().Infof("wasm %s schedule %s: stopped before it ran (plugins reloaded or till shutting down)", pluginID, ev.Type)
			return
		}
		logging.L().Errorf("wasm %s schedule %s: %v", pluginID, ev.Type, err)
	}
}

// lastSaleCommit is when the bus last published sale.completed.
func (w *WasmRuntime) lastSaleCommit() time.Time {
	w.mu.Lock()
	db := w.db
	w.mu.Unlock()
	if w.bus == nil && db == nil {
		return time.Time{}
	}
	return w.eventBus(db).lastSaleCompletedAt()
}

// startSchedules starts this Sync generation's tickers on schedCtx for the
// plugins in loaded. A failure to read schedules or check the permission
// starts no ticker for that plugin (fail closed) and never fails the Sync —
// checkout must not depend on a plugin.
func (w *WasmRuntime) startSchedules(ctx, schedCtx context.Context, gen int, db *sql.DB, loaded []string) {
	if len(loaded) == 0 {
		return
	}
	rows, err := data.NewPluginRepo(db).ListPluginSchedules(ctx)
	if err != nil {
		logging.L().Errorf("wasm sync: list plugin schedules: %v — no schedule ticks this generation", err)
		return
	}
	isLoaded := make(map[string]bool, len(loaded))
	for _, id := range loaded {
		isLoaded[id] = true
	}
	allowed := map[string]bool{}
	for _, s := range rows {
		if !isLoaded[s.PluginID] {
			continue
		}
		ok, seen := allowed[s.PluginID]
		if !seen {
			// Not audited here: every Reload re-runs this, and a plugin
			// still awaiting its grant would write a denial row each time
			// without attempting anything. The per-tick check is audited.
			granted, _, err := data.NewPluginRepo(db).CheckPermission(ctx, s.PluginID, schedulePermission)
			if err != nil {
				logging.L().Errorf("wasm sync: %s schedule permission check: %v — its schedules do not tick", s.PluginID, err)
			} else if !granted {
				logging.L().Infof("wasm sync: %s declares schedules but the %q permission is not granted — they do not tick", s.PluginID, schedulePermission)
			}
			ok = err == nil && granted
			allowed[s.PluginID] = ok
		}
		if ok {
			w.startScheduleLoop(schedCtx, gen, s.PluginID, s)
		}
	}
}

// startScheduleLoop starts one ticker goroutine, counted in w.wg and
// w.schedActive before it runs so Close and tests see it at once.
func (w *WasmRuntime) startScheduleLoop(ctx context.Context, gen int, pluginID string, s data.PluginScheduleRow) {
	deps := w.resolvedScheduleDeps()
	w.wg.Add(1)
	w.schedActive.Add(1)
	go func() {
		defer logging.RecoverAndLog("plugins.wasmSchedule")
		defer w.wg.Done()
		defer w.schedActive.Add(-1)
		w.runSchedule(ctx, gen, deps, pluginID, s)
	}()
}

// runSchedule is one schedule's ticker: wait every_s + jitter, wait out a
// sale burst, re-check that the generation and the permission still stand,
// then hand the tick to the plugin unless its previous run is still going.
func (w *WasmRuntime) runSchedule(ctx context.Context, gen int, deps scheduleDeps, pluginID string, s data.PluginScheduleRow) {
	every := time.Duration(s.EveryS) * time.Second
	maxJitter := time.Duration(s.JitterS) * time.Second
	// The busy key outlives a Sync generation: a run admitted before a
	// re-Sync keeps running, and the new generation's ticker must skip
	// while it does.
	busyKey := pluginID + "\x00" + s.Event
	for {
		if !sleepCtx(ctx, deps.after, every+deps.jitter(maxJitter)) {
			return
		}
		due := deps.now()
		if !w.waitOutSaleBurst(ctx, deps) {
			return
		}
		w.mu.Lock()
		stale := gen != w.unsubGen
		w.mu.Unlock()
		if stale || ctx.Err() != nil {
			return
		}
		granted, err := deps.granted(ctx, pluginID)
		if err != nil {
			logging.L().Errorf("wasm %s schedule %s: permission check: %v — tick skipped", pluginID, s.Event, err)
			continue
		}
		if !granted {
			logging.L().Infof("wasm %s schedule %s: %q permission no longer granted — ticker stopped", pluginID, s.Event, schedulePermission)
			return
		}
		if _, busy := w.schedBusy.LoadOrStore(busyKey, struct{}{}); busy {
			logging.L().Warnf("wasm %s schedule %s: previous tick still running — tick skipped", pluginID, s.Event)
			continue
		}
		payload, _ := json.Marshal(map[string]string{"scheduled_at": due.UTC().Format(time.RFC3339)})
		ev := Event{ID: uuid.NewString(), Type: s.Event, Timestamp: deps.now(), Payload: payload, Scheduled: true}
		w.wg.Add(1)
		w.schedRunsInFlight.Add(1)
		go func() {
			defer logging.RecoverAndLog("plugins.wasmScheduleTick")
			defer w.wg.Done()
			defer w.schedBusy.Delete(busyKey)
			defer w.schedRunsInFlight.Add(-1)
			deps.fire(ctx, pluginID, ev)
		}()
	}
}

// waitOutSaleBurst returns once saleBurstPause has passed since the latest
// sale commit (re-read after every wait, so a continuing burst keeps the
// tick waiting), or false when ctx ends first.
func (w *WasmRuntime) waitOutSaleBurst(ctx context.Context, deps scheduleDeps) bool {
	for {
		last := w.lastSaleCommit()
		if last.IsZero() {
			return true
		}
		rest := last.Add(saleBurstPause).Sub(deps.now())
		if rest <= 0 {
			return true
		}
		// The mark is wall-clock time. A window ending further ahead than
		// saleBurstPause means the clock stepped back since that sale
		// (NTP correcting a Pi's fake-hwclock): no burst is in progress,
		// and waiting would stall every ticker for the size of the step.
		if rest > saleBurstPause {
			return true
		}
		if !sleepCtx(ctx, deps.after, rest) {
			return false
		}
	}
}

// sleepCtx waits d on after, or returns false when ctx ends first.
func sleepCtx(ctx context.Context, after func(time.Duration) <-chan time.Time, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-after(d):
		return ctx.Err() == nil
	}
}

// persistSchedules replaces pluginID's persisted schedules with the
// manifest's (PersistManifest and Rollback), so the next Sync ticks exactly
// what the installed version declares.
func persistSchedules(ctx context.Context, repo *data.PluginRepo, tx *sql.Tx, pluginID string, m *Manifest) error {
	rows := make([]data.PluginScheduleRow, 0, len(m.Schedules))
	for _, s := range m.Schedules {
		rows = append(rows, data.PluginScheduleRow{PluginID: pluginID, Event: s.Event, EveryS: s.EveryS, JitterS: s.JitterS})
	}
	if err := repo.ReplacePluginSchedules(ctx, tx, pluginID, rows); err != nil {
		return fmt.Errorf("persist plugin schedules: %w", err)
	}
	return nil
}
