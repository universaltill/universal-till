package main

// The no-native-window path of showWindow (ut-docs#2761) — deliberately free
// of the `desktop` build tag, like control.go/window_mode.go, so plain
// `go test ./...` (which never sets `-tags desktop`, see stub.go) exercises
// it. webview_fallback.go's showWindow calls browserFallback whenever the
// system WebView cannot be created (no WebView2 runtime, or WebView2 failing
// to start — e.g. stale msedgewebview2.exe processes still holding the user
// data folder after a runtime self-update).

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// startupFailureLinger is how long the shell keeps the temp page alive after
// handing it to the browser: the browser reads the file asynchronously — after
// a first-run wizard on a machine that never opened it — and the shell process
// is exiting anyway (ut-docs#5051).
var startupFailureLinger = func() { time.Sleep(5 * time.Minute) }

// startupFailureInBrowser shows the static startup-failure page when no native
// window exists (ut-docs#5051): html is written to a temp file in dir (which
// must be absolute, so the path handed to the browser is unambiguous), open
// is called on its file:// URL, linger gives the browser time to read it, then
// the file is removed (a browser still holding it on Windows leaves it to the
// OS temp cleanup; logged). open/linger are openBrowser/startupFailureLinger
// in production.
func startupFailureInBrowser(dir, html string, open func(string), linger func()) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("startup-failure page: temp dir %q is not absolute", dir)
	}
	f, err := os.CreateTemp(dir, "unitill-startup-failure-*.html")
	if err != nil {
		return err
	}
	path := f.Name()
	defer func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logging.L().Warnf("cannot remove the startup-failure page %s: %v", path, err)
		}
	}()
	_, werr := f.WriteString(html)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return err
	}
	open(fileURL(path))
	linger()
	return nil
}

// fileURL renders an absolute path as a file:// URL with spaces and other
// specials escaped ("C:\Users\A B\x.html" -> "file:///C:/Users/A%20B/x.html"),
// which rundll32's FileProtocolHandler and xdg-open both take unquoted.
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
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
