//go:build desktop && !darwin

package main

import (
	"net/http"
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
