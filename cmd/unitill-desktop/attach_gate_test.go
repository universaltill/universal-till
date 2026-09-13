package main

import (
	"testing"
	"time"
)

// fakeClock is a deterministic, zero-wall-time stand-in for time.Now/
// time.Sleep — sleep advances it directly rather than actually blocking, so
// these tests cost no real wall-clock time regardless of the deadlines and
// intervals under test.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }
func (c *fakeClock) SleepCounting(n *int) func(time.Duration) {
	return func(d time.Duration) {
		*n++
		c.now = c.now.Add(d)
	}
}

// TestWaitForAttachSucceedsOnFirstProbeNeverSleeps covers the common case
// (warm launch, no cold-boot race, or a platform/deadline with no gate at
// all): the probe answers healthy immediately, so waitForAttach must return
// true without ever calling sleep — a regression here would silently add a
// new delay to every ordinary launch, not just the cold-boot one this exists
// to fix.
func TestWaitForAttachSucceedsOnFirstProbeNeverSleeps(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	slept := 0
	got := waitForAttach(clk.now.Add(time.Minute), func() bool { return true }, clk.SleepCounting(&slept), clk.Now)
	if !got {
		t.Fatal("waitForAttach() = false, want true (probe succeeded)")
	}
	if slept != 0 {
		t.Fatalf("slept %d times, want 0 — a succeeding probe must never sleep", slept)
	}
}

// TestWaitForAttachPastDeadlineDecidesFromOneProbe is the exact "single
// probe, no retry" behaviour this replaces: a deadline that has already
// passed (covers a disabled gate, a platform with none, or a warm/manual
// launch already past the boot gate) must not retry even once — false
// straight from the first failed probe.
func TestWaitForAttachPastDeadlineDecidesFromOneProbe(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	calls, slept := 0, 0
	got := waitForAttach(clk.now, func() bool { calls++; return false }, clk.SleepCounting(&slept), clk.Now)
	if got {
		t.Fatal("waitForAttach() = true, want false (probe never succeeded)")
	}
	if calls != 1 {
		t.Fatalf("probe called %d times, want exactly 1", calls)
	}
	if slept != 0 {
		t.Fatalf("slept %d times, want 0 — a deadline already passed must decide immediately", slept)
	}
}

// TestWaitForAttachRetriesUntilProbeSucceeds is ut-docs#1199's actual fix:
// the systemd service binds :8080 partway through the gate window, after a
// few failed probes — waitForAttach must keep retrying (not give up on the
// first miss) and return true the moment the probe turns healthy, never
// falling through to spawn.
func TestWaitForAttachRetriesUntilProbeSucceeds(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	calls, slept := 0, 0
	probe := func() bool {
		calls++
		return calls >= 4 // healthy on the 4th probe
	}
	got := waitForAttach(clk.now.Add(time.Hour), probe, clk.SleepCounting(&slept), clk.Now)
	if !got {
		t.Fatal("waitForAttach() = false, want true (probe eventually succeeded)")
	}
	if calls != 4 {
		t.Fatalf("probe called %d times, want exactly 4 (stop retrying once it succeeds)", calls)
	}
	if slept != 3 {
		t.Fatalf("slept %d times, want 3 (one sleep between each of the 3 failed probes and the next)", slept)
	}
}

// TestAttachDecisionLine covers waitForAttach's stderr diagnostic
// (ut-docs#1279, review follow-up to ut-docs#1199): the decision itself was
// previously invisible in the log, so a field engineer reading journal
// output on an affected till had no way to tell which branch the shell took
// or how long it spent deciding without a hardware trip. Split out as a
// pure string-builder (same "test the pure logic, leave the Fprintf
// untested" split as startup_gate.go's holdFor vs. waitForSafeStartup) so
// the message content is directly testable without capturing os.Stderr.
func TestAttachDecisionLine(t *testing.T) {
	for _, tc := range []struct {
		name        string
		retryWindow bool
		attempts    int
		elapsed     time.Duration
		attached    bool
		want        string
	}{
		{
			name:        "no retry window, attached on the single probe",
			retryWindow: false, attempts: 1, elapsed: 0, attached: true,
			want: "attach gate: no retry window (decided immediately), attached to an existing server after 1 probe over 0s (ut-docs#1199)\n",
		},
		{
			name:        "no retry window, gives up on the single probe",
			retryWindow: false, attempts: 1, elapsed: 0, attached: false,
			want: "attach gate: no retry window (decided immediately), no existing server — spawning our own after 1 probe over 0s (ut-docs#1199)\n",
		},
		{
			name:        "retry window open, attached on the very first probe",
			retryWindow: true, attempts: 1, elapsed: 0, attached: true,
			want: "attach gate: retry window open, attached to an existing server after 1 probe over 0s (ut-docs#1199)\n",
		},
		{
			name:        "retry window open, attached after retrying",
			retryWindow: true, attempts: 4, elapsed: 1500 * time.Millisecond, attached: true,
			want: "attach gate: retry window open, attached to an existing server after 4 probes over 1.5s (ut-docs#1199)\n",
		},
		{
			name:        "retry window open, gives up once the window closes",
			retryWindow: true, attempts: 3, elapsed: 1 * time.Second, attached: false,
			want: "attach gate: retry window open, no existing server — spawning our own after 3 probes over 1s (ut-docs#1199)\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := attachDecisionLine(tc.retryWindow, tc.attempts, tc.elapsed, tc.attached)
			if got != tc.want {
				t.Errorf("attachDecisionLine(%v, %d, %v, %v) = %q, want %q",
					tc.retryWindow, tc.attempts, tc.elapsed, tc.attached, got, tc.want)
			}
		})
	}
}

