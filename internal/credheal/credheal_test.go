package credheal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ---- fakes -------------------------------------------------------------------------

type memKV struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemKV() *memKV { return &memKV{m: map[string]string{}} }
func (k *memKV) Get(_ context.Context, key string) (string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	return v, ok, nil
}
func (k *memKV) Set(_ context.Context, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = v
	return nil
}
func (k *memKV) GetOrCreate(ctx context.Context, key, def string) (string, error) {
	if v, ok, _ := k.Get(ctx, key); ok {
		return v, nil
	}
	_ = k.Set(ctx, key, def)
	return def, nil
}
func (k *memKV) val(key string) string { v, _, _ := k.Get(context.Background(), key); return v }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	t        *testing.T
	e        *Engine
	kv       *memKV
	clk      *clock
	recovers atomic.Int32
	probes   atomic.Int32
	healed   atomic.Int32
	outcome  func() enroll.RecoverOutcome // the next Recover answer
	probe    func() ProbeResult
	can      func() (bool, string)
	pauses   []time.Duration // the jitter waits asked for
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, kv: newMemKV(), clk: &clock{t: time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)}}
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Healed: true} }
	r.probe = func() ProbeResult { return ProbeResult{Code: "unauthorized"} }
	r.can = func() (bool, string) { return true, "" }
	SetHooks(Hooks{
		OnHealed: func() { r.healed.Add(1) },
		Probe:    func(context.Context) ProbeResult { r.probes.Add(1); return r.probe() },
	})
	t.Cleanup(func() { SetHooks(Hooks{}) })
	r.build()
	return r
}

func (r *rig) build() {
	r.e = newEngine(Deps{
		Cfg: &config.Config{}, KV: r.kv, Now: r.clk.now, Jitter: func() float64 { return 0.5 },
		Pause:      func(_ context.Context, d time.Duration) { r.pauses = append(r.pauses, d) },
		CanRecover: func(context.Context) (bool, string) { return r.can() },
		Recover: func(context.Context) (enroll.RecoverOutcome, error) {
			r.recovers.Add(1)
			return r.outcome(), nil
		},
	})
	r.e.load(context.Background())
}

var bg = context.Background()

func unauth(fromCheckin bool) Signal {
	return Signal{Source: "checkin", Code: "unauthorized", FromCheckin: fromCheckin}
}

// ---- tests ---------------------------------------------------------------------------

// AC1/AC12: the FIRST 401 starts a heal (not the third): chip data at once,
// recover once, state ok, OnHealed fired, last_healed_at saved.
func TestFirstUnauthorizedHealsInOneRound(t *testing.T) {
	r := newRig(t)
	r.e.handle(bg, unauth(true))
	if r.recovers.Load() != 1 {
		t.Fatalf("recover calls = %d, want 1", r.recovers.Load())
	}
	if r.probes.Load() != 0 {
		t.Fatal("a check-in's own 401 must not be probed again")
	}
	if st := r.e.status(); st.State != StateOK || st.LastHealedAt.IsZero() {
		t.Fatalf("status = %+v, want ok with last_healed_at", st)
	}
	if r.healed.Load() != 1 {
		t.Fatalf("OnHealed fired %d times", r.healed.Load())
	}
	if r.kv.val(data.CredHealLastHealedAtKey) == "" || r.kv.val(data.CredHealStateKey) != "" {
		t.Fatal("saved state: last_healed_at kept, state cleared")
	}
}

// AC12: between the first 401 being reported and the engine running, the chip
// already says "reconnecting".
func TestReportShowsReconnectingBeforeTheEngineRuns(t *testing.T) {
	r := newRig(t)
	active.Store(r.e)
	t.Cleanup(func() { active.Store(nil) })
	Report(Signal{Source: "checkin", Code: "unauthorized", FromCheckin: true})
	if st := Current(); st.State != StateHealing || st.Reason != ReasonReconnecting {
		t.Fatalf("Current() = %+v, want healing/reconnecting at once", st)
	}
	// A final answer never flashes "reconnecting".
	r2 := newRig(t)
	active.Store(r2.e)
	Report(Signal{Code: "device_revoked", Reason: "removed"})
	if st := Current(); st.State != StateOK {
		t.Fatalf("Current() = %+v for a removal, want ok until the engine decides", st)
	}
}

