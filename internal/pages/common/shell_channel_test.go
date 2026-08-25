package common

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ShellChannel (ADR-0064, ut-docs#1039) is the server-authoritative live
// window-mode state the desktop shell long-polls. These tests drive the
// broadcast/wait semantics directly; the HTTP surface on top of it is
// covered in internal/pages/window_state_api_test.go.

func TestShellChannel_SnapshotClampsInitialMode(t *testing.T) {
	c := NewShellChannel("bogus")
	mode, rev := c.Snapshot()
	if mode != "normal" {
		t.Fatalf("initial mode = %q, want normal (clamped)", mode)
	}
	if rev != 1 {
		t.Fatalf("initial rev = %d, want 1", rev)
	}
}

func TestShellChannel_WaitReturnsImmediatelyWhenSinceDiffers(t *testing.T) {
	c := NewShellChannel("kiosk")
	// since=0 is a first call — the client has seen nothing, so the current
	// state must come back at once, never after a hold.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	mode, rev := c.Wait(ctx, 0, 10*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Wait(since=0) held for %v, want immediate return", elapsed)
	}
	if mode != "kiosk" || rev != 1 {
		t.Fatalf("Wait(since=0) = (%q, %d), want (kiosk, 1)", mode, rev)
	}
}

func TestShellChannel_WaiterReleasedBySetMode(t *testing.T) {
	c := NewShellChannel("kiosk")
	done := make(chan struct{})
	var mode string
	var rev uint64
	go func() {
		mode, rev = c.Wait(context.Background(), 1, 10*time.Second)
		close(done)
	}()
	// Give the waiter a moment to actually park, then change the mode.
	time.Sleep(50 * time.Millisecond)
	c.SetMode("normal")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait not released by SetMode within 2s")
	}
	if mode != "normal" || rev != 2 {
		t.Fatalf("released Wait = (%q, %d), want (normal, 2)", mode, rev)
	}
}

func TestShellChannel_WaitTimeoutReturnsCurrentState(t *testing.T) {
	c := NewShellChannel("fullscreen")
	mode, rev := c.Wait(context.Background(), 1, 30*time.Millisecond)
	if mode != "fullscreen" || rev != 1 {
		t.Fatalf("timed-out Wait = (%q, %d), want (fullscreen, 1)", mode, rev)
	}
}

func TestShellChannel_WaitReleasedByContextCancel(t *testing.T) {
	c := NewShellChannel("kiosk")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Wait(ctx, 1, 10*time.Second)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait not released by context cancel within 2s")
	}
}

func TestShellChannel_UnchangedSetModeDoesNotBumpRev(t *testing.T) {
	c := NewShellChannel("kiosk")
	c.SetMode("kiosk")
	if _, rev := c.Snapshot(); rev != 1 {
		t.Fatalf("rev after unchanged SetMode = %d, want 1 (no bump)", rev)
	}
	// An invalid mode clamps to normal — a real change from kiosk.
	c.SetMode("nonsense")
	if mode, rev := c.Snapshot(); mode != "normal" || rev != 2 {
		t.Fatalf("after SetMode(nonsense): (%q, %d), want (normal, 2)", mode, rev)
	}
}

func TestShellChannel_AttachedTracksNoteSeenWithinWindow(t *testing.T) {
	c := NewShellChannel("kiosk")
	base := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	now := base
	c.now = func() time.Time { return now }

	if c.Attached(ShellAttachedWindow) {
		t.Fatal("Attached = true before any NoteSeen, want false")
	}
	c.NoteSeen("kiosk")
	if !c.Attached(ShellAttachedWindow) {
		t.Fatal("Attached = false right after NoteSeen, want true")
	}
	now = base.Add(ShellAttachedWindow - time.Second)
	if !c.Attached(ShellAttachedWindow) {
		t.Fatal("Attached = false just inside the window, want true")
	}
	now = base.Add(ShellAttachedWindow + time.Second)
	if c.Attached(ShellAttachedWindow) {
		t.Fatal("Attached = true just outside the window, want false")
	}
}

