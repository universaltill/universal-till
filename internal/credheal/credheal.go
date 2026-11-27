// Package credheal is the till's self-heal engine (ADR-0222, design §4): when
// the cloud stops accepting this till's credential, it proves its approval key
// and gets a new credential for the same store and device, in the background,
// with no human. A human is asked only when the till cannot prove who it is,
// or the owner (or the cloud) took the till out on purpose; the engine then
// holds one plain-words state with a reason code for the chip.
//
// Rules that hold in every state: one round at a time; nothing runs on a
// request goroutine and no DB transaction is held across the network; the sale
// path never reads this package; identity is never cleared here (the only write
// is enroll.Recover's one token adoption after the cloud's 200); no token, key,
// nonce or hash is ever logged.
//
// Import direction: cloudsync, offsitebackup, cloudlink and the other cloud
// callers import this package (Report); it imports enroll, approvalkey, data
// and logging only, and gets everything else from pages through SetHooks.
package credheal

import (
	"context"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// State is the engine's state.
type State string

const (
	StateOK          State = "ok"
	StateHealing     State = "healing"
	StateWaiting     State = "waiting"
	StateNeedsPerson State = "needs_person"
)

// Reason codes: the chip data the UI renders (design §4.7). Healing carries
// ReasonReconnecting; waiting carries why it waits.
const (
	ReasonReconnecting   = "reconnecting"
	ReasonKeyQuarantined = "key_quarantined"
	ReasonRecoverLimit   = "recover_limit"
	ReasonCannotProve    = "cannot_prove"
	ReasonReplaced       = "replaced"
	ReasonMoved          = "moved"
	ReasonRemoved        = "removed"
	ReasonStoreDeleted   = "store_deleted"
	ReasonRoleConflict   = "role_conflict"
	ReasonInUseElsewhere = "in_use_elsewhere"
	ReasonPairAgain      = "pair_again" // today's behaviour: an older cloud, switched off, stopped or pinned
)

// FinalReason reports whether a reason can only be left by pairing, joining or
// registering (never by a timer): the owner's or the cloud's decision.
func FinalReason(r string) bool {
	return r == ReasonReplaced || r == ReasonMoved || r == ReasonRemoved
}

// Status is what the chip and the Tills page read. Local, never blocks.
type Status struct {
	State        State
	Reason       string
	Detail       string // a till or shop name from the cloud, clamped
	Since, Until time.Time
	LastHealedAt time.Time
}

// ShowsChip reports whether the heal chip owns the status bar: every state
// but ok, except pair_again, which keeps today's chips.
func (s Status) ShowsChip() bool {
	return s.State != StateOK && !(s.State == StateNeedsPerson && s.Reason == ReasonPairAgain)
}

// Signal is one caller's evidence that the credential is dead: a 401 with an
// envelope code. A 401 without one is never reported (a proxy's answer).
type Signal struct {
	Source      string // checkin, sync, catalog, stock, backup, link, approval_key...
	Code        string // unauthorized | token_retired | device_revoked
	Reason      string // data.reason of a device_revoked
	Detail      string
	Recover     bool // data.recover: the cloud says this revocation is healable
	FromCheckin bool // the check-in GET or the sync POST: the probe is already answered
	At          time.Time
}

// ProbeResult is a check-in probe's answer: OK when the cloud accepted the
// credential, else the 401's code, reason and hint; Err for a transport failure
// or any answer that says nothing about the credential.
type ProbeResult struct {
	OK      bool
	Code    string
	Reason  string
	Recover bool
	Err     error
}

// Hooks are wired by pages (they reach what this package may not import).
type Hooks struct {
	// OnHealed runs after a successful heal (clear the 401 lock-out, kick a
	// check-in, clear the backup refusal wait, redial the cloud link).
	OnHealed func()
	// Probe answers a check-in with the current credential.
	Probe func(ctx context.Context) ProbeResult
	// OnState is told each state change.
	OnState func(Status)
}

// Settings is the persistence the engine needs (enroll.Settings).
type Settings = enroll.Settings

// Deps is everything Start needs. Cfg and KV are required; the rest default
// to the real enroll calls and the wall clock (tests replace them).
type Deps struct {
	Cfg        *config.Config
	KV         Settings
	CanRecover func(ctx context.Context) (bool, string)
	Recover    func(ctx context.Context) (enroll.RecoverOutcome, error)
	Now        func() time.Time
	Jitter     func() float64 // in [0,1)
	// Pause waits d or until ctx ends (tests replace it). It carries the
	// 0-60 s jitter before a first attempt (ADR-0222 review M6).
	Pause func(ctx context.Context, d time.Duration)
}

var (
	hooksMu sync.RWMutex
	hooks   Hooks
	active  atomic.Pointer[Engine]
)

// SetHooks installs pages' hooks. Call before Start.
func SetHooks(h Hooks) {
	hooksMu.Lock()
	hooks = h
	hooksMu.Unlock()
}

func getHooks() Hooks {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	return hooks
}

// Backoff is the wait after the n-th consecutive transient failure (n from 0):
// 30 s, 1, 2, 5, 15 min, then 15 min, each with ±20% jitter (j in [0,1)).
func Backoff(n int, j float64) time.Duration {
	steps := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}
	if n < 0 {
		n = 0
	}
	if n >= len(steps) {
		n = len(steps) - 1
	}
	return time.Duration(float64(steps[n]) * (0.8 + 0.4*j))
}