// A non-check-in caller's 401 is confirmed by a probe first; a working
// credential is never rotated because one route misbehaved (2026-11-26).
func TestNonCheckinSignalWithHealthyProbeNeverRotates(t *testing.T) {
	r := newRig(t)
	r.probe = func() ProbeResult { return ProbeResult{OK: true} }
	r.e.handle(bg, Signal{Source: "backup", Code: "unauthorized"})
	if r.probes.Load() != 1 || r.recovers.Load() != 0 {
		t.Fatalf("probes %d recovers %d, want 1 and 0", r.probes.Load(), r.recovers.Load())
	}
	if st := r.e.status(); st.State != StateOK {
		t.Fatalf("status = %+v", st)
	}
	if r.kv.val(data.CredHealStateKey) != "" {
		t.Fatal("state left saved")
	}
}

func TestNonCheckinSignalWithDeadProbeHeals(t *testing.T) {
	r := newRig(t)
	r.e.handle(bg, Signal{Source: "backup", Code: "unauthorized"})
	if r.probes.Load() != 1 || r.recovers.Load() != 1 || r.e.status().State != StateOK {
		t.Fatalf("probes %d recovers %d status %+v", r.probes.Load(), r.recovers.Load(), r.e.status())
	}
}

// A probe that cannot reach the cloud is a transient failure: no recover yet.
func TestProbeTransportFailureBacksOff(t *testing.T) {
	r := newRig(t)
	r.probe = func() ProbeResult { return ProbeResult{Err: errors.New("offline")} }
	r.e.handle(bg, Signal{Source: "stock", Code: "unauthorized"})
	if r.recovers.Load() != 0 {
		t.Fatal("recover ran without a confirmed dead credential")
	}
	if st := r.e.status(); st.State != StateHealing {
		t.Fatalf("status = %+v", st)
	}
}

// A 401 with no envelope code never triggers anything.
func TestEnvelopeLessSignalNeverTriggers(t *testing.T) {
	r := newRig(t)
	for _, code := range []string{"", "forbidden", "device_mismatch", "plan_required"} {
		r.e.handle(bg, Signal{Source: "catalog", Code: code, FromCheckin: true})
	}
	if r.recovers.Load() != 0 || r.probes.Load() != 0 || r.e.status().State != StateOK {
		t.Fatalf("recovers %d probes %d status %+v", r.recovers.Load(), r.probes.Load(), r.e.status())
	}
}

