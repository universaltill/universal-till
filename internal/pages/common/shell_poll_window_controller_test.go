package common

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ShellPollWindowController (ADR-0064, ut-docs#1039) is the attach-mode
// replacement for the NoopWindowController default: it drives the
// ShellChannel the desktop shell long-polls, and — unlike the no-op it
// replaces — reports honestly when nothing is holding the window.

type fakeFallbackController struct {
	exitCalls      int
	applyCalls     []string
	exitErr        error
	heartbeatCalls int
	heartbeatErr   error
}

func (f *fakeFallbackController) ExitToOS() error { f.exitCalls++; return f.exitErr }
func (f *fakeFallbackController) ApplyMode(mode string) error {
	f.applyCalls = append(f.applyCalls, mode)
	return nil
}
func (f *fakeFallbackController) InputHeartbeat() error {
	f.heartbeatCalls++
	return f.heartbeatErr
}

func TestShellPollController_ApplyModeReachesChannelAndNeverErrors(t *testing.T) {
	ch := NewShellChannel("normal")
	wc := NewShellPollWindowController(ch, nil)
	if err := wc.ApplyMode("kiosk"); err != nil {
		t.Fatalf("ApplyMode = %v, want nil (persisting with no shell attached is legitimate)", err)
	}
	if mode, rev := ch.Snapshot(); mode != "kiosk" || rev != 2 {
		t.Fatalf("channel after ApplyMode = (%q, %d), want (kiosk, 2)", mode, rev)
	}
}

func TestShellPollController_ApplyModeForwardsToFallbackWhenDetached(t *testing.T) {
	ch := NewShellChannel("normal")
	fb := &fakeFallbackController{}
	wc := NewShellPollWindowController(ch, fb)

	// Detached: the env-handed spawn-mode channel (an old shell that never
	// polls) still gets the live apply, best-effort.
	if err := wc.ApplyMode("fullscreen"); err != nil {
		t.Fatalf("ApplyMode = %v, want nil", err)
	}
	if len(fb.applyCalls) != 1 || fb.applyCalls[0] != "fullscreen" {
		t.Fatalf("fallback applyCalls = %v, want [fullscreen]", fb.applyCalls)
	}

	// Attached: the polled channel is the live path; the fallback must not
	// be double-driven.
	ch.NoteSeen("fullscreen")
	if err := wc.ApplyMode("normal"); err != nil {
		t.Fatalf("ApplyMode = %v, want nil", err)
	}
	if len(fb.applyCalls) != 1 {
		t.Fatalf("fallback applyCalls = %v, want unchanged [fullscreen] while a shell is attached", fb.applyCalls)
	}
}

// TestShellPollController_InputHeartbeatForwardsToFallback proves
// ut-docs#1329's InputHeartbeat relays through the spawn-mode fallback
// unconditionally — unlike ApplyMode, the polled ShellChannel has no
// mechanism at all for this (heartbeat is data reported TO the shell, and
// "all traffic shell → server, never the reverse" per this type's own doc
// comment), so the fallback is the only channel InputHeartbeat can ever
// use, attached or not.
func TestShellPollController_InputHeartbeatForwardsToFallback(t *testing.T) {
	ch := NewShellChannel("normal")
	fb := &fakeFallbackController{}
	wc := NewShellPollWindowController(ch, fb)

	if err := wc.InputHeartbeat(); err != nil {
		t.Fatalf("InputHeartbeat = %v, want nil (best-effort, never fails the caller)", err)
	}
	if fb.heartbeatCalls != 1 {
		t.Fatalf("fallback heartbeatCalls = %d, want 1", fb.heartbeatCalls)
	}

	ch.NoteSeen("normal") // attach a poller — heartbeat must still forward
	if err := wc.InputHeartbeat(); err != nil {
		t.Fatalf("InputHeartbeat = %v, want nil", err)
	}
	if fb.heartbeatCalls != 2 {
		t.Fatalf("fallback heartbeatCalls = %d, want 2 (heartbeat forwards regardless of attachment)", fb.heartbeatCalls)
	}
}

// TestShellPollController_InputHeartbeatNoFallbackIsSilentNoOp proves the
// no-fallback case never errors — a heartbeat with nowhere to relay to is
// completely ordinary (attach-mode, no env-handed spawn shell) and must
// never fail the caller's POST.
func TestShellPollController_InputHeartbeatNoFallbackIsSilentNoOp(t *testing.T) {
	ch := NewShellChannel("normal")
	wc := NewShellPollWindowController(ch, nil)
	if err := wc.InputHeartbeat(); err != nil {
		t.Fatalf("InputHeartbeat = %v, want nil", err)
	}
}

func TestShellPollController_ExitToOS_NoShellNoFallbackIsHonestError(t *testing.T) {
	ch := NewShellChannel("kiosk")
	wc := NewShellPollWindowController(ch, nil)
	err := wc.ExitToOS()
	if err == nil {
		t.Fatal("ExitToOS = nil with no shell attached and no fallback — the fabricated success this card removes")
	}
	if !errors.Is(err, ErrNoWindowControl) {
		t.Fatalf("ExitToOS error = %v, want errors.Is(ErrNoWindowControl)", err)
	}
}

func TestShellPollController_ExitToOS_NoShellDelegatesToFallback(t *testing.T) {
	ch := NewShellChannel("kiosk")
	fb := &fakeFallbackController{}
	wc := NewShellPollWindowController(ch, fb)
	if err := wc.ExitToOS(); err != nil {
		t.Fatalf("ExitToOS = %v, want nil (fallback succeeded)", err)
	}
	if fb.exitCalls != 1 {
		t.Fatalf("fallback exitCalls = %d, want 1", fb.exitCalls)
	}

	// A fallback failure propagates as-is (mapped to 500 by the handler,
	// not the 503 the sentinels get).
	fb.exitErr = errors.New("shell control channel unreachable")
	if err := wc.ExitToOS(); !errors.Is(err, fb.exitErr) {
		t.Fatalf("ExitToOS = %v, want the fallback's own error", err)
	}
}

func TestShellPollController_ExitToOS_AttachedAckedSucceeds(t *testing.T) {
	ch := NewShellChannel("kiosk")
	wc := NewShellPollWindowController(ch, nil)
	ch.NoteSeen("kiosk") // shell is polling

	// Simulate the attached shell: park on the channel like the real long
	// poll, apply what comes back, ack it.
	_, rev := ch.Snapshot()
	go func() {
		mode, _ := ch.Wait(context.Background(), rev, 5*time.Second)
		ch.NoteSeen(mode)
	}()

	if err := wc.ExitToOS(); err != nil {
		t.Fatalf("ExitToOS = %v, want nil (shell acked normal)", err)
	}
	if mode, _ := ch.Snapshot(); mode != "normal" {
		t.Fatalf("live mode after ExitToOS = %q, want normal", mode)
	}
}

func TestShellPollController_ExitToOS_AttachedButNoAckTimesOutHonestly(t *testing.T) {
	ch := NewShellChannel("kiosk")
	c := &ShellPollWindowController{ch: ch, ackTimeout: 50 * time.Millisecond}
	ch.NoteSeen("kiosk") // attached, but nothing will ever ack "normal"

	err := c.ExitToOS()
	if err == nil {
		t.Fatal("ExitToOS = nil with no ack, want ErrExitNotConfirmed")
	}
	if !errors.Is(err, ErrExitNotConfirmed) {
		t.Fatalf("ExitToOS error = %v, want errors.Is(ErrExitNotConfirmed)", err)
	}
}
