package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// TestEnvVarsMatchCommonPackage guards the cross-binary contract that has
// no compiler to enforce it: cmd/unitill-desktop's env var literals and
// internal/pages/common's must be byte-identical, or the whole feature
// dies silently (this shell sets one name, HTTPWindowController reads
// another, neither side ever errors — see ut-docs#882 review m4).
func TestEnvVarsMatchCommonPackage(t *testing.T) {
	if envDesktopControlAddr != common.EnvDesktopControlAddr {
		t.Errorf("envDesktopControlAddr = %q, common.EnvDesktopControlAddr = %q — must match",
			envDesktopControlAddr, common.EnvDesktopControlAddr)
	}
	if envDesktopControlToken != common.EnvDesktopControlToken {
		t.Errorf("envDesktopControlToken = %q, common.EnvDesktopControlToken = %q — must match",
			envDesktopControlToken, common.EnvDesktopControlToken)
	}
}

// authedPost is what the real HTTPWindowController client always sends:
// the bearer token, no Origin header.
func authedPost(t *testing.T, addr, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(controlTokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// TestControlServer_NoOpsReturns503 covers the "shell listening but no
// window yet / platform doesn't wire a live channel" case (ut-docs#882) —
// the client-visible half of "falls back safely", proven end-to-end (a real
// HTTP round trip on the loopback listener, not just a function call).
func TestControlServer_NoOpsReturns503(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	for _, path := range []string{"/exit-to-os", "/apply-mode"} {
		resp := authedPost(t, cs.Addr(), path, cs.Token(), "mode=normal")
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("POST %s with no ops set = %d, want 503", path, resp.StatusCode)
		}
	}
}

// TestControlServer_ExitToOSRoutesToOps proves a real HTTP request reaches
// the wired native callback.
func TestControlServer_ExitToOSRoutesToOps(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	called := false
	cs.SetOps(&windowOps{
		ExitToOS:  func() error { called = true; return nil },
		ApplyMode: func(string) error { return nil },
	})

	resp := authedPost(t, cs.Addr(), "/exit-to-os", cs.Token(), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if !called {
		t.Error("ExitToOS was not called")
	}
}

// TestControlServer_ApplyModeForwardsModeValue proves the mode form value
// reaches the native callback unchanged — this is what makes a live
// Settings toggle (not just exit-to-os) work over the channel.
func TestControlServer_ApplyModeForwardsModeValue(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	var gotMode string
	cs.SetOps(&windowOps{
		ExitToOS:  func() error { return nil },
		ApplyMode: func(mode string) error { gotMode = mode; return nil },
	})

	resp := authedPost(t, cs.Addr(), "/apply-mode", cs.Token(), "mode=kiosk")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if gotMode != "kiosk" {
		t.Errorf("ApplyMode got mode %q, want %q", gotMode, "kiosk")
	}
}

// TestControlServer_ApplyModeRejectsInvalidOrMissingMode proves an
// unrecognized or absent mode is a clear 400, never a silent
// un-fullscreen-the-till degrade to "normal" (ut-docs#882 review m2).
func TestControlServer_ApplyModeRejectsInvalidOrMissingMode(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	called := false
	cs.SetOps(&windowOps{
		ExitToOS:  func() error { return nil },
		ApplyMode: func(string) error { called = true; return nil },
	})

	for _, body := range []string{"mode=DROP-TABLE-lol", "", "mode="} {
		resp := authedPost(t, cs.Addr(), "/apply-mode", cs.Token(), body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST /apply-mode %q = %d, want 400", body, resp.StatusCode)
		}
	}
	if called {
		t.Error("ApplyMode must not be called for an invalid/missing mode")
	}
}

// TestControlServer_OpsErrorReturns500 proves a native-call failure
// surfaces as a clear error, not a silently-swallowed success.
func TestControlServer_OpsErrorReturns500(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	cs.SetOps(&windowOps{
		ExitToOS:  func() error { return errors.New("boom") },
		ApplyMode: func(string) error { return errors.New("boom") },
	})

	resp := authedPost(t, cs.Addr(), "/exit-to-os", cs.Token(), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

// TestControlServer_WrongOrMissingTokenReturns403 is the core of ut-docs#882
// review M2: without a correct bearer token, the channel must refuse the
// request outright — never fall through to the native call — regardless of
// whether ops are wired. Proven with ops wired (so a bug here would
// otherwise silently succeed).
func TestControlServer_WrongOrMissingTokenReturns403(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	called := false
	cs.SetOps(&windowOps{
		ExitToOS:  func() error { called = true; return nil },
		ApplyMode: func(string) error { called = true; return nil },
	})

	cases := []struct {
		name  string
		token string
	}{
		{"missing token", ""},
		{"wrong token", "not-the-real-token"},
	}
	for _, c := range cases {
		resp := authedPost(t, cs.Addr(), "/exit-to-os", c.token, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", c.name, resp.StatusCode)
		}
	}
	if called {
		t.Error("native ops must never be called without a valid token")
	}
}

// TestControlServer_OriginHeaderReturns403 rejects any request carrying an
// Origin header — the real Go client never sends one, so its presence
// marks the request as coming from a browsing context (e.g. content
// rendered in the shell's own WebView), which this loopback channel must
// refuse even if it somehow also had a valid token.
func TestControlServer_OriginHeaderReturns403(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()
	cs.SetOps(&windowOps{ExitToOS: func() error { return nil }, ApplyMode: func(string) error { return nil }})

	req, err := http.NewRequest(http.MethodPost, "http://"+cs.Addr()+"/exit-to-os", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(controlTokenHeader, cs.Token())
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /exit-to-os: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (Origin header present, even with a valid token)", resp.StatusCode)
	}
}

// TestControlServer_LoopbackOnly proves the listener is never reachable off
// 127.0.0.1 — ut-docs#882's own acceptance criteria ("this is a local
// escape hatch, not a network control surface").
func TestControlServer_LoopbackOnly(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	if !strings.HasPrefix(cs.Addr(), "127.0.0.1:") {
		t.Errorf("Addr() = %q, want a 127.0.0.1:* address", cs.Addr())
	}
}

// TestControlServer_SetOpsConcurrentAccessIsRaceFree exercises SetOps and
// the request path concurrently under -race — ut-docs#882 review n5: the
// committed suite otherwise never proved the mutex is load-bearing.
func TestControlServer_SetOpsConcurrentAccessIsRaceFree(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				cs.SetOps(&windowOps{
					ExitToOS:  func() error { return nil },
					ApplyMode: func(string) error { return nil },
				})
			}
		}
	}()

	for i := 0; i < 50; i++ {
		resp := authedPost(t, cs.Addr(), "/exit-to-os", cs.Token(), "")
		resp.Body.Close()
	}
	close(stop)
	wg.Wait()
}

// TestControlServer_InputHeartbeatRecordsAndSnapshotReportsIt is the
// ut-docs#1329 round trip: a heartbeat POST needs no windowOps at all
// (works even before the native window exists), and the snapshot GET then
// reports a small, recent age — never the -1 "never happened" sentinel.
func TestControlServer_InputHeartbeatRecordsAndSnapshotReportsIt(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()
	// Deliberately no cs.SetOps call — proves the heartbeat and snapshot
	// paths don't require a windowOps the way exit-to-os/apply-mode do.

	resp := authedPost(t, cs.Addr(), "/input-heartbeat", cs.Token(), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /input-heartbeat status = %d, want 204", resp.StatusCode)
	}

	snap := getSnapshot(t, cs.Addr(), cs.Token())
	if snap.LastInputAgeMs < 0 {
		t.Errorf("last_input_age_ms = %d, want >= 0 after a heartbeat was just recorded", snap.LastInputAgeMs)
	}
	if snap.LastInputAgeMs > 5000 {
		t.Errorf("last_input_age_ms = %d, want a small age (heartbeat just POSTed)", snap.LastInputAgeMs)
	}
	if snap.ControlAddr != cs.Addr() {
		t.Errorf("control_addr = %q, want %q", snap.ControlAddr, cs.Addr())
	}
	if snap.ProcessUptimeS < 0 {
		t.Errorf("process_uptime_s = %d, want >= 0", snap.ProcessUptimeS)
	}
}

// TestControlServer_SnapshotBeforeAnyHeartbeatIsMinusOne proves the -1
// sentinel — a fabricated 0 would read as "input a moment ago" on a till
// that has never sent a single heartbeat, exactly the kind of false
// success ADR-0064 already bans elsewhere in this file.
func TestControlServer_SnapshotBeforeAnyHeartbeatIsMinusOne(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	snap := getSnapshot(t, cs.Addr(), cs.Token())
	if snap.LastInputAgeMs != -1 {
		t.Errorf("last_input_age_ms = %d, want -1 before any heartbeat", snap.LastInputAgeMs)
	}
	if snap.WindowMode != "" {
		t.Errorf("window_mode = %q, want empty before any /apply-mode call", snap.WindowMode)
	}
}

// TestControlServer_SnapshotReflectsLastAppliedMode proves the window_mode
// field tracks what /apply-mode actually applied, not just what was asked.
func TestControlServer_SnapshotReflectsLastAppliedMode(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()
	cs.SetOps(&windowOps{ApplyMode: func(string) error { return nil }})

	resp := authedPost(t, cs.Addr(), "/apply-mode", cs.Token(), "mode=kiosk")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /apply-mode status = %d, want 204", resp.StatusCode)
	}

	snap := getSnapshot(t, cs.Addr(), cs.Token())
	if snap.WindowMode != "kiosk" {
		t.Errorf("window_mode = %q, want kiosk", snap.WindowMode)
	}
}