const (
	recheckEvery    = 24 * time.Hour   // a non-final needs_person looks again daily
	refusedRecheck  = 65 * time.Minute // after recover_refused: the per-device budget may have been spent by someone else (review B1.5)
	refusedRechecks = 2                // that quick look happens twice, then daily
	firstJitter     = time.Minute      // 0-60 s before the first attempt of a heal (review M6)
	healWindow      = 24 * time.Hour
	maxHealsInDay   = 2 // a third heal inside the window is "in use elsewhere"
	graceAfterHeal  = 10 * time.Second
	sigBuffer       = 8
)

// Engine is one running heal engine.
type Engine struct {
	d Deps

	mu       sync.Mutex
	st       Status
	code     string    // the 401 code/reason that drove the saved state
	attempts int       // consecutive transient failures
	refused  int       // rounds that ended in recover_refused since the last heal
	nextAt   time.Time // when the loop wakes next; zero = never
	pending  time.Time // a healable signal arrived, not yet handled
	settled  time.Time // the last round or heal finished
	healed   time.Time
	heals    []time.Time
	gen      uint64 // bumped when the credential's story changes under a running round (reset, working)

	sig      chan Signal
	resumeCh chan struct{}
}

func newEngine(d Deps) *Engine {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Jitter == nil {
		d.Jitter = rand.Float64
	}
	if d.Pause == nil {
		d.Pause = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if d.CanRecover == nil {
		d.CanRecover = func(ctx context.Context) (bool, string) { return enroll.CanRecover(ctx, d.Cfg, d.KV) }
	}
	if d.Recover == nil {
		d.Recover = func(ctx context.Context) (enroll.RecoverOutcome, error) { return enroll.Recover(ctx, d.Cfg, d.KV) }
	}
	return &Engine{d: d, st: Status{State: StateOK}, sig: make(chan Signal, sigBuffer), resumeCh: make(chan struct{}, 1)}
}

// Start loads the saved state and runs the engine until ctx ends. wg, when
// given, tracks the goroutine. It installs the package's callbacks into enroll.
func Start(ctx context.Context, d Deps, wg *sync.WaitGroup) *Engine {
	e := newEngine(d)
	e.load(ctx)
	active.Store(e)
	enroll.SetDeadCredentialHook(func(source, code, reason string, recover bool) {
		Report(Signal{Source: source, Code: code, Reason: reason, Recover: recover})
	})
	enroll.SetIdentityResetHook(e.reset)
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		defer logging.RecoverAndLog("credheal.run")
		if wg != nil {
			defer wg.Done()
		}
		defer func() {
			active.CompareAndSwap(e, nil)
			enroll.SetDeadCredentialHook(nil)
			enroll.SetIdentityResetHook(nil)
		}()
		e.run(ctx)
	}()
	return e
}

