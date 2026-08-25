package pages

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// TestKioskServiceUnitInstalledAt covers the second half of ut-docs#883's Pi-
// kiosk detection (review finding F1): a box already kiosked BEFORE
// ut-docs#883 landed has unitill-kiosk.service on disk even though
// pos.env/the unit's own environment never gained UT_KIOSK=1 —
// unitill-kiosk-setup.sh's own is_pi_appliance gate deliberately never
// re-triggers automatically on an upgrade (postinstall.sh: "an UPGRADE must
// never convert an existing field Pi"). Without also probing for the unit
// file, pages.Init would keep picking NoopWindowController on every such
// box, and the Settings toggle would silently do nothing — exactly the
// "silent no-op" ut-docs#883's own acceptance criteria forbids.
func TestKioskServiceUnitInstalledAt(t *testing.T) {
	dir := t.TempDir()
	unit := filepath.Join(dir, "unitill-kiosk.service")

	if kioskServiceUnitInstalledAt(unit) {
		t.Fatal("kioskServiceUnitInstalledAt = true before the unit file exists, want false")
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !kioskServiceUnitInstalledAt(unit) {
		t.Fatal("kioskServiceUnitInstalledAt = false once the unit file exists, want true")
	}
}

// TestAttachModeWindowControllerIsReal is ut-docs#1039's named regression
// test: with no UT_DESKTOP_CONTROL_ADDR and no Pi kiosk unit — i.e.
// exactly the .deb attach-mode topology, where the systemd-run unitill-pos
// was never spawned by the shell — Deps.WindowCtl must be a WORKING
// controller, not NoopWindowController. Asserted on behaviour, not type:
// ApplyMode must actually reach the shell channel, and ExitToOS with
// nothing attached must be an honest ErrNoWindowControl — the exact
// opposite of the silent nil that made the Settings page claim "Exited to
// OS." while a fullscreen undecorated window stayed put.
func TestAttachModeWindowControllerIsReal(t *testing.T) {
	t.Setenv(common.EnvDesktopControlAddr, "") // attach mode: env never handed over
	t.Setenv("UT_KIOSK", "")

	shell := common.NewShellChannel("kiosk")
	wc := newWindowController(shell, false)

	if _, isNoop := wc.(common.NoopWindowController); isNoop {
		t.Fatal("attach mode wired NoopWindowController — the ut-docs#1039 trap (exit-to-os silently no-ops while kiosk engages)")
	}

	// ApplyMode reaches the channel the shell polls.
	if err := wc.ApplyMode("fullscreen"); err != nil {
		t.Fatalf("ApplyMode = %v, want nil", err)
	}
	if mode, _ := shell.Snapshot(); mode != "fullscreen" {
		t.Fatalf("shell channel mode after ApplyMode = %q, want fullscreen", mode)
	}

	// ExitToOS with no shell polling: an honest error, never a silent success.
	err := wc.ExitToOS()
	if err == nil {
		t.Fatal("ExitToOS = nil with no shell attached — fabricated success")
	}
	if !errors.Is(err, common.ErrNoWindowControl) {
		t.Fatalf("ExitToOS error = %v, want errors.Is(ErrNoWindowControl)", err)
	}
}

// TestSpawnModeWindowControllerFallsBackToEnvChannel: when the shell DID
// spawn this process (UT_DESKTOP_CONTROL_ADDR present) but is too old to
// poll, exit-to-os must still travel ut-docs#882's env-handed loopback
// channel (ADR-0064 Decision 5 keeps it as the spawn-mode fallback).
func TestSpawnModeWindowControllerFallsBackToEnvChannel(t *testing.T) {
	exitCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/exit-to-os" {
			exitCalls++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	t.Setenv(common.EnvDesktopControlAddr, srv.Listener.Addr().String())
	t.Setenv(common.EnvDesktopControlToken, "test-token")
	t.Setenv("UT_KIOSK", "")

	shell := common.NewShellChannel("kiosk")
	wc := newWindowController(shell, false)

	if err := wc.ExitToOS(); err != nil {
		t.Fatalf("ExitToOS = %v, want nil via the env-handed fallback", err)
	}
	if exitCalls != 1 {
		t.Fatalf("fallback control channel exit-to-os calls = %d, want 1", exitCalls)
	}
}