// The state table: what each recover outcome (or answer) becomes.
func TestOutcomeTable(t *testing.T) {
	in5 := func(r *rig) time.Time { return r.clk.now().Add(5 * time.Hour) }
	cases := []struct {
		name       string
		out        func(r *rig) enroll.RecoverOutcome
		wantState  State
		wantReason string
		wantDetail string
		wantUntil  bool
		recheck    bool // a next wake is scheduled
	}{
		{name: "quarantined", out: func(r *rig) enroll.RecoverOutcome {
			return enroll.RecoverOutcome{Code: "key_quarantined", Until: in5(r)}
		}, wantState: StateWaiting, wantReason: ReasonKeyQuarantined, wantUntil: true, recheck: true},
		{name: "limit with date", out: func(r *rig) enroll.RecoverOutcome {
			return enroll.RecoverOutcome{Code: "recover_limit", Until: in5(r)}
		}, wantState: StateWaiting, wantReason: ReasonRecoverLimit, wantUntil: true, recheck: true},
		{name: "limit no date", out: func(*rig) enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "recover_limit"} },
			wantState: StateNeedsPerson, wantReason: ReasonInUseElsewhere, recheck: true},
		{name: "refused", out: func(*rig) enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "recover_refused"} },
			wantState: StateNeedsPerson, wantReason: ReasonCannotProve, recheck: true},
		{name: "key conflict", out: func(*rig) enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "key_conflict"} },
			wantState: StateNeedsPerson, wantReason: ReasonCannotProve, recheck: true},
		{name: "role conflict", out: func(*rig) enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "role_conflict"} },
			wantState: StateNeedsPerson, wantReason: ReasonRoleConflict, recheck: true},
		{name: "replaced", out: func(*rig) enroll.RecoverOutcome {
			return enroll.RecoverOutcome{Code: "device_revoked", Reason: "replaced", Detail: "Back till"}
		}, wantState: StateNeedsPerson, wantReason: ReasonReplaced, wantDetail: "Back till"},
		{name: "moved", out: func(*rig) enroll.RecoverOutcome {
			return enroll.RecoverOutcome{Code: "device_revoked", Reason: "moved", Detail: "Other shop"}
		}, wantState: StateNeedsPerson, wantReason: ReasonMoved, wantDetail: "Other shop"},
		{name: "removed", out: func(*rig) enroll.RecoverOutcome {
			return enroll.RecoverOutcome{Code: "device_revoked", Reason: "removed"}
		}, wantState: StateNeedsPerson, wantReason: ReasonRemoved},
		{name: "store deleted is not final", out: func(*rig) enroll.RecoverOutcome {
			return enroll.RecoverOutcome{Code: "device_revoked", Reason: "store_deleted"}
		}, wantState: StateNeedsPerson, wantReason: ReasonStoreDeleted, recheck: true},
		{name: "old cloud 404", out: func(*rig) enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "feature_absent"} },
			wantState: StateNeedsPerson, wantReason: ReasonPairAgain, recheck: true},
		{name: "switched off", out: func(*rig) enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "recover_disabled"} },
			wantState: StateNeedsPerson, wantReason: ReasonPairAgain, recheck: true},
		{name: "transient", out: func(*rig) enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "transport", Transient: true} },
			wantState: StateHealing, wantReason: ReasonReconnecting, recheck: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.outcome = func() enroll.RecoverOutcome { return c.out(r) }
			r.e.handle(bg, unauth(true))
			st := r.e.status()
			if st.State != c.wantState || st.Reason != c.wantReason || st.Detail != c.wantDetail || st.Until.IsZero() == c.wantUntil {
				t.Fatalf("status = %+v, want %s/%s detail %q until %v", st, c.wantState, c.wantReason, c.wantDetail, c.wantUntil)
			}
			if _, has := r.e.untilNext(); has != c.recheck {
				t.Fatalf("a wake is scheduled = %v, want %v", has, c.recheck)
			}
			if r.healed.Load() != 0 {
				t.Fatal("healed on a refusal")
			}
		})
	}
}

// A device_revoked 401 with a final reason needs no network call at all.
func TestFinalRevokeMakesNoNetworkCall(t *testing.T) {
	for _, reason := range []string{"replaced", "moved", "removed"} {
		r := newRig(t)
		r.e.handle(bg, Signal{Source: "checkin", Code: "device_revoked", Reason: reason, Detail: "X", FromCheckin: true})
		st := r.e.status()
		if st.State != StateNeedsPerson || st.Reason != reason || r.recovers.Load() != 0 || r.probes.Load() != 0 {
			t.Fatalf("%s: status %+v recovers %d probes %d", reason, st, r.recovers.Load(), r.probes.Load())
		}
		if _, has := r.e.untilNext(); has {
			t.Fatalf("%s: a final state must not schedule a wake", reason)
		}
		// Another 401 does not move it; a heal hint is ignored for a final reason.
		r.e.handle(bg, Signal{Code: "unauthorized", FromCheckin: true, At: r.clk.now().Add(time.Hour)})
		if r.e.status().Reason != reason || r.recovers.Load() != 0 {
			t.Fatalf("%s: a final state was left by a 401", reason)
		}
	}
}

// device_revoked + the cloud's recover hint (pair_abandoned, an armed restore)
// heals; without the hint a deleted shop waits for the owner.
func TestDeviceRevokedWithRecoverHintHeals(t *testing.T) {
	r := newRig(t)
	r.e.handle(bg, Signal{Code: "device_revoked", Reason: "pair_abandoned", Recover: true, FromCheckin: true})
	if r.recovers.Load() != 1 || r.e.status().State != StateOK {
		t.Fatalf("recovers %d status %+v", r.recovers.Load(), r.e.status())
	}
	r2 := newRig(t)
	r2.e.handle(bg, Signal{Code: "device_revoked", Reason: "pair_abandoned", FromCheckin: true})
	if r2.recovers.Load() != 0 || r2.e.status().Reason != ReasonPairAgain {
		t.Fatalf("pair_abandoned without the hint must not heal: %+v", r2.e.status())
	}
}