// Report tells the engine a cloud caller got a dead-credential answer. It never
// blocks and is safe from any goroutine; with no engine running it does
// nothing. The chip changes at once (before the engine's goroutine runs).
func Report(s Signal) {
	e := active.Load()
	if e == nil {
		return
	}
	if s.At.IsZero() {
		s.At = e.d.Now()
	}
	e.mu.Lock()
	if e.st.State == StateOK && e.pending.IsZero() && classify(s).kind == kindHeal {
		e.pending = s.At
	}
	e.mu.Unlock()
	select {
	case e.sig <- s:
	default: // a flood is one signal: the engine is already about to act
	}
}

// Resume tells the engine the app came back to the foreground (a phone shell
// calls it through mobile.Resume). While suspended the process's timers do not
// run, so a dead credential or an overdue wake would otherwise wait for the next
// tick. Never blocks; safe from any goroutine; nothing without a running engine.
func Resume() {
	e := active.Load()
	if e == nil {
		return
	}
	select {
	case e.resumeCh <- struct{}{}:
	default: // one pending resume is enough
	}
}

// Current is the status the chip reads. Local and instant.
func Current() Status {
	e := active.Load()
	if e == nil {
		return Status{State: StateOK}
	}
	return e.status()
}

// Working tells the engine a check-in or sync really succeeded: the credential
// is fine, whatever it believed. Clears saved state. Cheap when already ok.
func Working() {
	if e := active.Load(); e != nil {
		e.working()
	}
}

func (e *Engine) status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.st
	if st.State == StateOK && !e.pending.IsZero() {
		st.State, st.Reason, st.Since = StateHealing, ReasonReconnecting, e.pending
	}
	return st
}

// ---- classification --------------------------------------------------------

type kind int

const (
	kindIgnore kind = iota
	kindHeal
	kindNeedsPerson
)

type verdict struct {
	kind   kind
	reason string // for kindNeedsPerson
}

// classify says what a dead-credential answer means (design §4.4 table, first
// rows). Nothing is guessed from a code the till does not know.
func classify(s Signal) verdict {
	switch s.Code {
	case "unauthorized", "token_retired":
		return verdict{kind: kindHeal}
	case "device_revoked":
		switch s.Reason {
		case "replaced", "moved", "removed":
			return verdict{kind: kindNeedsPerson, reason: s.Reason}
		}
		if s.Recover {
			return verdict{kind: kindHeal}
		}
		if s.Reason == "store_deleted" {
			return verdict{kind: kindNeedsPerson, reason: ReasonStoreDeleted}
		}
		// An older cloud (no reason) or a reason this till does not know: today's behaviour.
		return verdict{kind: kindNeedsPerson, reason: ReasonPairAgain}
	}
	return verdict{kind: kindIgnore}
}

func signalKey(code, reason string) string { return code + "/" + reason }

// ---- the loop ----------------------------------------------------------------

func (e *Engine) run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if d, ok := e.untilNext(); ok {
			timer.Reset(d)
		}
		select {
		case <-ctx.Done():
			return
		case s := <-e.sig:
			e.handle(ctx, s)
		case <-e.resumeCh:
			e.resume(ctx)
		case <-timer.C:
			e.wake(ctx)
		}
	}
}

func (e *Engine) untilNext() (time.Duration, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.nextAt.IsZero() {
		return 0, false
	}
	d := e.nextAt.Sub(e.d.Now())
	if d < 0 {
		d = 0
	}
	return d, true
}

