package cloudsync

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#2588: jittered tick / backoff-on-failure / Retry-After / dedicated
// transport for connection reuse. See cloudsync.go's Start, post and
// statusError, and schedule.go's pure delay-selection functions.

// --- jitter bounds (design point 1) ---

func TestJitteredWaitBounds(t *testing.T) {
	base := 2 * time.Minute
	lo := time.Duration(float64(base) * 0.8)
	hi := time.Duration(float64(base) * 1.2)
	seen := map[time.Duration]bool{}
	for i := 0; i <= 1000; i++ {
		r := float64(i) / 1000
		d := jitteredWait(base, r)
		if d < lo || d > hi {
			t.Fatalf("r=%v -> %v, want in [%v,%v]", r, d, lo, hi)
		}
		seen[d] = true
	}
	if len(seen) < 100 {
		t.Fatalf("draws barely varied: only %d distinct values out of 1001 draws", len(seen))
	}
}

func TestJitteredWaitExtremes(t *testing.T) {
	base := 2 * time.Minute
	lo := time.Duration(float64(base) * 0.8)
	hi := time.Duration(float64(base) * 1.2)
	if d := jitteredWait(base, 0); d != lo {
		t.Fatalf("r=0: got %v, want exactly %v", d, lo)
	}
	got := jitteredWait(base, 0.999999)
	diff := got - hi
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Millisecond {
		t.Fatalf("r->1: got %v, want ~%v", got, hi)
	}
}

// --- backoff sequence (design point 2) ---

func TestBackoffCeilingDoublesAndCaps(t *testing.T) {
	base := 2 * time.Minute
	capD := 10 * time.Minute
	c1 := backoffCeiling(base, capD, 1)
	c2 := backoffCeiling(base, capD, 2)
	c3 := backoffCeiling(base, capD, 3)
	if c1 != 4*time.Minute {
		t.Fatalf("ceiling(1) = %v, want 4m", c1)
	}
	if c2 != 8*time.Minute {
		t.Fatalf("ceiling(2) = %v, want 8m", c2)
	}
	if c3 != capD {
		t.Fatalf("ceiling(3) = %v, want capped at %v (base*2^3=16m > cap)", c3, capD)
	}
}

func TestBackoffCeilingOverflowGuard(t *testing.T) {
	base := 2 * time.Minute
	capD := 10 * time.Minute
	if c := backoffCeiling(base, capD, 100); c != capD {
		t.Fatalf("ceiling(100) = %v, want capped at %v (overflow guard)", c, capD)
	}
}

func TestBackoffWaitFloor(t *testing.T) {
	base := 2 * time.Minute
	capD := 10 * time.Minute
	if d := backoffWait(base, capD, 1, 0); d != 5*time.Second {
		t.Fatalf("r=0 floor: got %v, want 5s", d)
	}
}

func TestBackoffWaitNearCeiling(t *testing.T) {
	base := 2 * time.Minute
	capD := 10 * time.Minute
	ceiling := backoffCeiling(base, capD, 1)
	d := backoffWait(base, capD, 1, 0.999999)
	if d > ceiling {
		t.Fatalf("delay %v exceeds ceiling %v", d, ceiling)
	}
	if d < ceiling-10*time.Millisecond {
		t.Fatalf("delay %v not close to ceiling %v", d, ceiling)
	}
}

func TestSchedulerBackoffSequenceAndReset(t *testing.T) {
	origTick := tickIntervalNS.Load()
	t.Cleanup(func() { tickIntervalNS.Store(origTick) })
	tickIntervalNS.Store(int64(2 * time.Minute))

	s := newScheduler()
	s.rng = func() float64 { return 0.999999 } // pin every draw near its ceiling

	c1 := backoffCeiling(2*time.Minute, backoffCap(), 1)
	c2 := backoffCeiling(2*time.Minute, backoffCap(), 2)
	c3 := backoffCeiling(2*time.Minute, backoffCap(), 3)

	d1 := s.next(errors.New("boom"))
	d2 := s.next(errors.New("boom"))
	d3 := s.next(errors.New("boom"))

	if d1 > c1 || d1 < c1-10*time.Millisecond {
		t.Fatalf("1st failure: %v, want ~%v", d1, c1)
	}
	if d2 > c2 || d2 < c2-10*time.Millisecond {
		t.Fatalf("2nd failure: %v, want ~%v", d2, c2)
	}
	if c3 != 10*time.Minute {
		t.Fatalf("3rd failure ceiling: %v, want capped at 10m", c3)
	}
	if d3 > c3 || d3 < c3-10*time.Millisecond {
		t.Fatalf("3rd failure: %v, want ~%v (capped ceiling)", d3, c3)
	}

	// A successful tick resets the failure streak back to a normal
	// jittered wait, not a continued/decayed backoff.
	dSuccess := s.next(nil)
	lo := time.Duration(float64(2*time.Minute) * 0.8)
	hi := time.Duration(float64(2*time.Minute) * 1.2)
	if dSuccess < lo || dSuccess > hi {
		t.Fatalf("post-success wait %v not a normal jittered tick in [%v,%v]", dSuccess, lo, hi)
	}
	d4 := s.next(errors.New("boom again"))
	if d4 > c1+10*time.Millisecond || d4 < c1-10*time.Millisecond {
		t.Fatalf("failure right after a reset: %v, want back to the 1st-failure ceiling ~%v", d4, c1)
	}
}