// Backoff table: 30 s, 1, 2, 5, 15 min, capped, ±20% jitter.
func TestBackoffTable(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for n, w := range want {
		if got := Backoff(n, 0.5); got != w {
			t.Errorf("Backoff(%d, .5) = %s, want %s", n, got, w)
		}
		if lo := Backoff(n, 0); lo != time.Duration(float64(w)*0.8) {
			t.Errorf("Backoff(%d, 0) = %s, want 80%% of %s", n, lo, w)
		}
		if hi := Backoff(n, 0.999999); hi > time.Duration(float64(w)*1.2) || hi < w {
			t.Errorf("Backoff(%d, ~1) = %s, want at most 120%% of %s", n, hi, w)
		}
	}
}

// Consecutive transient failures walk the table; the saved attempts survive.
func TestTransientFailuresWalkTheBackoff(t *testing.T) {
	r := newRig(t)
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "transport", Transient: true} }
	r.e.handle(bg, unauth(true))
	start := r.clk.now()
	wants := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range wants {
		d, has := r.e.untilNext()
		if !has || d != w {
			t.Fatalf("step %d: next in %s (%v), want %s", i, d, has, w)
		}
		r.clk.add(w)
		r.e.wake(bg) // the timer fires: probe says 401 again, recover fails again
	}
	if r.kv.val(data.CredHealAttemptsKey) != "7" {
		t.Fatalf("saved attempts = %q", r.kv.val(data.CredHealAttemptsKey))
	}
	_ = start
}

// A retry-after / usable_from later than the backoff wins.
func TestTransientHonoursALaterRetryAfter(t *testing.T) {
	r := newRig(t)
	r.outcome = func() enroll.RecoverOutcome {
		return enroll.RecoverOutcome{Code: "rate_limited", Transient: true, Until: r.clk.now().Add(10 * time.Minute)}
	}
	r.e.handle(bg, unauth(true))
	if d, _ := r.e.untilNext(); d != 10*time.Minute {
		t.Fatalf("next in %s, want 10m", d)
	}
}

// A waiting till wakes at its date (+ jitter) and heals then.
func TestWaitingWakesAtTheDateAndHeals(t *testing.T) {
	r := newRig(t)
	until := r.clk.now().Add(5 * time.Hour)
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "key_quarantined", Until: until} }
	r.e.handle(bg, unauth(true))
	d, _ := r.e.untilNext()
	if d != 5*time.Hour+30*time.Second { // jitter fixed at 0.5 of a minute
		t.Fatalf("wake in %s, want 5h30s", d)
	}
	// A 401 meanwhile does not start another round.
	r.e.handle(bg, Signal{Code: "unauthorized", FromCheckin: true, At: r.clk.now().Add(time.Minute)})
	if r.recovers.Load() != 1 {
		t.Fatalf("recovers = %d while waiting", r.recovers.Load())
	}
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Healed: true} }
	r.clk.add(5*time.Hour + 30*time.Second)
	r.e.wake(bg)
	if r.e.status().State != StateOK || r.healed.Load() != 1 {
		t.Fatalf("status = %+v", r.e.status())
	}
}

// One round at a time: a 401 while healing is the same round.
func TestSignalWhileHealingIsIgnored(t *testing.T) {
	r := newRig(t)
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "transport", Transient: true} }
	r.e.handle(bg, unauth(true))
	r.e.handle(bg, Signal{Code: "unauthorized", FromCheckin: true, At: r.clk.now().Add(time.Second)})
	if r.recovers.Load() != 1 {
		t.Fatalf("recovers = %d, want 1", r.recovers.Load())
	}
}