// TestAttachRetryDurationFloorsWhenGateDisabled is ut-docs#1278's actual
// fix: attachDeadline() (attach_gate_linux.go) used to collapse straight to
// "now" (a single immediate probe, no retry) whenever the render-defect
// gate was disabled (UT_SHELL_MIN_UPTIME_SECONDS=0), silently reopening the
// ut-docs#1199 boot race on any machine disabling the gate for an unrelated
// reason (X11, a non-Pi Linux desktop, no WebKitGTK 2.52.6 compositing
// defect). The attach-race retry must still get its own floor in that case.
func TestAttachRetryDurationFloorsWhenGateDisabled(t *testing.T) {
	got := attachRetryDuration(0, 0)
	if got != attachRetryFloor {
		t.Fatalf("attachRetryDuration(0, 0) = %v, want attachRetryFloor (%v) — a disabled render gate must not collapse the attach-race retry to a single immediate probe",
			got, attachRetryFloor)
	}
}

// TestAttachRetryDurationUnchangedWhenGateActive covers acceptance criterion
// 3 of ut-docs#1278: when the render gate is NOT disabled, attachRetryDuration
// must still reduce to plain holdFor(up, min) — including the
// near-end-of-window case where that is smaller than attachRetryFloor. The
// two windows may legitimately coincide (ut-docs#1199's "free on the attach
// path" reasoning), but this fix must not widen the active-gate window by
// flooring it too — only the disabled-gate case (min == 0) gets the floor.
func TestAttachRetryDurationUnchangedWhenGateActive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		up, min time.Duration
		want    time.Duration
	}{
		{"far from min, most of the window remains", 5 * time.Second, 60 * time.Second, 55 * time.Second},
		{"near end of window, below the floor — must NOT be raised to the floor", 59 * time.Second, 60 * time.Second, 1 * time.Second},
		{"exactly at min, no window left", 60 * time.Second, 60 * time.Second, 0},
		{"already past min, no window left", 65 * time.Second, 60 * time.Second, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := attachRetryDuration(tc.up, tc.min)
			if got != tc.want {
				t.Errorf("attachRetryDuration(%v, %v) = %v, want %v", tc.up, tc.min, got, tc.want)
			}
		})
	}
}

// TestWaitForAttachGivesUpAtDeadlineNeverAttaches covers the genuine
// no-service case (dev launch, tarball install, or a .deb whose service is
// simply down): the probe never succeeds, so waitForAttach must give up
// once the fake clock (advanced only by sleep, never real time) reaches the
// deadline, and the caller falls back to spawning — exactly as before this
// fix, just later than the very first probe.
func TestWaitForAttachGivesUpAtDeadlineNeverAttaches(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	deadline := clk.now.Add(2 * attachPollInterval)
	calls, slept := 0, 0
	got := waitForAttach(deadline, func() bool { calls++; return false }, clk.SleepCounting(&slept), clk.Now)
	if got {
		t.Fatal("waitForAttach() = true, want false (probe never succeeded)")
	}
	// 2*attachPollInterval of budget: probe+sleep at now (fail, T0<deadline,
	// sleep to T0+1i), probe+sleep at T0+1i (fail, still <deadline, sleep to
	// T0+2i), probe at T0+2i (fail, now == deadline — not before it — give
	// up without a 3rd sleep).
	if calls != 3 {
		t.Fatalf("probe called %d times, want exactly 3", calls)
	}
	if slept != 2 {
		t.Fatalf("slept %d times, want exactly 2", slept)
	}
}