// handle folds one signal in (loop goroutine only).
func (e *Engine) handle(ctx context.Context, s Signal) {
	// Server-supplied words are clamped before they are logged or saved.
	s.Code, s.Reason = clamp(s.Code, 40), clamp(s.Reason, 40)
	v := classify(s)
	if v.kind == kindNeedsPerson && !s.FromCheckin {
		// Another route's device_revoked carries no reason: the probe (a
		// check-in) asks the cloud for it before anything is decided.
		v = verdict{kind: kindHeal}
	}
	e.mu.Lock()
	e.pending = time.Time{}
	now := e.d.Now()
	// A signal from a call that started before the last round finished, or
	// within the grace after a heal, describes the credential the heal replaced.
	stale := s.At.Before(e.settled) || (!e.healed.IsZero() && now.Sub(e.healed) < graceAfterHeal)
	st, key := e.st, signalKey(s.Code, s.Reason)
	sameAsSaved := key == e.code
	waitingUntil := e.nextAt
	e.mu.Unlock()

	switch {
	case v.kind == kindIgnore:
		logging.L().Debugf("credheal: ignored a %s answer from %s (not a dead-credential code)", orNone(s.Code), s.Source)
		return
	case stale:
		return
	case st.State == StateNeedsPerson && FinalReason(st.Reason):
		return // only a pairing, join or registration leaves it
	}
	if v.kind == kindNeedsPerson {
		if st.State == StateNeedsPerson && st.Reason == v.reason {
			return
		}
		e.enter(ctx, StateNeedsPerson, v.reason, s.Detail, time.Time{}, key)
		e.scheduleRecheck(ctx, v.reason)
		return
	}
	// A healable answer.
	switch st.State {
	case StateHealing:
		return // one round at a time; the loop is the round
	case StateWaiting:
		if waitingUntil.After(now) {
			return
		}
	case StateNeedsPerson:
		if sameAsSaved && waitingUntil.After(now) {
			return // the same answer again before the daily look
		}
	}
	e.enter(ctx, StateHealing, ReasonReconnecting, "", time.Time{}, key)
	e.round(ctx, s.FromCheckin, s)
}

// resume is Resume on the loop goroutine: from ok, ask the cloud once (a probe is a real check-in); a dead
// answer is handled exactly like the first 401 of a tick. From any other state
// that is not final, an overdue wake (the timer did not run while suspended)
// runs now; a wake still ahead (a backoff) is left alone.
func (e *Engine) resume(ctx context.Context) {
	e.mu.Lock()
	st, next, pending := e.st, e.nextAt, !e.pending.IsZero()
	e.mu.Unlock()
	now := e.d.Now()
	switch {
	case st.State == StateNeedsPerson && FinalReason(st.Reason):
		return
	case st.State == StateOK && !pending:
		h := getHooks()
		if h.Probe == nil {
			return
		}
		pr := h.Probe(ctx)
		if ctx.Err() != nil || pr.OK || pr.Err != nil || pr.Code == "" {
			return // working, offline or an answer that says nothing about the credential
		}
		e.handle(ctx, Signal{Source: "resume", Code: pr.Code, Reason: pr.Reason, Recover: pr.Recover, FromCheckin: true, At: now})
	case !next.IsZero() && !next.After(now):
		e.wake(ctx)
	}
}

// wake is the timer firing: a backoff, a waiting date or the daily look.
func (e *Engine) wake(ctx context.Context) {
	e.mu.Lock()
	e.nextAt = time.Time{}
	st := e.st
	key := e.code
	e.mu.Unlock()
	switch st.State {
	case StateHealing:
	case StateWaiting, StateNeedsPerson:
		if st.State == StateNeedsPerson && FinalReason(st.Reason) {
			return
		}
		e.enter(ctx, StateHealing, ReasonReconnecting, "", time.Time{}, key)
	default:
		return
	}
	e.round(ctx, false, Signal{})
}

// round is one heal round: optionally a probe, then enroll.Recover.
func (e *Engine) round(ctx context.Context, skipProbe bool, s Signal) {
	defer func() {
		e.mu.Lock()
		e.settled = e.d.Now()
		e.mu.Unlock()
	}()
	log := logging.L()
	e.mu.Lock()
	gen := e.gen
	e.mu.Unlock()
	if h := getHooks(); !skipProbe && h.Probe != nil {
		pr := h.Probe(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case pr.Err != nil:
			e.transient(ctx, time.Time{})
			return
		case pr.OK:
			if s.Source != "" {
				log.Warnf("credheal: %s refuses a working credential (%s); this till is not rotated", s.Source, orNone(s.Code))
			}
			e.markOK(ctx)
			return
		default:
			switch v := classify(Signal{Code: pr.Code, Reason: pr.Reason, Recover: pr.Recover}); v.kind {
			case kindNeedsPerson:
				e.enter(ctx, StateNeedsPerson, v.reason, "", time.Time{}, signalKey(pr.Code, pr.Reason))
				e.scheduleRecheck(ctx, v.reason)
				return
			case kindIgnore:
				e.transient(ctx, time.Time{}) // a 401 with no usable code says nothing
				return
			}
		}
	}
	if ok, why := e.d.CanRecover(ctx); !ok {
		e.cannotRecover(ctx, why)
		return
	}
	e.mu.Lock()
	first := e.attempts == 0
	e.mu.Unlock()
	if first {
		// A cloud restore makes every keyed till fail at once: spread them.
		e.d.Pause(ctx, time.Duration(e.d.Jitter()*float64(firstJitter)))
		if ctx.Err() != nil {
			return
		}
		// The till may have been paired, reset or have worked again while the
		// round slept: never rotate a credential that is healthy now.
		e.mu.Lock()
		changed := e.gen != gen || e.st.State != StateHealing
		e.mu.Unlock()
		if changed {
			return
		}
		if ok, why := e.d.CanRecover(ctx); !ok {
			e.cannotRecover(ctx, why)
			return
		}
	}
	if e.healsInWindow() >= maxHealsInDay {
		e.enter(ctx, StateNeedsPerson, ReasonInUseElsewhere, "", time.Time{}, "")
		e.scheduleRecheck(ctx, ReasonInUseElsewhere)
		return
	}
	out, err := e.d.Recover(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		e.transient(ctx, time.Time{})
		return
	}
	e.apply(ctx, out)
}