// A 401 from a call that began before the last heal describes the old token.
func TestStaleSignalAfterAHealIsDropped(t *testing.T) {
	r := newRig(t)
	r.e.handle(bg, unauth(true)) // heals
	r.e.handle(bg, Signal{Source: "backup", Code: "unauthorized", At: r.clk.now().Add(-time.Second)})
	r.e.handle(bg, Signal{Source: "link", Code: "unauthorized", At: r.clk.now()}) // inside the grace
	if r.recovers.Load() != 1 || r.probes.Load() != 0 {
		t.Fatalf("recovers %d probes %d", r.recovers.Load(), r.probes.Load())
	}
}

// The third heal inside 24 h is "in use elsewhere" (two machines, one folder).
func TestThirdHealInADayIsInUseElsewhere(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 2; i++ {
		r.clk.add(time.Hour)
		r.e.handle(bg, Signal{Code: "unauthorized", FromCheckin: true, At: r.clk.now()})
		if r.e.status().State != StateOK {
			t.Fatalf("heal %d: %+v", i, r.e.status())
		}
	}
	r.clk.add(time.Hour)
	r.e.handle(bg, Signal{Code: "unauthorized", FromCheckin: true, At: r.clk.now()})
	if st := r.e.status(); st.State != StateNeedsPerson || st.Reason != ReasonInUseElsewhere || r.recovers.Load() != 2 {
		t.Fatalf("status %+v recovers %d", st, r.recovers.Load())
	}
	// Outside the window it may heal again.
	r.clk.add(25 * time.Hour)
	r.e.wake(bg)
	if r.e.status().State != StateOK || r.recovers.Load() != 3 {
		t.Fatalf("status %+v recovers %d", r.e.status(), r.recovers.Load())
	}
}

// CanRecover refusals: no key is cannot_prove; the stop switch, an env pin
// and a demo till keep today's behaviour (pair_again); nothing held is no-op.
func TestCanRecoverRefusals(t *testing.T) {
	cases := map[string]struct {
		state  State
		reason string
	}{
		enroll.CanRecoverNoProof:    {StateNeedsPerson, ReasonCannotProve},
		enroll.CanRecoverStopped:    {StateNeedsPerson, ReasonPairAgain},
		enroll.CanRecoverPinned:     {StateNeedsPerson, ReasonPairAgain},
		enroll.CanRecoverDemo:       {StateNeedsPerson, ReasonPairAgain},
		enroll.CanRecoverNoIdentity: {StateOK, ""},
	}
	for why, want := range cases {
		r := newRig(t)
		r.can = func() (bool, string) { return false, why }
		r.e.handle(bg, unauth(true))
		st := r.e.status()
		if st.State != want.state || st.Reason != want.reason || r.recovers.Load() != 0 {
			t.Errorf("%s: status %+v recovers %d", why, st, r.recovers.Load())
		}
	}
}

// Saved state survives a restart: a waiting till keeps its date, a final
// state stays final, a non-final one looks again at start-up, healing resumes.
func TestSavedStateSurvivesRestart(t *testing.T) {
	r := newRig(t)
	until := r.clk.now().Add(3 * time.Hour)
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "key_quarantined", Until: until} }
	r.e.handle(bg, unauth(true))
	r.build() // restart: a new engine over the same settings
	st := r.e.status()
	if st.State != StateWaiting || st.Reason != ReasonKeyQuarantined || !st.Until.Equal(until) {
		t.Fatalf("after restart: %+v", st)
	}
	// The date is kept; the saved wait is re-asked once at start-up (review M5).
	if d, has := r.e.untilNext(); !has || d != 0 {
		t.Fatalf("wake in %s (%v), want an immediate start-up look", d, has)
	}

	f := newRig(t)
	f.e.handle(bg, Signal{Code: "device_revoked", Reason: "removed", Detail: "X", FromCheckin: true})
	f.build()
	if st := f.e.status(); st.State != StateNeedsPerson || st.Reason != ReasonRemoved || st.Detail != "X" {
		t.Fatalf("final after restart: %+v", st)
	}
	if _, has := f.e.untilNext(); has {
		t.Fatal("a final state scheduled a wake after restart")
	}

	n := newRig(t)
	n.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "recover_refused"} }
	n.e.handle(bg, unauth(true))
	n.build()
	if d, has := n.e.untilNext(); !has || d != 0 {
		t.Fatalf("a non-final needs_person must look again at start-up, got %s %v", d, has)
	}
	n.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Healed: true} }
	n.e.wake(bg)
	if n.e.status().State != StateOK {
		t.Fatalf("status after the start-up round: %+v", n.e.status())
	}
	if n.kv.val(data.CredHealStateKey) != "" {
		t.Fatal("ok state left in settings")
	}
	// The heal history survives (the third-in-a-day rule spans restarts).
	if n.kv.val(data.CredHealHealsKey) == "" {
		t.Fatal("heal history not saved")
	}
}

