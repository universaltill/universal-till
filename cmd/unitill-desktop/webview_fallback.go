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
// the pre-shell behaviour.
func showWindow(url, title string, childPid int) {
	_ = childPid
	w := webview.New(false)
	if w == nil {
		openBrowser(url)
		waitForServer(url)
		return
	}
	defer w.Destroy()
	w.SetTitle(title)
	w.SetSize(1280, 860, webview.HintNone)
	// Both applied once at launch, from whatever's persisted right now
	// (ut-docs#611) — "applies live or on next launch" is explicitly
	// acceptable per #549; the live, no-relaunch case needs a cross-process
	// channel to the already-running unitill-pos server, split off as
	// ut-docs#882. A fetch failure degrades to defaultShellPrefs (see
	// fetchShellPrefs) rather than blocking the window from opening.
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