func TestSchedulerBackoffOverflowStaysCapped(t *testing.T) {
	origTick := tickIntervalNS.Load()
	t.Cleanup(func() { tickIntervalNS.Store(origTick) })
	tickIntervalNS.Store(int64(2 * time.Minute))

	s := newScheduler()
	s.rng = func() float64 { return 0.999999 }
	var d time.Duration
	for i := 0; i < 100; i++ {
		d = s.next(errors.New("boom"))
	}
	if d > 10*time.Minute || d < 10*time.Minute-10*time.Millisecond {
		t.Fatalf("after 100 consecutive failures: %v, want ~10m (capped)", d)
	}
}

// --- Retry-After (design point 3) ---

func TestParseRetryAfterDeltaSeconds(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := parseRetryAfter("120", now); d != 120*time.Second {
		t.Fatalf("delta-seconds: got %v, want 120s", d)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(90 * time.Second)
	h := future.Format(http.TimeFormat)
	d := parseRetryAfter(h, now)
	if d < 89*time.Second || d > 91*time.Second {
		t.Fatalf("HTTP-date: got %v, want ~90s", d)
	}
}

func TestParseRetryAfterGarbageNegativePastZero(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []string{"", "not-a-number", "-5", "0", now.Add(-time.Hour).Format(http.TimeFormat)}
	for _, c := range cases {
		if d := parseRetryAfter(c, now); d != 0 {
			t.Fatalf("parseRetryAfter(%q) = %v, want 0", c, d)
		}
	}
}

func TestParseRetryAfterHugeValueSaturatesAtClamp(t *testing.T) {
	// A delta-seconds too big for time.Duration used to wrap (10000000000s
	// came out negative, 18446744074s as ~290ms) and silently bypass the 1h
	// clamp (review of ut-docs#2588). Anything at or past the clamp now
	// reads as the clamp itself, including an all-digit value Atoi rejects.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, h := range []string{"3600", "99999999", "10000000000", "18446744074", "99999999999999999999"} {
		if d := parseRetryAfter(h, now); d != retryAfterClamp {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", h, d, retryAfterClamp)
		}
	}
	if d := parseRetryAfter("120", now); d != 120*time.Second {
		t.Errorf("parseRetryAfter(120) = %v, want 2m0s", d)
	}
}

func TestPostCapturesRetryAfterOn503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
	var se *statusError
	if !errors.As(err, &se) {
		t.Fatalf("want *statusError, got %v (%T)", err, err)
	}
	if se.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode = %d, want 503", se.StatusCode)
	}
	if se.RetryAfter != 120*time.Second {
		t.Fatalf("RetryAfter = %v, want 120s", se.RetryAfter)
	}
	wantMsg := fmt.Sprintf("cloudsync: %s returned %d", "/v1/stores/sync", http.StatusServiceUnavailable)
	if se.Error() != wantMsg {
		t.Fatalf("Error() text changed: %q, want %q (existing callers rely on this text)", se.Error(), wantMsg)
	}
}

