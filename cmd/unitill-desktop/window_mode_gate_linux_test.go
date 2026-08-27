//go:build desktop && linux

package main

import "testing"

// assertShellAppliesWindowModeForBuild is the desktop&&linux half of
// TestShellAppliesWindowModeGatesTheAdvertise's platform-gating assertion
// (shell_poll_test.go) — see window_mode_gate_other_test.go for the other
// half. Linux is the only platform with a real applyWindowMode today
// (window_mode_linux.go's init sets shellAppliesWindowMode true), so this
// is the one build that must claim the live-control capability.
func assertShellAppliesWindowModeForBuild(t *testing.T) {
	t.Helper()
	if !shellAppliesWindowMode {
		t.Fatal("shellAppliesWindowMode = false in a desktop&&linux build — window_mode_linux.go's init must set it true")
	}
}
