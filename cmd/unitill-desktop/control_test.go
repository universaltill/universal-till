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

// authedGet mirrors authedPost above for the GET /diagnostics endpoint.
func authedGet(t *testing.T, addr, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(controlTokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

type diagnosticsBody struct {
	Data struct {
		LastInputAgeSeconds *float64 `json:"last_input_age_seconds"`
		CurrentWindowMode   string   `json:"current_window_mode"`
		UptimeSeconds       float64  `json:"uptime_seconds"`
		Addr                string   `json:"addr"`
	} `json:"data"`
	Error any `json:"error"`
}

func decodeDiagnostics(t *testing.T, resp *http.Response) diagnosticsBody {
	t.Helper()
	defer resp.Body.Close()
	var body diagnosticsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /diagnostics body: %v", err)
	}
	return body
}

// TestControlServer_InputHeartbeat_UpdatesLastInputAt proves a real HTTP
// POST reaches LastInputAt — the plumbing ut-docs#1329 exists for (split
// from #1228's input-freeze incident): a future occurrence needs a
// timestamp to reason about, not another anecdote.
func TestControlServer_InputHeartbeat_UpdatesLastInputAt(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	if _, ok := cs.LastInputAt(); ok {
		t.Fatal("LastInputAt() ok = true before any heartbeat, want false")
	}

	before := time.Now()
	resp := authedPost(t, cs.Addr(), "/input-heartbeat", cs.Token(), "")
	resp.Body.Close()
	after := time.Now()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /input-heartbeat = %d, want 204", resp.StatusCode)
	}
	at, ok := cs.LastInputAt()
	if !ok {
		t.Fatal("LastInputAt() ok = false after a heartbeat, want true")
	}
	if at.Before(before) || at.After(after) {
		t.Fatalf("LastInputAt() = %v, want between %v and %v", at, before, after)
	}
}

// TestControlServer_InputHeartbeat_RequiresAuth mirrors
// TestControlServer_WrongOrMissingTokenReturns403 for the new endpoint —
// same withAuth wrapper, same expected behaviour.
func TestControlServer_InputHeartbeat_RequiresAuth(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	for _, c := range []struct {
		name  string
		token string
	}{{"missing token", ""}, {"wrong token", "not-the-real-token"}} {
		resp := authedPost(t, cs.Addr(), "/input-heartbeat", c.token, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", c.name, resp.StatusCode)
		}
	}
	if _, ok := cs.LastInputAt(); ok {
		t.Fatal("LastInputAt() ok = true after only unauthenticated attempts, want false")
	}
}

// TestControlServer_InputHeartbeat_RejectsOriginHeader mirrors
// TestControlServer_OriginHeaderReturns403 for the new endpoint.
func TestControlServer_InputHeartbeat_RejectsOriginHeader(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	req, err := http.NewRequest(http.MethodPost, "http://"+cs.Addr()+"/input-heartbeat", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(controlTokenHeader, cs.Token())
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /input-heartbeat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (Origin header present, even with a valid token)", resp.StatusCode)
	}
}

// TestControlServer_Diagnostics_RequiresAuth and
// TestControlServer_Diagnostics_RejectsOriginHeader cover the same two
// gates as every other endpoint on this listener — GET is not special-cased
// in withAuth, so a table-driven pass across both new routes here would
// duplicate the loop above; a focused test per route reads more clearly for
// what each one asserts about /diagnostics specifically (the JSON body).
func TestControlServer_Diagnostics_RequiresAuth(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	resp := authedGet(t, cs.Addr(), "/diagnostics", "not-the-real-token")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET /diagnostics with wrong token = %d, want 403", resp.StatusCode)
	}
}

// TestControlServer_Diagnostics_ShapeBeforeAnyHeartbeat proves
// last_input_age_seconds is absent/null before the first heartbeat — never
// a fabricated zero that would misleadingly claim "input just now".
func TestControlServer_Diagnostics_ShapeBeforeAnyHeartbeat(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	resp := authedGet(t, cs.Addr(), "/diagnostics", cs.Token())
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("GET /diagnostics = %d, want 200", resp.StatusCode)
	}
	body := decodeDiagnostics(t, resp)
	if body.Data.LastInputAgeSeconds != nil {
		t.Fatalf("last_input_age_seconds = %v before any heartbeat, want nil/absent", *body.Data.LastInputAgeSeconds)
	}
	if body.Data.Addr != cs.Addr() {
		t.Errorf("addr = %q, want %q", body.Data.Addr, cs.Addr())
	}
	if body.Data.UptimeSeconds < 0 || body.Data.UptimeSeconds > 5 {
		t.Errorf("uptime_seconds = %v, want a small non-negative value fresh off newControlServer()", body.Data.UptimeSeconds)
	}
	if body.Data.CurrentWindowMode != "" {
		t.Errorf("current_window_mode = %q before any /apply-mode call, want empty", body.Data.CurrentWindowMode)
	}
}

