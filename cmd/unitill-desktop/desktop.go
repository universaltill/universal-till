//go:build desktop

// unitill-desktop — a native desktop app that runs the Universal Till server
// and shows it in an embedded WebView window (no browser chrome). It launches
// the sibling `unitill-pos` binary, waits for it to accept connections, then
// opens the window; closing the window stops the server.
//
// Build (macOS/Windows/Linux, needs CGO + the system WebView):
//
//	go build -tags desktop -o unitill-desktop ./cmd/unitill-desktop
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	webview "github.com/webview/webview_go"
)

func main() {
	const port = "8080"
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
	if _, err := os.Stat(filepath.Join(dir, "web")); err != nil {
		if res := filepath.Join(dir, "..", "Resources"); dirHasWeb(res) {
			workDir = res
		}
	}

	cmd := exec.Command(posBin)
	cmd.Dir = workDir // so web/ resolves; data/ defaults to the per-user dir
	// We supply the window ourselves, so tell the server not to also pop a browser.
	cmd.Env = append(os.Environ(), "UT_OPEN_BROWSER=0", "UT_LISTEN_ADDR=:"+port)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
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

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Universal Till")
	w.SetSize(1280, 860, webview.HintNone)
	w.Navigate("http://" + addr)
	w.Run() // blocks until the window closes
}

// dirHasWeb reports whether dir contains a web/ subdirectory.
func dirHasWeb(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "web"))
	return err == nil && info.IsDir()
}
