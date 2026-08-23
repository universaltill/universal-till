package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

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
		resp, err := http.Post("http://"+cs.Addr()+path, "application/x-www-form-urlencoded", nil)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
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

	resp, err := http.Post("http://"+cs.Addr()+"/exit-to-os", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /exit-to-os: %v", err)
	}
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

	resp, err := http.Post("http://"+cs.Addr()+"/apply-mode", "application/x-www-form-urlencoded",
		strings.NewReader("mode=kiosk"))
	if err != nil {
		t.Fatalf("POST /apply-mode: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if gotMode != "kiosk" {
		t.Errorf("ApplyMode got mode %q, want %q", gotMode, "kiosk")
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

	resp, err := http.Post("http://"+cs.Addr()+"/exit-to-os", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /exit-to-os: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
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
