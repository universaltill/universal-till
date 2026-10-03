package cloudsync

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// This file holds the pure delay-selection math behind Start's loop
// (ut-docs#2588): jittered ticks on success, full-jitter exponential
// backoff on failure, and an optional Retry-After floor on top of that
// backoff. Kept as pure functions of an injectable random source so tests
// can seed them deterministically instead of asserting on wall-clock timing.

// jitteredWait applies the design's per-normal-tick jitter (point 1): base
// x U(0.8, 1.2), independently redrawn on every call — spreads a fleet of
// tills that would otherwise all wake on the exact same 2-minute grid (a
// cloud outage, or every till in a shop powering on at open) across a
// window instead of hitting the cloud in the same second. r is expected to
// be a uniform draw in [0,1); a value outside that range is clamped rather
// than trusted, the same defensive stance as backoffWait below.
func jitteredWait(base time.Duration, r float64) time.Duration {
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	factor := 0.8 + 0.4*r
	return time.Duration(float64(base) * factor)
}

// backoffFloor is the minimum wait after a failed tick (design point 2): a
// full-jitter draw near zero must not let the loop hot-loop against a cloud
// that is still down. With the production 2-minute base the ceiling is at
// least 4 min, so the floor always applies; it is lowered to the ceiling
// only when the ceiling itself is smaller (tests' ms-scale intervals).
const backoffFloor = 5 * time.Second

// backoffCeiling returns the full-jitter backoff CEILING for the nth
// consecutive tick failure: min(maxCap, base*2^n). n is clamped to 1 at the
// low end (the design's own "n = consecutive failures (1 on first
// failure)") and to a shift no real caller could usefully exceed at the
// high end, purely so the base*2^n multiplication stays inside int64 and
// meaningful — min(..., maxCap) makes any n that large behave identically
// to the cap anyway, so the clamp changes no observable behaviour.
func backoffCeiling(base, maxCap time.Duration, n int) time.Duration {
	if n < 1 {
		n = 1
	}
	const maxShift = 62 // 2^62 is already astronomically past any real maxCap
	if n > maxShift {
		n = maxShift
	}
	if base <= 0 || maxCap <= 0 {
		return maxCap
	}
	mult := int64(1) << uint(n)
	// Guard the base*mult multiplication itself overflowing/wrapping int64
	// BEFORE doing it: comparing base against maxCap/mult (division, not
	// multiplication) can't overflow the way base*mult could.
	if base > maxCap/time.Duration(mult) {
		return maxCap
	}
	d := base * time.Duration(mult)
	if d > maxCap {
		d = maxCap
	}
	return d
}

// backoffCap is the design's "cap = max(10 min, tickInterval())" — always
// 10 minutes for the production 2-minute tick, but stays sane if a caller
// ever configured a tickInterval longer than that.
func backoffCap() time.Duration {
	c := 10 * time.Minute
	if ti := tickInterval(); ti > c {
		c = ti
	}
	return c
}

// backoffWait draws one full-jitter backoff delay (design point 2): U(0,
// ceiling), floored so a collapsed ceiling can never hot-loop. r is
// expected in [0,1); out-of-range values are clamped rather than trusted.
func backoffWait(base, maxCap time.Duration, n int, r float64) time.Duration {
	ceiling := backoffCeiling(base, maxCap, n)
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	d := time.Duration(float64(ceiling) * r)
	floor := backoffFloor
	if floor > ceiling {
		floor = ceiling // never widen a genuinely small ceiling past itself
	}
	if d < floor {
		d = floor
	}
	return d
}

// retryAfterClamp bounds how long a Retry-After header can park a till
// (design point 3): a buggy or hostile header must never mean this till
// stops syncing for an unbounded amount of time.
const retryAfterClamp = time.Hour

// retryAfterHint extracts a usable Retry-After wait from a Tick failure,
// but only for the two status codes that carry a real "come back later"
// contract — 429 Too Many Requests and 503 Service Unavailable (design
// point 3). Any other status's RetryAfter is ignored even if it happens to
// be set. Returns 0 when there is nothing to honour. errors.As, so the
// hint survives if a caller ever wraps post's error.
func retryAfterHint(err error) time.Duration {
	var se *statusError
	if !errors.As(err, &se) {
		return 0
	}
	if se.StatusCode != http.StatusTooManyRequests && se.StatusCode != http.StatusServiceUnavailable {
		return 0
	}
	ra := se.RetryAfter
	if ra <= 0 {
		return 0
	}
	if ra > retryAfterClamp {
		ra = retryAfterClamp
	}
	return ra
}