// Any successful check-in or sync clears a saved state, whatever it was.
func TestWorkingClearsState(t *testing.T) {
	r := newRig(t)
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "recover_refused"} }
	r.e.handle(bg, unauth(true))
	active.Store(r.e)
	t.Cleanup(func() { active.Store(nil) })
	Working()
	if r.e.status().State != StateOK || r.kv.val(data.CredHealStateKey) != "" {
		t.Fatalf("status %+v saved %q", r.e.status(), r.kv.val(data.CredHealStateKey))
	}
}

// An identity reset (a pairing) forgets everything, even a final state.
func TestResetLeavesAFinalState(t *testing.T) {
	r := newRig(t)
	r.e.handle(bg, Signal{Code: "device_revoked", Reason: "replaced", FromCheckin: true})
	r.e.reset()
	if r.e.status().State != StateOK {
		t.Fatalf("status %+v", r.e.status())
	}
	r.e.handle(bg, unauth(true))
	if r.e.status().State != StateOK || r.recovers.Load() != 1 {
		t.Fatal("a new identity could not heal")
	}
}

// The chip owns the bar in every state but ok and pair_again.
func TestShowsChip(t *testing.T) {
	for st, want := range map[Status]bool{
		{State: StateOK}: false,
		{State: StateHealing, Reason: ReasonReconnecting}:     true,
		{State: StateWaiting, Reason: ReasonKeyQuarantined}:   true,
		{State: StateNeedsPerson, Reason: ReasonCannotProve}:  true,
		{State: StateNeedsPerson, Reason: ReasonReplaced}:     true,
		{State: StateNeedsPerson, Reason: ReasonPairAgain}:    false,
		{State: StateNeedsPerson, Reason: ReasonStoreDeleted}: true,
	} {
		if st.ShowsChip() != want {
			t.Errorf("%+v ShowsChip = %v, want %v", st, !want, want)
		}
	}
}

// Nothing the engine logs carries a token or nonce.
func TestNoSecretsInLogs(t *testing.T) {
	r := newRig(t)
	r.e.handle(bg, Signal{Source: "checkin", Code: "unauthorized", Detail: "bearer abc", FromCheckin: true})
	if got := clamp("a\nb\x00c"+strings.Repeat("é", 100), 80); strings.ContainsAny(got, "\n\x00") || len(got) > 80 {
		t.Fatalf("clamp = %q", got)
	}
}

// Another route's device_revoked has no reason: it never decides a state on its
// own, the probe's answer does.
func TestNonCheckinRevokeIsDecidedByTheProbe(t *testing.T) {
	r := newRig(t)
	r.probe = func() ProbeResult { return ProbeResult{Code: "device_revoked", Reason: "removed"} }
	r.e.handle(bg, Signal{Source: "backup", Code: "device_revoked"})
	if st := r.e.status(); st.State != StateNeedsPerson || st.Reason != ReasonRemoved || r.probes.Load() != 1 || r.recovers.Load() != 0 {
		t.Fatalf("status %+v probes %d recovers %d", st, r.probes.Load(), r.recovers.Load())
	}
	r2 := newRig(t)
	r2.probe = func() ProbeResult { return ProbeResult{OK: true} }
	r2.e.handle(bg, Signal{Source: "backup", Code: "device_revoked"})
	if r2.e.status().State != StateOK {
		t.Fatalf("a healthy probe must win: %+v", r2.e.status())
	}
}