func TestSchedulerHonoursRetryAfterOn429And503Only(t *testing.T) {
	origTick := tickIntervalNS.Load()
	t.Cleanup(func() { tickIntervalNS.Store(origTick) })
	tickIntervalNS.Store(int64(2 * time.Minute))

	s := newScheduler()
	s.rng = func() float64 { return 0 } // backoff alone would be just the floor (5s)
	err429 := &statusError{Path: "/x", StatusCode: http.StatusTooManyRequests, RetryAfter: 90 * time.Second}
	if d := s.next(err429); d != 90*time.Second {
		t.Fatalf("429 with Retry-After: got %v, want max(backoff, retryAfter)=90s", d)
	}

	s2 := newScheduler()
	s2.rng = func() float64 { return 0 }
	err500 := &statusError{Path: "/x", StatusCode: http.StatusInternalServerError, RetryAfter: 90 * time.Second}
	if d := s2.next(err500); d != 5*time.Second {
		t.Fatalf("500 must ignore RetryAfter even if set: got %v, want backoff floor 5s", d)
	}

	s3 := newScheduler()
	s3.rng = func() float64 { return 0 }
	hugeRA := &statusError{Path: "/x", StatusCode: http.StatusServiceUnavailable, RetryAfter: 5 * time.Hour}
	if d := s3.next(hugeRA); d != time.Hour {
		t.Fatalf("huge Retry-After: got %v, want clamped to 1h", d)
	}

	s4 := newScheduler()
	s4.rng = func() float64 { return 0 }
	wrapped := fmt.Errorf("tick: %w", err429)
	if d := s4.next(wrapped); d != 90*time.Second {
		t.Fatalf("wrapped statusError: got %v, want 90s (errors.As must see through the wrap)", d)
	}
}

// --- connection reuse (design point 5) ---

func TestConnectionReuseAcrossTicks(t *testing.T) {
	cloud := &fakeCloud{}
	srv := httptest.NewUnstartedServer(cloud.handler())
	var newConns int32
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&newConns, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	db := testDB(t)
	cfg := testCfg(srv.URL)

	if err := Tick(context.Background(), cfg, db, Hooks{}); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if err := Tick(context.Background(), cfg, db, Hooks{}); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if n := atomic.LoadInt32(&newConns); n != 1 {
		t.Fatalf("new connections across 2 ticks = %d, want 1 (the idle connection from tick 1 should be reused)", n)
	}
}

// Pins the actual regression rather than just the symptom: against
// http.DefaultTransport (90s IdleConnTimeout) the reuse test above would
// ALSO pass, because two sequential httptest round trips complete in
// microseconds, nowhere near 90s. Assert the package transport's own knob
// directly so a revert back to the default transport fails HERE.
func TestTransportIdleTimeoutOutlivesProductionTick(t *testing.T) {
	tr, ok := httpClient.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("httpClient.Transport = %#v, want a *http.Transport", httpClient.Transport)
	}
	maxJitteredTick := time.Duration(float64(2*time.Minute) * 1.2) // 144s
	if tr.IdleConnTimeout <= maxJitteredTick {
		t.Fatalf("IdleConnTimeout = %v, want more than the max jittered production tick (%v)", tr.IdleConnTimeout, maxJitteredTick)
	}
}

// Start must actually take its waits from the scheduler (review of
// ut-docs#2588): the pure delay math above is worthless if the loop still
// sleeps on the raw firstDelay()/tickInterval(). The stub's rng is only
// ever reached through scheduler.firstWait/next, so a Start that bypasses
// the scheduler leaves the counter at zero.
func TestStartDrawsEveryWaitFromScheduler(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	origNew := newSchedulerFn
	t.Cleanup(func() { firstDelayNS.Store(origFirst); tickIntervalNS.Store(origTick); newSchedulerFn = origNew })
	firstDelayNS.Store(int64(2 * time.Millisecond))
	tickIntervalNS.Store(int64(2 * time.Millisecond))
	var draws atomic.Int64
	newSchedulerFn = func() *scheduler {
		return &scheduler{rng: func() float64 { draws.Add(1); return 0.5 }}
	}

	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	count := func() int { cloud.mu.Lock(); defer cloud.mu.Unlock(); return len(cloud.syncBodies) }

	wg := startJoined(t, ctx, cancel, testCfg(srv.URL), db, Hooks{})
	deadline := time.Now().Add(2 * time.Second)
	for count() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d ticks after 2s, want at least 2", count())
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	waitJoined(t, wg)
	// firstWait + one next() per completed tick.
	if got, ticks := draws.Load(), int64(count()); got < ticks {
		t.Fatalf("scheduler drew %d waits for %d ticks, want at least one per tick (Start bypassing the scheduler?)", got, ticks)
	}
}