func (e *Engine) healsInWindow() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	cut := e.d.Now().Add(-healWindow)
	n := 0
	for _, t := range e.heals {
		if t.After(cut) {
			n++
		}
	}
	return n
}

func (e *Engine) cannotRecover(ctx context.Context, why string) {
	switch why {
	case enroll.CanRecoverNoIdentity:
		e.markOK(ctx) // nothing to heal: this till holds no credential
	case enroll.CanRecoverNoProof:
		e.enter(ctx, StateNeedsPerson, ReasonCannotProve, "", time.Time{}, "")
		e.scheduleRecheck(ctx, ReasonCannotProve)
	default: // stopped, env pin, demo: today's behaviour
		e.enter(ctx, StateNeedsPerson, ReasonPairAgain, "", time.Time{}, "")
		e.scheduleRecheck(ctx, ReasonPairAgain)
	}
}

// apply maps a recover outcome onto the table (design §4.4).
func (e *Engine) apply(ctx context.Context, out enroll.RecoverOutcome) {
	switch {
	case out.Healed:
		e.healedNow(ctx)
	case out.Transient:
		e.transient(ctx, out.Until)
	case out.Code == "key_quarantined" && !out.Until.IsZero():
		e.wait(ctx, ReasonKeyQuarantined, out.Until)
	case out.Code == "recover_limit" && !out.Until.IsZero():
		e.wait(ctx, ReasonRecoverLimit, out.Until)
	case out.Code == "recover_limit":
		e.enter(ctx, StateNeedsPerson, ReasonInUseElsewhere, "", time.Time{}, "")
		e.scheduleRecheck(ctx, ReasonInUseElsewhere)
	case out.Code == "role_conflict":
		e.enter(ctx, StateNeedsPerson, ReasonRoleConflict, "", time.Time{}, "")
		e.scheduleRecheck(ctx, ReasonRoleConflict)
	case out.Code == "device_revoked":
		v := classify(Signal{Code: out.Code, Reason: out.Reason})
		reason := v.reason
		if v.kind != kindNeedsPerson || reason == ReasonPairAgain {
			reason = ReasonCannotProve // proven, yet refused: ask the owner
		}
		e.enter(ctx, StateNeedsPerson, reason, out.Detail, time.Time{}, signalKey(out.Code, out.Reason))
		e.scheduleRecheck(ctx, reason)
	case out.Code == "recover_refused":
		// Twice in one round can also mean "the budget was spent by someone
		// else": look again after 65 minutes (twice), then daily.
		e.mu.Lock()
		e.refused++
		quick := e.refused <= refusedRechecks
		e.mu.Unlock()
		e.enter(ctx, StateNeedsPerson, ReasonCannotProve, "", time.Time{}, "")
		if quick {
			e.setNext(ctx, e.d.Now().Add(refusedRecheck))
		} else {
			e.scheduleRecheck(ctx, ReasonCannotProve)
		}
	case out.Code == "key_conflict", out.Code == "key_quarantined":
		e.enter(ctx, StateNeedsPerson, ReasonCannotProve, "", time.Time{}, "")
		e.scheduleRecheck(ctx, ReasonCannotProve)
	case out.Code == "cannot_recover":
		e.cannotRecover(ctx, out.Detail)
	default: // feature_absent (404), recover_disabled, bad_answer, anything new
		e.enter(ctx, StateNeedsPerson, ReasonPairAgain, "", time.Time{}, "")
		e.scheduleRecheck(ctx, ReasonPairAgain)
	}
}

