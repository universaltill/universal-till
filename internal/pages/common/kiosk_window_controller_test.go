package common

import (
	"errors"
	"testing"
)

// TestKioskSystemdWindowController_ApplyMode_Kiosk verifies that setting
// window-mode to "kiosk" enables, then starts, unitill-kiosk.service — in
// that order (ut-docs#883 acceptance criteria).
func TestKioskSystemdWindowController_ApplyMode_Kiosk(t *testing.T) {
	var calls []string
	c := KioskSystemdWindowController{run: func(verb string) error {
		calls = append(calls, verb)
		return nil
	}}
	if err := c.ApplyMode("kiosk"); err != nil {
		t.Fatalf("ApplyMode(kiosk) = %v, want nil", err)
	}
	want := []string{"enable", "start"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

// TestKioskSystemdWindowController_ApplyMode_NonKiosk verifies that any mode
// other than "kiosk" (fullscreen, maximized, normal) disables, then stops,
// the service — the replacement for the file-touch-only opt-out.
func TestKioskSystemdWindowController_ApplyMode_NonKiosk(t *testing.T) {
	for _, mode := range []string{"fullscreen", "maximized", "normal", "bogus"} {
		var calls []string
		c := KioskSystemdWindowController{run: func(verb string) error {
			calls = append(calls, verb)
			return nil
		}}
		if err := c.ApplyMode(mode); err != nil {
			t.Fatalf("ApplyMode(%s) = %v, want nil", mode, err)
		}
		want := []string{"disable", "stop"}
		if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
			t.Fatalf("mode=%s calls = %v, want %v", mode, calls, want)
		}
	}
}

// TestKioskSystemdWindowController_ApplyMode_SurfacesFailure covers the
// missing-sudoers-grant case (a pre-#883 Pi upgraded without re-running
// unitill-kiosk-setup.sh): the first failing verb aborts immediately with a
// clear error — not a panic, not a silently-swallowed failure — and any
// later verb is never attempted.
func TestKioskSystemdWindowController_ApplyMode_SurfacesFailure(t *testing.T) {
	wantErr := errors.New("sudo: a password is required")
	var calls []string
	c := KioskSystemdWindowController{run: func(verb string) error {
		calls = append(calls, verb)
		if verb == "enable" {
			return wantErr
		}
		return nil
	}}
	err := c.ApplyMode("kiosk")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ApplyMode(kiosk) err = %v, want it to wrap %v", err, wantErr)
	}
	if len(calls) != 1 || calls[0] != "enable" {
		t.Fatalf("calls = %v, want only [enable] — start must not run after enable failed", calls)
	}
}

// TestKioskSystemdWindowController_ExitToOS: the Pi headless kiosk has no OS
// desktop to exit to (cage fills the whole console) — this is a deliberate
// no-op, not an unimplemented gap.
func TestKioskSystemdWindowController_ExitToOS(t *testing.T) {
	c := KioskSystemdWindowController{}
	if err := c.ExitToOS(); err != nil {
		t.Fatalf("ExitToOS() = %v, want nil", err)
	}
}

// TestKioskSystemdWindowController_ImplementsWindowController is a compile-
// time-ish check that the real constructor also satisfies the interface
// (the fake-runner tests above construct the struct literal directly).
func TestKioskSystemdWindowController_ImplementsWindowController(t *testing.T) {
	var _ WindowController = NewKioskSystemdWindowController()
}

// TestSystemctlKioskArgs_NeverPrompts covers review finding F4: without
// `sudo -n`, a missing sudoers grant makes sudo attempt an interactive
// password prompt instead of failing immediately — usually fast ("no tty
// present") but not guaranteed if an askpass helper is ever configured on
// the box. `-n` makes "no grant" a clean, immediate failure every time,
// which is what ApplyMode's callers need to surface a clear error rather
// than hang.
func TestSystemctlKioskArgs_NeverPrompts(t *testing.T) {
	got := systemctlKioskArgs("enable")
	want := []string{"-n", "systemctl", "enable", "unitill-kiosk.service"}
	if len(got) != len(want) {
		t.Fatalf("systemctlKioskArgs(enable) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("systemctlKioskArgs(enable) = %v, want %v", got, want)
		}
	}
}
