//go:build desktop

// unitill-desktop — a native desktop app that runs the Universal Till server
// and shows it in an embedded WebView window (no browser chrome). It launches
// the sibling `unitill-pos` binary, waits for it to accept connections, then
// opens the window; closing the window stops the server.
//
// The window itself is platform-specific (showWindow): macOS uses a native
// WKWebView shell with clipboard, file pickers, camera permission and popups
// (webkit_darwin.go); other platforms use webview_go (webview_fallback.go).
//
// Build (macOS/Windows/Linux, needs CGO + the system WebView):
//
//	go build -tags desktop -o unitill-desktop ./cmd/unitill-desktop
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// The native window toolkits (Cocoa on macOS, GTK via webview_go elsewhere)
// require all UI calls on the process's FIRST thread. Locking during package
// init — before main() and before any other goroutine exists — pins the main
// goroutine to that initial thread. Without this, main() blocking on the
// wait-for-server dial loop lets the scheduler migrate it to another thread,
// and Cocoa aborts in showWindow with "API misuse: setting the main menu on
// a non-main thread" — the v0.2.11 field crash (app flashed and never opened;
// the spawned unitill-pos servers were left orphaned).
func init() {
	runtime.LockOSThread()
}

func main() {
	// Attach mode: on a .deb install the till already runs as a systemd
	// service on :8080 — spawning a second server would fight over the same
	// database. If something that answers our /healthz is already there,
	// just open the window on it (also makes a second app launch join the
	// running till instead of starting another).
	if tillAlreadyRunning("127.0.0.1:8080") {
		showWindow("http://127.0.0.1:8080", "Universal Till", -1)
		return
	}

	// Prefer :8080 (so the operator can also open the till in a normal browser
	// at a known address), falling back to any free port if it's taken.
	port := freePort("8080")
	addr := "127.0.0.1:" + port

	// The POS binary ships next to this executable; fall back to PATH in dev.
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	posBin := filepath.Join(dir, posBinaryName())
	if _, err := os.Stat(posBin); err != nil {
		posBin = posBinaryName() // dev: rely on PATH / current dir
	}

	// The server resolves web/ relative to its working directory. That is the
	// executable's own dir in dev and in the tar/deb layout, but inside a macOS
	// .app the binaries live in Contents/MacOS while web/ (a resource, not code)
	// lives in Contents/Resources — so codesign accepts the bundle. Pick
	// whichever dir actually contains web/.
	workDir := dir
	if !dirHasWeb(workDir) {
		// mac .app: binaries in Contents/MacOS, web/ in Contents/Resources.
		// deb: binaries in /opt/unitill/bin, web/ in /opt/unitill.
		for _, cand := range []string{filepath.Join(dir, "..", "Resources"), filepath.Join(dir, "..")} {
			if dirHasWeb(cand) {
				workDir = cand
				break
			}
		}
	}

	cmd := exec.Command(posBin)
	cmd.Dir = workDir // so web/ resolves; data/ defaults to the per-user dir
	// We supply the window ourselves, so tell the server not to also pop a browser.
	cmd.Env = append(os.Environ(), "UT_OPEN_BROWSER=0", "UT_LISTEN_ADDR="+addr)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	configureChild(cmd) // Windows: keep the server's console window hidden
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "failed to start the till:", err)
		os.Exit(1)
	}
	defer func() { _ = cmd.Process.Kill() }()

	// Wait (up to ~10s) for the server to accept connections.
	for range 100 {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Blocks until the window closes. On macOS closing the window terminates the
	// process (skipping the deferred Kill), so showWindow is told the server PID
	// to stop it first.
	showWindow("http://"+addr, "Universal Till", cmd.Process.Pid)
}

// tillAlreadyRunning reports whether a Universal Till server answers on addr
// (its /healthz — not just any process squatting on the port).
func tillAlreadyRunning(addr string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// freePort returns `preferred` when that port is free (so the till lives at a
// predictable address), else a free loopback port chosen by the OS. The server
// has its own next-free-port fallback for the small probe→bind race.
func freePort(preferred string) string {
	if preferred != "" {
		if ln, err := net.Listen("tcp", "127.0.0.1:"+preferred); err == nil {
			_ = ln.Close()
			return preferred
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return preferred
	}
	defer ln.Close()
	_, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return preferred
	}
	return p
}

// dirHasWeb reports whether dir contains a web/ subdirectory.
func dirHasWeb(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "web"))
	return err == nil && info.IsDir()
}