// ---- state transitions ---------------------------------------------------------

func (e *Engine) healedNow(ctx context.Context) {
	now := e.d.Now()
	e.mu.Lock()
	e.healed = now
	e.heals = append(e.heals, now)
	if len(e.heals) > 8 {
		e.heals = e.heals[len(e.heals)-8:]
	}
	e.st = Status{State: StateOK, LastHealedAt: now}
	e.attempts, e.refused, e.nextAt, e.code = 0, 0, time.Time{}, ""
	e.mu.Unlock()
	e.save(ctx)
	logging.L().Infof("credheal: this till is connected to its shop again (new credential, same till)")
	if h := getHooks(); h.OnHealed != nil {
		h.OnHealed()
	}
	e.notify()
}

func (e *Engine) markOK(ctx context.Context) {
	e.mu.Lock()
	was := e.st.State != StateOK || !e.pending.IsZero()
	last := e.st.LastHealedAt
	e.st = Status{State: StateOK, LastHealedAt: last}
	e.attempts, e.refused, e.nextAt, e.code, e.pending = 0, 0, time.Time{}, "", time.Time{}
	e.mu.Unlock()
	if was {
		e.save(ctx)
		e.notify()
	}
}

func (e *Engine) working() {
	e.mu.Lock()
	idle := e.st.State == StateOK && e.pending.IsZero()
	if !idle {
		e.gen++
	}
	e.mu.Unlock()
	if idle {
		return
	}
	e.markOK(context.Background())
}

func (e *Engine) reset() {
	e.mu.Lock()
	last := e.st.LastHealedAt
	e.st = Status{State: StateOK, LastHealedAt: last}
	e.attempts, e.refused, e.nextAt, e.code, e.pending, e.heals = 0, 0, time.Time{}, "", time.Time{}, nil
	e.gen++
	e.mu.Unlock()
	e.notify()
}

// enter sets a state and saves it at once (the chip shows now).
func (e *Engine) enter(ctx context.Context, s State, reason, detail string, until time.Time, key string) {
	now := e.d.Now()
	e.mu.Lock()
	since := e.st.Since
	if e.st.State != s || since.IsZero() {
		since = now
	}
	last := e.st.LastHealedAt
	e.st = Status{State: s, Reason: reason, Detail: clamp(detail, 80), Since: since, Until: until, LastHealedAt: last}
	if key != "" {
		e.code = key
	}
	if s != StateHealing {
		e.attempts = 0
	}
	e.mu.Unlock()
	e.save(ctx)
	e.notify()
	logging.L().Infof("credheal: state %s (%s)", s, reason)
}

func (e *Engine) wait(ctx context.Context, reason string, until time.Time) {
	at := until.Add(time.Duration(e.d.Jitter() * float64(time.Minute)))
	if cut := e.d.Now().Add(recheckEvery); at.After(cut) {
		at = cut // a waiting till still looks again daily (review M5)
	}
	e.enter(ctx, StateWaiting, reason, "", until, "")
	e.setNext(ctx, at)
}

func (e *Engine) transient(ctx context.Context, floor time.Time) {
	e.mu.Lock()
	n := e.attempts
	e.attempts++
	e.mu.Unlock()
	at := e.d.Now().Add(Backoff(n, e.d.Jitter()))
	if floor.After(at) {
		at = floor
	}
	e.setNext(ctx, at)
}

func (e *Engine) scheduleRecheck(ctx context.Context, reason string) {
	if FinalReason(reason) {
		e.setNext(ctx, time.Time{})
		return
	}
	e.setNext(ctx, e.d.Now().Add(recheckEvery))
}

func (e *Engine) setNext(ctx context.Context, at time.Time) {
	e.mu.Lock()
	e.nextAt = at
	e.mu.Unlock()
	e.save(ctx)
}