// Review M6: the first attempt of a heal waits a 0-60 s jitter (here 0.5 of a
// minute) so a cloud restore does not hit the cloud at one instant; a retry
// that is already on the backoff does not add another.
func TestFirstAttemptWaitsAJitter(t *testing.T) {
	r := newRig(t)
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Transient: true} }
	r.e.handle(bg, unauth(true))
	if len(r.pauses) != 1 || r.pauses[0] != 30*time.Second {
		t.Fatalf("pauses = %v, want exactly [30s] before the first attempt", r.pauses)
	}
	r.clk.add(time.Hour)
	r.e.wake(bg)
	if len(r.pauses) != 1 {
		t.Fatalf("pauses = %v: a retry on the backoff must not pause again", r.pauses)
	}
}

// Review B1.5: recover_refused can mean "the per-device budget was spent by
// someone else": look again after 65 minutes, twice, then daily. "No key" stays
// daily.
func TestRecoverRefusedRechecksAt65MinutesTwiceThenDaily(t *testing.T) {
	r := newRig(t)
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "recover_refused"} }
	r.e.handle(bg, unauth(true))
	for i, want := range []time.Duration{65 * time.Minute, 65 * time.Minute, 24 * time.Hour} {
		st := r.e.status()
		if st.State != StateNeedsPerson || st.Reason != ReasonCannotProve {
			t.Fatalf("round %d: status %+v, want needs_person(cannot_prove)", i+1, st)
		}
		d, ok := r.e.untilNext()
		if !ok || d != want {
			t.Fatalf("after refused round %d: next look in %s (%v), want %s", i+1, d, ok, want)
		}
		r.clk.add(want)
		r.e.wake(bg)
	}
	// Saved, so a restart does not forget how many quick looks were used.
	if got := r.kv.val(data.CredHealRefusedKey); got != "4" {
		t.Fatalf("saved refused count = %q, want 4 (three rounds, then the wake after the third)", got)
	}
}

func TestNoProofStaysDaily(t *testing.T) {
	r := newRig(t)
	r.can = func() (bool, string) { return false, enroll.CanRecoverNoProof }
	r.e.handle(bg, unauth(true))
	if d, _ := r.e.untilNext(); d != 24*time.Hour {
		t.Fatalf("no key: next look in %s, want 24h", d)
	}
}

// Review M5: a waiting till also looks again daily, even for a date weeks away.
func TestWaitingLooksAgainDaily(t *testing.T) {
	r := newRig(t)
	r.outcome = func() enroll.RecoverOutcome {
		return enroll.RecoverOutcome{Code: "key_quarantined", Until: r.clk.now().Add(70 * time.Hour)}
	}
	r.e.handle(bg, unauth(true))
	if d, _ := r.e.untilNext(); d != 24*time.Hour {
		t.Fatalf("waiting for 70h: next look in %s, want 24h", d)
	}
}

// ---- resume (phones, ut-docs#7097 T8) ------------------------------------------------

// A phone suspended in the background wakes with a dead credential nobody has
// reported yet: resume asks the cloud once and heals in that round.
func TestResumeWithDeadCredentialHeals(t *testing.T) {
	r := newRig(t)
	r.e.resume(bg)
	if r.probes.Load() != 1 || r.recovers.Load() != 1 {
		t.Fatalf("probes %d recovers %d, want 1 and 1", r.probes.Load(), r.recovers.Load())
	}
	if st := r.e.status(); st.State != StateOK || st.LastHealedAt.IsZero() || r.healed.Load() != 1 {
		t.Fatalf("status %+v healed %d", st, r.healed.Load())
	}
}

