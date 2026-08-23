package common

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewHTTPWindowControllerFromEnv_AbsentReturnsFalse(t *testing.T) {
	t.Setenv(EnvDesktopControlAddr, "")
	if _, ok := NewHTTPWindowControllerFromEnv(); ok {
		t.Fatal("ok = true with the env var unset, want false")
	}
}

func TestNewHTTPWindowControllerFromEnv_PresentReturnsController(t *testing.T) {
	t.Setenv(EnvDesktopControlAddr, "127.0.0.1:9") // discard port; just needs to be non-empty
	wc, ok := NewHTTPWindowControllerFromEnv()
	if !ok {
		t.Fatal("ok = false with the env var set, want true")
	}
	if _, isHTTP := wc.(HTTPWindowController); !isHTTP {
		t.Fatalf("got %T, want HTTPWindowController", wc)
	}
}

// TestHTTPWindowController_ExitToOS_ReachesControlChannel proves a real
// HTTP round trip against the shell's control channel, not just that the
// method compiles.
func TestHTTPWindowController_ExitToOS_ReachesControlChannel(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := HTTPWindowController{addr: strings.TrimPrefix(srv.URL, "http://"), client: srv.Client()}
	if err := c.ExitToOS(); err != nil {
		t.Fatalf("ExitToOS() = %v, want nil", err)
	}
	if gotPath != "/exit-to-os" {
		t.Errorf("path = %q, want /exit-to-os", gotPath)
	}
}

// TestHTTPWindowController_ApplyMode_SendsModeForm proves the mode value
// reaches the shell as a form field, which is what its control.go
// (r.Form.Get("mode")) expects.
func TestHTTPWindowController_ApplyMode_SendsModeForm(t *testing.T) {
	var gotMode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotMode = r.Form.Get("mode")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := HTTPWindowController{addr: strings.TrimPrefix(srv.URL, "http://"), client: srv.Client()}
	if err := c.ApplyMode("kiosk"); err != nil {
		t.Fatalf("ApplyMode(kiosk) = %v, want nil", err)
	}
	if gotMode != "kiosk" {
		t.Errorf("mode = %q, want kiosk", gotMode)
	}
}

// TestHTTPWindowController_UnreachableReturnsError is the "shell exited, or
// predates this card and never started the listener" case ut-docs#882's own
// acceptance criteria names — must be a clear error, never a panic.
func TestHTTPWindowController_UnreachableReturnsError(t *testing.T) {
	c := HTTPWindowController{addr: "127.0.0.1:1", client: &http.Client{}}
	if err := c.ExitToOS(); err == nil {
		t.Fatal("ExitToOS() against an unreachable address = nil, want an error")
	}
}

// TestHTTPWindowController_NonOKStatusReturnsError covers the shell being
// up but returning e.g. 503 (no native handler wired for this platform,
// see controlServer.errNoOps) — must not be mistaken for success.
func TestHTTPWindowController_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "desktop shell window not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := HTTPWindowController{addr: strings.TrimPrefix(srv.URL, "http://"), client: srv.Client()}
	if err := c.ExitToOS(); err == nil {
		t.Fatal("ExitToOS() against a 503 = nil, want an error")
	}
}