func (e *Engine) notify() {
	if h := getHooks(); h.OnState != nil {
		h.OnState(e.status())
	}
}

func orNone(s string) string {
	if s == "" {
		return "no code"
	}
	return s
}

func clamp(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}

// ---- persistence -------------------------------------------------------------------

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (e *Engine) save(ctx context.Context) {
	if e.d.KV == nil {
		return
	}
	e.mu.Lock()
	st := e.st
	vals := map[string]string{
		data.CredHealStateKey:         string(st.State),
		data.CredHealReasonKey:        st.Reason,
		data.CredHealDetailKey:        st.Detail,
		data.CredHealSinceKey:         stamp(st.Since),
		data.CredHealUntilKey:         stamp(st.Until),
		data.CredHealNextAttemptAtKey: stamp(e.nextAt),
		data.CredHealAttemptsKey:      strconv.Itoa(e.attempts),
		data.CredHealLastHealedAtKey:  stamp(st.LastHealedAt),
		data.CredHealCodeKey:          e.code,
		data.CredHealRefusedKey:       strconv.Itoa(e.refused),
	}
	heals := make([]string, 0, len(e.heals))
	for _, t := range e.heals {
		heals = append(heals, strconv.FormatInt(t.Unix(), 10))
	}
	vals[data.CredHealHealsKey] = strings.Join(heals, ",")
	e.mu.Unlock()
	if st.State == StateOK {
		for _, k := range []string{data.CredHealStateKey, data.CredHealReasonKey, data.CredHealDetailKey, data.CredHealSinceKey,
			data.CredHealUntilKey, data.CredHealNextAttemptAtKey, data.CredHealAttemptsKey, data.CredHealCodeKey, data.CredHealRefusedKey} {
			vals[k] = ""
		}
	}
	// Not atomic, so ordered: heals and the details first, the state key last
	// (a crash then leaves the old state, which start-up re-runs a round for);
	// going back to ok clears the state key first.
	setOne := func(k string) {
		if err := e.d.KV.Set(ctx, k, vals[k]); err != nil {
			logging.L().Warnf("credheal: save %s: %v", k, err)
		}
	}
	if st.State == StateOK {
		setOne(data.CredHealStateKey)
	}
	for k := range vals {
		if k != data.CredHealStateKey {
			setOne(k)
		}
	}
	if st.State != StateOK {
		setOne(data.CredHealStateKey)
	}
}

// load restores the saved state at start-up: a healing or waiting round
// resumes on its timer, a non-final needs_person looks again at once.
func (e *Engine) load(ctx context.Context) {
	if e.d.KV == nil {
		return
	}
	get := func(k string) string { v, _, _ := e.d.KV.Get(ctx, k); return strings.TrimSpace(v) }
	parse := func(k string) time.Time { t, _ := time.Parse(time.RFC3339, get(k)); return t }
	for _, f := range strings.Split(get(data.CredHealHealsKey), ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64); err == nil {
			e.heals = append(e.heals, time.Unix(n, 0))
		}
	}
	e.st.LastHealedAt = parse(data.CredHealLastHealedAtKey)
	st := State(get(data.CredHealStateKey))
	switch st {
	case StateHealing, StateWaiting, StateNeedsPerson:
	default:
		return
	}
	reason := get(data.CredHealReasonKey)
	e.st = Status{State: st, Reason: reason, Detail: clamp(get(data.CredHealDetailKey), 80),
		Since: parse(data.CredHealSinceKey), Until: parse(data.CredHealUntilKey), LastHealedAt: e.st.LastHealedAt}
	e.code = get(data.CredHealCodeKey)
	e.attempts, _ = strconv.Atoi(get(data.CredHealAttemptsKey))
	e.refused, _ = strconv.Atoi(get(data.CredHealRefusedKey))
	e.nextAt = parse(data.CredHealNextAttemptAtKey)
	if (st == StateNeedsPerson && !FinalReason(reason)) || st == StateWaiting {
		e.nextAt = e.d.Now() // start-up: one round (a waiting date is re-asked too, review M5)
	}
	if st == StateHealing && e.nextAt.IsZero() {
		e.nextAt = e.d.Now()
	}
}
