//go:build !(desktop && linux)

package main

import "testing"

// assertShellAppliesWindowModeForBuild is the untagged/non-linux half of
// TestShellAppliesWindowModeGatesTheAdvertise's platform-gating assertion
// (shell_poll_test.go) — see window_mode_gate_linux_test.go for the other
// half. Every build except desktop&&linux (plain `go test ./...`, and
// desktop&&windows's applyWindowMode stub in window_mode_windows.go) must
// never claim the live-control capability.
func assertShellAppliesWindowModeForBuild(t *testing.T) {
	t.Helper()
	if shellAppliesWindowMode {
		t.Fatal("shellAppliesWindowMode = true in a non-desktop-linux build — only desktop&&linux may set it")
	}
}
