package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// flagsForWindowMode is the pure mapping from a persisted display.window_mode
// value to what the native window should do (ut-docs#611) — deliberately
// kept free of cgo/OS build tags so `go test ./...` (which never passes
// `-tags desktop`, see stub.go) actually exercises it; the per-OS glue that
// calls real GTK/WebView2/Cocoa APIs stays thin by design.
func TestFlagsForWindowMode(t *testing.T) {
	cases := []struct {
		mode string
		want windowModeFlags
	}{
		{"fullscreen", windowModeFlags{Fullscreen: true, Decorated: false}},
		{"kiosk", windowModeFlags{Fullscreen: true, Decorated: false}},
		{"maximized", windowModeFlags{Decorated: true, Maximize: true}},
		{"normal", windowModeFlags{Decorated: true}},
		{"", windowModeFlags{Decorated: true}},      // unrecognized -> normal
		{"bogus", windowModeFlags{Decorated: true}}, // unrecognized -> normal, never crash
	}
	for _, c := range cases {
		if got := flagsForWindowMode(c.mode); got != c.want {
			t.Errorf("flagsForWindowMode(%q) = %+v, want %+v", c.mode, got, c.want)
		}
	}
}

func TestFetchShellPrefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/window-mode" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"window_mode":"kiosk","launch_on_startup":true},"error":null}`))
	}))
	defer srv.Close()

	got := fetchShellPrefs(srv.URL)
	if got.WindowMode != "kiosk" {
		t.Errorf("WindowMode = %q, want kiosk", got.WindowMode)
	}
	if !got.LaunchOnStartup {
		t.Error("LaunchOnStartup = false, want true")
	}
}

func TestFetchShellPrefs_UnreachableServerFallsBackToDefault(t *testing.T) {
	// Nothing listening on this port — window creation must never block or
	// crash because the settings fetch failed.
	if got := fetchShellPrefs("http://127.0.0.1:1"); got != defaultShellPrefs {
		t.Errorf("fetchShellPrefs(unreachable) = %+v, want %+v", got, defaultShellPrefs)
	}
}

func TestFetchShellPrefs_NonOKStatusFallsBackToDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An older unitill-pos with no /api/window-mode route, or any
		// other non-2xx — must not be trusted even if the body happens to
		// parse (e.g. an HTML error page a proxy inserted).
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"data":{"window_mode":"kiosk","launch_on_startup":true},"error":"not found"}`))
	}))
	defer srv.Close()

	if got := fetchShellPrefs(srv.URL); got != defaultShellPrefs {
		t.Errorf("fetchShellPrefs(404) = %+v, want %+v (must not trust a non-200 body)", got, defaultShellPrefs)
	}
}

func TestFetchShellPrefs_MalformedResponseFallsBackToDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	if got := fetchShellPrefs(srv.URL); got != defaultShellPrefs {
		t.Errorf("fetchShellPrefs(malformed) = %+v, want %+v", got, defaultShellPrefs)
	}
}

func TestFetchShellPrefs_UnrecognizedModeFallsBackToNormalButKeepsStartupFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"window_mode":"something-new","launch_on_startup":true},"error":null}`))
	}))
	defer srv.Close()

	got := fetchShellPrefs(srv.URL)
	if got.WindowMode != "normal" {
		t.Errorf("WindowMode = %q, want normal", got.WindowMode)
	}
	// An unrecognized mode is this build predating a future value — no
	// reason to distrust the OTHER field in the same response too.
	if !got.LaunchOnStartup {
		t.Error("LaunchOnStartup = false, want true (unrelated to the bad window_mode value)")
	}
}