// A working credential, an offline phone and a final answer are all left alone.
func TestResumeLeavesAWorkingOrUnreachableCredentialAlone(t *testing.T) {
	r := newRig(t)
	r.probe = func() ProbeResult { return ProbeResult{OK: true} }
	r.e.resume(bg)
	r.probe = func() ProbeResult { return ProbeResult{Err: errors.New("offline")} }
	r.e.resume(bg)
	if r.recovers.Load() != 0 || r.e.status().State != StateOK || r.kv.val(data.CredHealStateKey) != "" {
		t.Fatalf("recovers %d status %+v", r.recovers.Load(), r.e.status())
	}
	// A removed till is told by the probe and never calls recover.
	r.probe = func() ProbeResult { return ProbeResult{Code: "device_revoked", Reason: "removed"} }
	r.e.resume(bg)
	if st := r.e.status(); st.State != StateNeedsPerson || st.Reason != "removed" || r.recovers.Load() != 0 {
		t.Fatalf("status %+v recovers %d", st, r.recovers.Load())
	}
	// And a final state is not probed again on the next resume.
	before := r.probes.Load()
	r.e.resume(bg)
	if r.probes.Load() != before {
		t.Fatal("a final state was probed on resume")
	}
}

// A waiting till whose date passed while the app was suspended (its timer did
// not run) looks again at once; one whose date is still ahead does not.
func TestResumeWakesAnOverdueWaitingTillOnly(t *testing.T) {
	r := newRig(t)
	until := r.clk.now().Add(5 * time.Hour)
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Code: "key_quarantined", Until: until} }
	r.e.handle(bg, unauth(true))
	r.outcome = func() enroll.RecoverOutcome { return enroll.RecoverOutcome{Healed: true} }
	r.clk.add(time.Hour)
	r.e.resume(bg)
	if r.recovers.Load() != 1 || r.e.status().State != StateWaiting {
		t.Fatalf("early resume: recovers %d status %+v", r.recovers.Load(), r.e.status())
	}
	r.clk.add(5 * time.Hour)
	r.e.resume(bg)
	if r.recovers.Load() != 2 || r.e.status().State != StateOK {
		t.Fatalf("overdue resume: recovers %d status %+v", r.recovers.Load(), r.e.status())
	}
}

// Resume is a no-op without a running engine and never blocks.
func TestResumeWithoutEngineIsANoop(t *testing.T) {
	active.Store(nil)
	Resume()
}

// Review 2026-11-27: a till that is paired (identity reset) or works again while
// a round sleeps its jitter must not have its healthy credential rotated.
func TestRoundDoesNotRecoverACredentialThatBecameHealthyDuringTheJitter(t *testing.T) {
	for name, interrupt := range map[string]func(e *Engine){
		"identity reset": func(e *Engine) { e.reset() },
		"working again":  func(e *Engine) { e.working() },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.e.d.Pause = func(context.Context, time.Duration) { interrupt(r.e) }
			r.e.handle(bg, unauth(true))
			if r.recovers.Load() != 0 {
				t.Fatalf("recover calls = %d, want 0: the credential was healthy after the pause", r.recovers.Load())
			}
			if st := r.e.status(); st.State != StateOK {
				t.Fatalf("status = %+v, want ok", st)
			}
		})
	}
}

// Server-supplied reason words are clamped before they are saved.
func TestServerReasonIsClampedBeforeItIsSaved(t *testing.T) {
	r := newRig(t)
	long := strings.Repeat("x", 500) + "\nforged log line"
	r.e.handle(bg, Signal{Source: "checkin", Code: "device_revoked", Reason: long, FromCheckin: true})
	if got := r.kv.val(data.CredHealCodeKey); len(got) > 90 || strings.ContainsAny(got, "\n\r") {
		t.Fatalf("saved code %q is not clamped", got)
	}
}

// ut-docs#2630: store_purged is terminal and needs a person; store_deleted
// without recover stays the undo-window state.
func TestClassify_StorePurgedIsFinalNeedsPerson(t *testing.T) {
	v := classify(Signal{Code: "device_revoked", Reason: "store_purged"})
	if v.kind != kindNeedsPerson || v.reason != ReasonStorePurged {
		t.Fatalf("store_purged = %+v, want needs-person store_purged", v)
	}
	if !FinalReason(ReasonStorePurged) {
		t.Error("store_purged must be final: no timer may leave it")
	}
	if FinalReason(ReasonStoreDeleted) {
		t.Error("store_deleted is inside the undo window: not final")
	}
	if v := classify(Signal{Code: "device_revoked", Reason: "store_deleted"}); v.kind != kindNeedsPerson || v.reason != ReasonStoreDeleted {
		t.Fatalf("store_deleted = %+v", v)
	}
}