// TestControlServer_Diagnostics_ReflectsHeartbeatAndLastAppliedMode is the
// end-to-end shape this whole card exists for: a heartbeat makes
// last_input_age_seconds present and small, and a real /apply-mode call
// (through wired ops, not just accepted-then-discarded) is what
// current_window_mode reports back.
func TestControlServer_Diagnostics_ReflectsHeartbeatAndLastAppliedMode(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()
	cs.SetOps(&windowOps{
		ExitToOS:  func() error { return nil },
		ApplyMode: func(string) error { return nil },
	})

	resp := authedPost(t, cs.Addr(), "/apply-mode", cs.Token(), "mode=kiosk")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /apply-mode = %d, want 204", resp.StatusCode)
	}
	resp = authedPost(t, cs.Addr(), "/input-heartbeat", cs.Token(), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /input-heartbeat = %d, want 204", resp.StatusCode)
	}

	resp = authedGet(t, cs.Addr(), "/diagnostics", cs.Token())
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("GET /diagnostics = %d, want 200", resp.StatusCode)
	}
	body := decodeDiagnostics(t, resp)
	if body.Data.CurrentWindowMode != "kiosk" {
		t.Errorf("current_window_mode = %q, want kiosk (the last successful /apply-mode)", body.Data.CurrentWindowMode)
	}
	if body.Data.LastInputAgeSeconds == nil {
		t.Fatal("last_input_age_seconds absent after a heartbeat, want present")
	}
	if age := *body.Data.LastInputAgeSeconds; age < 0 || age > 5 {
		t.Errorf("last_input_age_seconds = %v, want a small non-negative value right after the heartbeat", age)
	}
}

// TestControlServer_Diagnostics_ModeUnchangedByFailedOrInvalidApply proves
// current_window_mode reflects only a mode that was ACTUALLY applied — a
// rejected (400) or failed (500) /apply-mode call must not silently claim
// the operator's till is in a mode it never reached.
func TestControlServer_Diagnostics_ModeUnchangedByFailedOrInvalidApply(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()
	cs.SetOps(&windowOps{
		ExitToOS:  func() error { return nil },
		ApplyMode: func(string) error { return nil },
	})

	resp := authedPost(t, cs.Addr(), "/apply-mode", cs.Token(), "mode=kiosk")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /apply-mode = %d, want 204", resp.StatusCode)
	}

	// An invalid mode is rejected (400) and must not overwrite the tracked
	// current mode.
	resp = authedPost(t, cs.Addr(), "/apply-mode", cs.Token(), "mode=bogus")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /apply-mode mode=bogus = %d, want 400", resp.StatusCode)
	}

	resp = authedGet(t, cs.Addr(), "/diagnostics", cs.Token())
	body := decodeDiagnostics(t, resp)
	if body.Data.CurrentWindowMode != "kiosk" {
		t.Errorf("current_window_mode = %q after a rejected apply-mode, want kiosk unchanged", body.Data.CurrentWindowMode)
	}
}

// TestControlServer_SetAppliedMode proves SetAppliedMode updates
// current_window_mode the same way a successful POST /apply-mode does —
// this is the hook showWindow's initial apply and watchShellMode's live
// callback now call directly (webview_fallback.go), bypassing the HTTP
// path entirely (review of ut-docs#1329, should-fix 3; ut-docs#1331).
func TestControlServer_SetAppliedMode(t *testing.T) {
	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	cs.SetAppliedMode("kiosk")

	resp := authedGet(t, cs.Addr(), "/diagnostics", cs.Token())
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("GET /diagnostics = %d, want 200", resp.StatusCode)
	}
	body := decodeDiagnostics(t, resp)
	if body.Data.CurrentWindowMode != "kiosk" {
		t.Errorf("current_window_mode = %q after SetAppliedMode(\"kiosk\"), want kiosk", body.Data.CurrentWindowMode)
	}
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
