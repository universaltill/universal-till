//go:build !(desktop && (linux || windows))

package main

import "testing"

// assertShellAppliesWindowModeForBuild is the untagged/non-linux half of
// TestShellAppliesWindowModeGatesTheAdvertise's platform-gating assertion
// (shell_poll_test.go) — see window_mode_gate_linux_test.go for the other
// half. Every build except desktop&&linux and desktop&&windows (plain
// `go test ./...`, and desktop&&darwin, whose applyWindowMode is still
// ut-docs#609's) must never claim the live-control capability.
func assertShellAppliesWindowModeForBuild(t *testing.T) {
	t.Helper()
	if shellAppliesWindowMode {
		t.Fatal("shellAppliesWindowMode = true in a build without a real applyWindowMode — only desktop&&linux and desktop&&windows may set it")
	}
}
