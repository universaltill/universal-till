package main

// The no-native-window path of showWindow (ut-docs#2761) — deliberately free
// of the `desktop` build tag, like control.go/window_mode.go, so plain
// `go test ./...` (which never sets `-tags desktop`, see stub.go) exercises
// it. webview_fallback.go's showWindow calls browserFallback whenever the
// system WebView cannot be created (no WebView2 runtime, or WebView2 failing
// to start — e.g. stale msedgewebview2.exe processes still holding the user
// data folder after a runtime self-update).

import (
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// browserFallback opens url in the OS default browser and keeps serving
// until the server itself stops — the pre-shell behaviour — logging reason
// to desktop.log (ut-docs#2720) so "the app opens and closes" is never
// silent again. ctl gets explicit no-op ops so a live toggle degrades to
// persist-only (silent 204) instead of a 503 (ut-docs#882 review M1); a nil
// ctl means this process's control listener never bound. browserFallback
// owns ctl.Close(). open/wait are openBrowser/waitForServer in production.
func browserFallback(url string, ctl *controlServer, reason string, open, wait func(string)) {
	logging.L().Warnf("no native window: %s; opening the till in the default browser at %s instead", reason, url)
	if ctl != nil {
		ctl.SetOps(&windowOps{
			ExitToOS:  func() error { return nil },
			ApplyMode: func(string) error { return nil },
		})
		defer ctl.Close()
	}
	open(url)
	wait(url)
}

// webViewInitFailureReason renders why the native window could not be
// created: the WebView2 HRESULT from webview.LastInitError (0 when unknown,
// or on a platform that records none) and the installed runtime version
// (empty when unknown).
func webViewInitFailureReason(hresult int32, runtimeVersion string) string {
	hr := "HRESULT unknown"
	if hresult != 0 {
		hr = fmt.Sprintf("HRESULT 0x%08X", uint32(hresult))
	}
	ver := "runtime version unknown"
	if runtimeVersion != "" {
		ver = "runtime " + runtimeVersion
	}
	return fmt.Sprintf("system WebView failed to start (%s, %s)", hr, ver)
}

// fallbackOpen/fallbackWait are what showWindow hands browserFallback —
// vars so a test can drive showWindow's fallback branch without launching
// a browser or polling for 30s.
var (
	fallbackOpen = openBrowser
	fallbackWait = waitForServer
)

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
