//go:build desktop && (linux || windows)

package main

import "testing"

// assertShellAppliesWindowModeForBuild is the desktop&&linux / desktop&&windows half of
// TestShellAppliesWindowModeGatesTheAdvertise's platform-gating assertion
// (shell_poll_test.go) — see window_mode_gate_other_test.go for the other
// half. Linux and Windows (ut-docs#610) have a real applyWindowMode — each
// platform's window_mode_*.go init sets shellAppliesWindowMode true — so
// these builds must claim the live-control capability.
func assertShellAppliesWindowModeForBuild(t *testing.T) {
	t.Helper()
	if !shellAppliesWindowMode {
		t.Fatal("shellAppliesWindowMode = false in a desktop&&linux/windows build — window_mode_<os>.go's init must set it true")
	}
}
