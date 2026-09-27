//go:build desktop && linux

package main

import (
	"net/http"
	"os"
	"testing"

	webview "github.com/webview/webview_go"
)

// TestShowWindow_CreationFailureTakesBrowserFallback is ut-docs#2761's
// regression test: an injected native-window creation failure (what the
// patched webview_go now reports as a nil WebView when WebView2 fails to
// start) must take the browser fallback and return — never reach Navigate
// on a dead window, which crashed with 0xc0000005. Runs without a display:
// the window is never created.
func TestShowWindow_CreationFailureTakesBrowserFallback(t *testing.T) {
	t.Setenv(minUptimeEnv, "0")
	restoreNew, restoreCookies, restoreOpen, restoreWait := newWebView, setupPersistentCookies, fallbackOpen, fallbackWait
	defer func() {
		newWebView, setupPersistentCookies, fallbackOpen, fallbackWait = restoreNew, restoreCookies, restoreOpen, restoreWait
	}()
	newWebView = func(bool) webview.WebView { return nil }
	setupPersistentCookies = func() error { return nil }
	var opened, waited string
	fallbackOpen = func(u string) { opened = u }
	fallbackWait = func(u string) { waited = u }

	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	addr := cs.Addr()

	showWindow("http://127.0.0.1:8080", "Universal Till", -1, cs)

	if opened != "http://127.0.0.1:8080" || waited != "http://127.0.0.1:8080" {
		t.Fatalf("fallback open/wait = %q/%q, want the till URL", opened, waited)
	}
	if _, err := http.Get("http://" + addr + "/diagnostics"); err == nil {
		t.Errorf("control listener still up after the fallback returned")
	}
}

// The unpatched binding wrapped a failed creation in a non-nil WebView, so
// showWindow's nil check never fired. With no display, GTK cannot open a
// window: New must now report that as nil.
func TestNew_NoDisplayReturnsNil(t *testing.T) {
	// Once any test in this process has initialised GTK against a real
	// display, clearing the variables no longer stops New from succeeding.
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("a display is available; this test needs a display-less process")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if w := webview.New(false); w != nil {
		w.Destroy()
		t.Fatalf("webview.New without a display = non-nil, want nil")
	}
}