func TestShellChannel_WaitAppliedAckAndTimeout(t *testing.T) {
	c := NewShellChannel("kiosk")

	// Already-acked mode returns true immediately.
	c.NoteSeen("kiosk")
	if !c.WaitApplied(context.Background(), "kiosk", 10*time.Millisecond) {
		t.Fatal("WaitApplied(kiosk) = false with lastApplied already kiosk, want true")
	}

	// No ack for "normal" yet — times out false.
	if c.WaitApplied(context.Background(), "normal", 30*time.Millisecond) {
		t.Fatal("WaitApplied(normal) = true with no ack, want timeout false")
	}

	// A concurrent NoteSeen releases the waiter with true.
	done := make(chan bool, 1)
	go func() { done <- c.WaitApplied(context.Background(), "normal", 5*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	c.NoteSeen("normal")
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("WaitApplied(normal) = false after NoteSeen(normal), want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitApplied not released by NoteSeen within 2s")
	}
}

// TestShellChannel_ConcurrentWaitersAndWriters is a -race exerciser: several
// long-poll waiters and several writers hammering the channel must never
// race or deadlock.
func TestShellChannel_ConcurrentWaitersAndWriters(t *testing.T) {
	c := NewShellChannel("normal")
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var since uint64
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				_, since = c.Wait(ctx, since, 15*time.Millisecond)
				cancel()
			}
		}()
	}
	modes := []string{"kiosk", "normal", "fullscreen", "maximized"}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.SetMode(modes[(i+j)%len(modes)])
				c.NoteSeen(fmt.Sprintf("m%d", j))
				_ = c.Attached(ShellAttachedWindow)
				_, _ = c.Snapshot()
			}
		}(i)
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestShellChannel_ExitPathDefaultsFalseAndMarks (review of ut-docs#1039,
// blocker 2): a bare channel is NOT the exit path — only
// NewShellPollWindowController's construction marks it, which is what lets
// GET /api/window-mode key the fail-closed downgrade on "something really
// consumes this channel".
func TestShellChannel_ExitPathDefaultsFalseAndMarks(t *testing.T) {
	c := NewShellChannel("kiosk")
	if c.IsExitPath() {
		t.Fatal("IsExitPath = true on a bare channel, want false (nothing consumes it yet)")
	}
	NewShellPollWindowController(c, nil)
	if !c.IsExitPath() {
		t.Fatal("IsExitPath = false after NewShellPollWindowController, want true")
	}
}

// TestShellChannel_AdoptIfUntouched pins the finding-7 adoption semantics:
// one-shot per boot, closed by any explicit SetMode (no-op included),
// never an escalation into a chrome-hiding mode, and inert on an
// empty/invalid applied value (which leaves the one-shot window open for
// the first real report).
func TestShellChannel_AdoptIfUntouched(t *testing.T) {
	// The finding-7 scenario: server restarted with persisted kiosk, the
	// still-running shell reports the normal window the operator escaped to.
	c := NewShellChannel("kiosk")
	if c.AdoptIfUntouched("") {
		t.Fatal("adopted an empty applied value")
	}
	if c.AdoptIfUntouched("bogus") {
		t.Fatal("adopted an invalid mode")
	}
	if mode, rev := c.Snapshot(); mode != "kiosk" || rev != 1 {
		t.Fatalf("state after invalid adoption attempts = (%q, %d), want (kiosk, 1) — and the one-shot window must still be open", mode, rev)
	}
	if !c.AdoptIfUntouched("normal") {
		t.Fatal("first valid report not adopted")
	}
	if mode, rev := c.Snapshot(); mode != "normal" || rev != 2 {
		t.Fatalf("state after adoption = (%q, %d), want (normal, 2)", mode, rev)
	}
	// Strictly one-shot: a later report changes nothing.
	if c.AdoptIfUntouched("maximized") {
		t.Fatal("second adoption succeeded — must be one-shot per boot")
	}

	// Escalation refused: adopting may only keep/leave, never enter a
	// chrome-hiding mode (a hostile loopback caller must not be able to
	// talk the server into serving kiosk).
	c2 := NewShellChannel("normal")
	if c2.AdoptIfUntouched("kiosk") {
		t.Fatal("adoption escalated normal → kiosk")
	}
	if mode, _ := c2.Snapshot(); mode != "normal" {
		t.Fatalf("mode = %q after refused escalation, want normal", mode)
	}

	// Same-mode adoption is a clean no-op (no rev bump), still one-shot.
	c3 := NewShellChannel("kiosk")
	if !c3.AdoptIfUntouched("kiosk") {
		t.Fatal("same-mode adoption refused")
	}
	if _, rev := c3.Snapshot(); rev != 1 {
		t.Fatalf("rev = %d after same-mode adoption, want 1 (no spurious wakeup)", rev)
	}

	// Any explicit SetMode closes the window — even a no-op change: an
	// operator re-applying kiosk right after a server start means it.
	c4 := NewShellChannel("kiosk")
	c4.SetMode("kiosk")
	if c4.AdoptIfUntouched("normal") {
		t.Fatal("adoption succeeded after an explicit SetMode")
	}
	if mode, _ := c4.Snapshot(); mode != "kiosk" {
		t.Fatalf("mode = %q, want kiosk kept after explicit SetMode", mode)
	}
}

// TestShellChannel_NoteSeenDoesNotWakeParkedModeWaiters (review of
// ut-docs#1039, finding 11): the shell's heartbeat/acknowledgement rides
// on every poll, so broadcasting it on the same channel Wait parks on
// wakes every parked long poll for an ack it does not care about — O(N)
// spurious wakeups per heartbeat, and an amplifier for finding 6's
// unauthenticated traffic. Mode waiters and ack waiters get separate
// broadcast channels. White-box: a mode-change broadcast replaces
// c.changed; an ack must not.
func TestShellChannel_NoteSeenDoesNotWakeParkedModeWaiters(t *testing.T) {
	c := NewShellChannel("kiosk")
	c.mu.Lock()
	before := c.changed
	c.mu.Unlock()

	c.NoteSeen("normal") // a fresh ack — must broadcast to WaitApplied only

	c.mu.Lock()
	after := c.changed
	c.mu.Unlock()
	if before != after {
		t.Fatal("NoteSeen replaced the mode-change broadcast channel — every parked Wait was woken for an ack")
	}

	// And the ack side still works: WaitApplied sees it.
	if !c.WaitApplied(context.Background(), "normal", 10*time.Millisecond) {
		t.Fatal("WaitApplied(normal) = false after NoteSeen(normal)")
	}

	// The converse: SetMode must wake mode waiters (channel replaced).
	c.SetMode("normal")
	c.mu.Lock()
	final := c.changed
	c.mu.Unlock()
	if final == after {
		t.Fatal("SetMode did not broadcast on the mode-change channel")
	}
}
