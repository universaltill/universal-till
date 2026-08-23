//go:build desktop && !darwin

package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	webview "github.com/webview/webview_go"
)

// showWindow opens the webview_go window (Windows: Edge WebView2, Linux: GTK
// WebKit) and blocks until it closes. Run() returns on close, so main's
// deferred Kill stops the server; childPid is unused here.
//
// When the system WebView is unavailable (typically a Windows box without the
// WebView2 runtime), webview.New returns nil — fall back to the default
// browser and keep serving until the server itself exits, which is exactly
// the pre-shell behaviour. ctl is still very much non-nil here (main starts
// the control listener before it even knows whether webview.New will
// succeed, and already handed its address to the child) — ops are wired as
// explicit no-ops in this branch too, same as webkit_darwin.go's own
// reasoning, so a live toggle degrades to today's actual pre-#882 behavior
// (persist-only, silent 204) instead of a 503/500 regression (ut-docs#882
// review M1). ctl is nil only if this process's own control listener
// failed to bind (newControlServer's error case in desktop.go) — no live
// channel to wire either way.
func showWindow(url, title string, childPid int, ctl *controlServer) {
	_ = childPid
	w := webview.New(false)
	if w == nil {
		if ctl != nil {
			ctl.SetOps(&windowOps{
				ExitToOS:  func() error { return nil },
				ApplyMode: func(string) error { return nil },
			})
			defer ctl.Close()
		}
		openBrowser(url)
		waitForServer(url)
		return
	}
	defer w.Destroy()

	// Live, no-relaunch channel (ut-docs#882), wired as early as possible —
	// right after the window exists, before the (network-bound)
	// fetchShellPrefs/reconcileAutostart calls below — so the window where
	// a live toggle would still see ops==nil (503) stays as small as
	// physically possible (review M1's narrower point). ctl.Close() is
	// deferred here too, registered AFTER defer w.Destroy() above so LIFO
	// runs it FIRST when showWindow returns: the control listener stops
	// accepting and drains any in-flight request before the window it
	// dispatches onto is destroyed, closing the use-after-free window
	// review m1 found (Dispatch onto a freed *C.struct_webview after
	// w.Destroy(), if a request landed in the narrow gap between Destroy()
	// and main's own, later-running Close()).
	//
	// exit-to-os is "apply mode normal" — un-fullscreen and re-decorate,
	// returning control to the OS desktop — and apply-mode forwards
	// whatever mode a live Settings toggle sent. Both dispatch onto
	// webview_go's own UI thread (required: this control listener's
	// handler runs on its own goroutine, not the thread GTK/WebView2 calls
	// must happen on) and report success immediately — Dispatch is
	// fire-and-forget with no completion signal, the same
	// best-effort-and-log convention #611's own review accepted for
	// OS-level apply failures (settings_page.go's own comment on the
	// window-mode handler spells out that tradeoff for this specific
	// path). On Windows, applyWindowMode is still #610's own no-op stub,
	// so this wiring is inert there today but needs no further change once
	// #610 lands a real implementation.
	if ctl != nil {
		defer ctl.Close()
		ctl.SetOps(&windowOps{
			ExitToOS: func() error {
				w.Dispatch(func() { applyWindowMode(w, "normal") })
				return nil
			},
			ApplyMode: func(mode string) error {
				w.Dispatch(func() { applyWindowMode(w, mode) })
				return nil
			},
		})
	}

	w.SetTitle(title)
	w.SetSize(1280, 860, webview.HintNone)
	// Both applied once at launch, from whatever's persisted right now
	// (ut-docs#611) — "applies live or on next launch" is explicitly
	// acceptable per #549. A fetch failure degrades to defaultShellPrefs
	// (see fetchShellPrefs) rather than blocking the window from opening.
	//
	// Autostart is reconciled here — in this process, not unitill-pos's —
	// because this is the process that actually knows its own executable
	// path and, critically on a .deb install, is the one running as the
	// interactive desktop user (see reconcileAutostart's doc comment;
	// ut-docs#611 review, M2/M3).
	prefs := fetchShellPrefs(url)
	applyWindowMode(w, prefs.WindowMode)
	if err := reconcileAutostart(prefs.LaunchOnStartup); err != nil {
		fmt.Fprintln(os.Stderr, "reconcile autostart entry:", err)
	}

	w.Navigate(url)
	w.Run()
}

// openBrowser opens the OS default browser on url, best-effort.
func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}

// waitForServer blocks while the till still answers; when it stops (server
// quit or crashed) the shell exits too instead of lingering invisibly.
func waitForServer(url string) {
	client := &http.Client{Timeout: 5 * time.Second}
	misses := 0
	for {
		time.Sleep(15 * time.Second)
		resp, err := client.Get(url)
		if err != nil {
			misses++
			if misses >= 2 {
				return
			}
			continue
		}
		resp.Body.Close()
		misses = 0
	}
}
