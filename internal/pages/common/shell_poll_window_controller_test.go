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
	exitCalls           int
	applyCalls          []string
	exitErr             error
	inputHeartbeatCalls int
	inputHeartbeatErr   error
}

func (f *fakeFallbackController) ExitToOS() error { f.exitCalls++; return f.exitErr }
func (f *fakeFallbackController) ApplyMode(mode string) error {
	f.applyCalls = append(f.applyCalls, mode)
	return nil
}
func (f *fakeFallbackController) RecordInputHeartbeat() error {
	f.inputHeartbeatCalls++
	return f.inputHeartbeatErr
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

// TestShellPollController_RecordInputHeartbeat_ForwardsToFallback proves
// the heartbeat is unconditionally forwarded to a spawn-mode fallback
// (ut-docs#1329) — unlike ApplyMode, this never checks Attached(), since
// there is no ShellChannel field to publish a heartbeat into for an
// attach-mode shell to consume on its own next poll.
func TestShellPollController_RecordInputHeartbeat_ForwardsToFallback(t *testing.T) {
	ch := NewShellChannel("normal")
	fb := &fakeFallbackController{}
	wc := NewShellPollWindowController(ch, fb)

	if err := wc.RecordInputHeartbeat(); err != nil {
		t.Fatalf("RecordInputHeartbeat() = %v, want nil", err)
	}
	if fb.inputHeartbeatCalls != 1 {
		t.Fatalf("fallback inputHeartbeatCalls = %d, want 1", fb.inputHeartbeatCalls)
	}

	// Even while a shell is attached (polling), the fallback still gets it
	// — there is no other channel this signal can travel over.
	ch.NoteSeen("normal")
	if err := wc.RecordInputHeartbeat(); err != nil {
		t.Fatalf("RecordInputHeartbeat() = %v, want nil", err)
	}
	if fb.inputHeartbeatCalls != 2 {
		t.Fatalf("fallback inputHeartbeatCalls = %d, want 2 (attached shells still forward)", fb.inputHeartbeatCalls)
	}
}

// TestShellPollController_RecordInputHeartbeat_NoFallbackIsQuietNoop
// covers the common .deb-install attach-mode topology (nil fallback):
// telemetry that cannot be delivered must never surface as an error to a
// caller firing this many times a minute.
func TestShellPollController_RecordInputHeartbeat_NoFallbackIsQuietNoop(t *testing.T) {
	ch := NewShellChannel("normal")
	wc := NewShellPollWindowController(ch, nil)
	if err := wc.RecordInputHeartbeat(); err != nil {
		t.Fatalf("RecordInputHeartbeat() = %v, want nil with no fallback wired", err)
	}
}

// TestShellPollController_RecordInputHeartbeat_FallbackErrorIsSwallowed
// proves a genuine fallback failure (e.g. the shell process exited) never
// propagates — this is best-effort telemetry, not a correctness-affecting
// action.
func TestShellPollController_RecordInputHeartbeat_FallbackErrorIsSwallowed(t *testing.T) {
	ch := NewShellChannel("normal")
	fb := &fakeFallbackController{inputHeartbeatErr: errors.New("shell control channel unreachable")}
	wc := NewShellPollWindowController(ch, fb)
	if err := wc.RecordInputHeartbeat(); err != nil {
		t.Fatalf("RecordInputHeartbeat() = %v, want nil even when the fallback fails", err)
	}
	if fb.inputHeartbeatCalls != 1 {
		t.Fatalf("fallback inputHeartbeatCalls = %d, want 1 (the attempt still happened)", fb.inputHeartbeatCalls)
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