// scheduler owns Start's loop-level delay state (ut-docs#2588): how many
// consecutive ticks have failed, and the random source behind every jitter
// draw. One scheduler per Start() goroutine, never shared — Start's loop is
// already single-goroutine, so this needs no locking of its own.
type scheduler struct {
	// rng returns a uniform draw in [0,1): math/rand/v2's auto-seeded,
	// goroutine-safe top-level Float64 in production, a stub in tests.
	rng   func() float64
	fails int
	// auth is the 401 streak (ADR-0116 D6), kept apart from fails: fails
	// mixes every failure kind, the streak counts only 401s. Production
	// shares the package-level tillAuth so the web layer can read it; nil
	// (a test's bare scheduler) leaves the generic backoff alone.
	auth *authTracker
}

// newSchedulerFn is what Start calls; a var so a test can stub the random
// source and prove Start really draws its waits from the scheduler.
var newSchedulerFn = newScheduler

func newScheduler() *scheduler {
	return &scheduler{rng: rand.Float64, auth: tillAuth}
}

// firstWait is the jittered wait before Start's very first tick (design
// point 1: firstDelay() gets the same jitter treatment as every later
// normal tick, so even the post-boot wave of tills doesn't line up).
func (s *scheduler) firstWait() time.Duration {
	return jitteredWait(firstDelay(), s.rng())
}

// next chooses how long to wait before the NEXT tick, given how the tick
// that just ran went. nil means success: the failure streak resets and the
// wait is a normal jittered tick (design point 1). Non-nil means the
// failure streak grows by one and the wait is full-jitter backoff (design
// point 2), raised to a Retry-After hint when the failure carries one
// (design point 3) — never lowered by one, since the backoff is itself a
// floor the cloud's own hint can only extend.
//
// ADR-0116 D6 overrides all of that while the till is locked out (3
// consecutive 401s): every wait is a flat authRetryWait until a tick
// succeeds, whatever the failure in between (a 503 or a dead network
// neither ends the lock-out nor speeds it up).
func (s *scheduler) next(tickErr error) time.Duration {
	if s.auth != nil {
		s.auth.observe(tickErr)
	}
	if tickErr == nil {
		s.fails = 0
		return jitteredWait(tickInterval(), s.rng())
	}
	s.fails++
	d := backoffWait(tickInterval(), backoffCap(), s.fails, s.rng())
	if ra := retryAfterHint(tickErr); ra > d {
		d = ra
	}
	if s.auth != nil && s.auth.lockedOut() {
		return authRetryWait
	}
	return d
}

// ADR-0116 D6 (ut-docs#3524): after authLockoutAfter consecutive 401s on a
// till-auth endpoint (the check-in GET and the sync POST — the two calls
// whose error is the tick's) the till keeps selling offline, calls the
// cloud only every authRetryWait, and shows a status chip until a tick
// succeeds again (pairing gives it a working credential).
const (
	authLockoutAfter = 3
	authRetryWait    = time.Hour
)

// The 401 machine codes ADR-0116 D6 names. Any other code — or none, from
// an older cloud or a proxy's 401 — reads as AuthCodeUnauthorized.
const (
	AuthCodeDeviceRevoked = "device_revoked"
	AuthCodeTokenRetired  = "token_retired"
	AuthCodeUnauthorized  = "unauthorized"
)

// authTracker is the consecutive-401 streak. Start's goroutine writes it
// (through scheduler.next) while page handlers read it (AuthChipStatus), so
// it has its own lock. In memory like the check-in state: a restarted till
// starts at zero and re-learns its lock-out within three ticks.
type authTracker struct {
	mu    sync.Mutex
	fails int
	code  string // the latest 401's normalised code
}

// tillAuth is the tracker Start's scheduler feeds and AuthChipStatus reads.
var tillAuth = &authTracker{}

// observe folds one tick's outcome into the streak: a success resets it, a
// 401 extends it, anything else (503 auth_unavailable, 429, 5xx, transport)
// is neutral — it says nothing about the credential.
func (a *authTracker) observe(tickErr error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if tickErr == nil {
		a.fails, a.code = 0, ""
		return
	}
	var se *statusError
	if !errors.As(tickErr, &se) || se.StatusCode != http.StatusUnauthorized {
		return
	}
	a.fails++
	a.code = normaliseAuthCode(se.Code)
}

func (a *authTracker) lockedOut() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fails >= authLockoutAfter
}

func (a *authTracker) status() AuthChip {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fails < authLockoutAfter {
		return AuthChip{}
	}
	return AuthChip{Show: true, Code: a.code}
}

func normaliseAuthCode(code string) string {
	switch code {
	case AuthCodeDeviceRevoked, AuthCodeTokenRetired:
		return code
	default:
		return AuthCodeUnauthorized
	}
}

// AuthChip is what the web layer needs for ADR-0116 D6's status chip: Show
// once the till is locked out, and the latest 401's code (one of the
// AuthCode* constants). Whether the store is claimed or anonymous is the
// caller's to decide — it changes the chip's words, not the lock-out.
type AuthChip struct {
	Show bool
	Code string
}

// AuthChipStatus reports the cloud-credential chip's state for this till.
// A local read; it never touches the network.
func AuthChipStatus() AuthChip {
	return tillAuth.status()
}