// TestControlServer_SetModeUpdatesSnapshotDirectly proves SetMode (review
// of ut-docs#1329) is what webview_fallback.go's launch-time apply and its
// ADR-0064 polled-shell callback actually call — neither goes through
// POST /apply-mode at all (they call applyWindowMode(w, mode) directly, in
// the same process), so without a way to record that outside the HTTP
// handler, window_mode would sit at "" for the entire life of the
// steady-state common case (no live Settings toggle since launch).
func TestControlServer_SetModeUpdatesSnapshotDirectly(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	cs.SetMode("fullscreen")

	snap := getSnapshot(t, cs.Addr(), cs.Token())
	if snap.WindowMode != "fullscreen" {
		t.Errorf("window_mode = %q, want fullscreen", snap.WindowMode)
	}
}

// TestControlServer_InputHeartbeatAndSnapshotRequireAuth proves both new
// endpoints go through the same withAuth wrapper as exit-to-os/apply-mode —
// no new trust boundary, per ut-docs#1329's acceptance criteria.
func TestControlServer_InputHeartbeatAndSnapshotRequireAuth(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	resp := authedPost(t, cs.Addr(), "/input-heartbeat", "wrong-token", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /input-heartbeat with wrong token: status = %d, want 403", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+cs.Addr()+"/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(controlTokenHeader, "wrong-token")
	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /snapshot: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusForbidden {
		t.Errorf("GET /snapshot with wrong token: status = %d, want 403", getResp.StatusCode)
	}
}

// getSnapshot performs an authed GET /snapshot and decodes the JSON body,
// the read counterpart to authedPost above.
func getSnapshot(t *testing.T, addr, token string) snapshot {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(controlTokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /snapshot status = %d, want 200", resp.StatusCode)
	}
	var snap snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}

// TestShellPollContractsMatchCommonPackage pins the cross-binary numeric
// contracts that have no compiler to enforce them (review of ut-docs#1039,
// finding 10 — same mechanism as TestEnvVarsMatchCommonPackage above):
// the shell's requested hold must not exceed the server's clamp, and the
// shell-side outage watchdog must agree with the server's own
// attached-window so both sides give up on each other on the same clock.
func TestShellPollContractsMatchCommonPackage(t *testing.T) {
	if d := time.Duration(shellPollWaitSeconds) * time.Second; d > common.ShellPollMaxWait {
		t.Errorf("shellPollWaitSeconds = %ds exceeds common.ShellPollMaxWait = %s — the server would clamp every hold", shellPollWaitSeconds, common.ShellPollMaxWait)
	}
	if shellPollDowngradeAfter != common.ShellAttachedWindow {
		t.Errorf("shellPollDowngradeAfter = %s, common.ShellAttachedWindow = %s — the two sides must give up on each other on the same clock", shellPollDowngradeAfter, common.ShellAttachedWindow)
	}
}
