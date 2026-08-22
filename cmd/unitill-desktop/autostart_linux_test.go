//go:build desktop && linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reconcileAutostart is the thin OS-resolution wrapper around the pure,
// fixture-tested autostartEntryContents (see autostart_test.go) — this
// verifies the WIRING (honors $XDG_CONFIG_HOME, creates the directory,
// writes/removes the right file) rather than re-deriving the content
// format, which autostart_test.go already pins independently. NOT run by
// CI (go test ./... never sets -tags desktop, see stub.go) — same
// structural gap every other file in this package already has; verified
// manually before merge.
func TestReconcileAutostart_WritesAndRemovesXDGEntry(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	wantPath := filepath.Join(cfgDir, "autostart", "unitill.desktop")

	if err := reconcileAutostart(true); err != nil {
		t.Fatalf("reconcileAutostart(true): %v", err)
	}
	contents, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("autostart entry not written at %s: %v", wantPath, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// This process's OWN executable — the test binary here, unitill-desktop
	// in production, by construction (reconcileAutostart lives inside
	// unitill-desktop's own package and calls os.Executable() on itself;
	// there is no other process to accidentally name, unlike the removed
	// server-side version this replaced).
	if !strings.Contains(string(contents), "Exec="+exe) {
		t.Errorf("entry does not name this process's own executable (%s):\n%s", exe, contents)
	}

	if err := reconcileAutostart(false); err != nil {
		t.Fatalf("reconcileAutostart(false): %v", err)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("autostart entry still present after disable: err=%v", err)
	}
}

func TestReconcileAutostart_DisableWhenAbsentIsNotAnError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := reconcileAutostart(false); err != nil {
		t.Fatalf("reconcileAutostart(false) on a fresh dir: %v", err)
	}
}

func TestReconcileAutostart_EnableTwiceIsIdempotent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := reconcileAutostart(true); err != nil {
		t.Fatalf("first enable: %v", err)
	}
	if err := reconcileAutostart(true); err != nil {
		t.Fatalf("second enable: %v", err)
	}
}
