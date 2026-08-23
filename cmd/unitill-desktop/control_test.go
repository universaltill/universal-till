package main

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

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
