//go:build desktop && windows

package main

import (
	"fmt"

	"github.com/universaltill/universal-till/internal/logging"

	webview "github.com/webview/webview_go"
)

// init claims the live window-control capability (ADR-0064): Windows now
// really applies and leaves every window mode (ut-docs#610), so the server
// may serve it fullscreen/kiosk — see shellAppliesWindowMode (window_mode.go).
func init() { shellAppliesWindowMode = true }

// applyWindowMode applies the persisted display.window_mode to the WebView2
// shell's own window (ut-docs#610). Runs on the UI thread — once before Run,
// then via Dispatch for live changes. A failure is logged, never fatal: the
// till keeps working in whatever window it has.
func applyWindowMode(w webview.WebView, mode string) {
	if err := applyWin32WindowMode(uintptr(w.Window()), flagsForWindowMode(mode)); err != nil {
		fmt.Fprintf(logging.Stderr(), "apply window mode %q: %v\n", mode, err)
	}
}
