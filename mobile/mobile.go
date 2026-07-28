// Package mobile is the gomobile-bind entry point for the Android/iOS till
// shells (ADR-0023, ut-docs): starts the same server internal/app.Run drives
// for the CLI/desktop build, in-process — a mobile app can't spawn a sibling
// binary the way cmd/unitill-desktop's WebView shell spawns unitill-pos as a
// child process, so this package drives the identical boot sequence directly
// instead. Once running, the native shell points its own WebView at the
// returned loopback address — same "start, poll until it answers, then show
// a window" shape as cmd/unitill-desktop/desktop.go, just in-process instead
// of a child process.
//
// gomobile bind's cross-language boundary only supports a narrow set of
// types (strings, ints, bools, []byte, error, and a few others — no generics,
// no complex struct fields crossing directly) — this package's exported
// surface is deliberately minimal for that reason: three functions, no
// exported types.
//
// This package mutates PROCESS-WIDE environment variables (os.Setenv) to
// configure internal/app.Run, the same way the CLI's own pos.env/shell
// environment does. That's fine for a mobile app where this package owns
// the whole process, but means this is NOT safe to embed alongside other
// Go code that also touches os.Setenv/os.Environ() concurrently outside
// this package's own synchronization.
//
// Build (needs the Android SDK+NDK or Xcode, per platform — see
// ut-docs/adr/0023-android-ios-till-strategy.md for the full setup):
//
//	gomobile bind -target=android ./mobile
//	gomobile bind -target=ios ./mobile
package mobile

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/app"
)

// instance is one running server lifecycle. Start/Stop/IsRunning agree on
// current state by swapping this pointer under mu — never by holding mu
// across the blocking work (starting or stopping the actual server), so a
// slow Start can never make IsRunning/Stop block for the whole timeout.
type instance struct {
	dataDir string
	addr    string
	cancel  context.CancelFunc
	done    chan struct{} // closed once app.Run has fully returned
	err     error         // app.Run's result; only valid after done is closed
}

var (
	mu       sync.Mutex
	inst     *instance
	starting bool
)

// Start boots the till server with its on-device data directory at dataDir
// (the native shell resolves this to whatever platform-appropriate sandboxed
// storage path it has — Android's Context.getFilesDir(), iOS's
// NSSearchPathForDirectoriesInDomains(.applicationSupportDirectory), or
// similar) and returns "127.0.0.1:<port>" once the server is confirmed
// accepting connections — ready for a WebView to load.
//
// Idempotent while genuinely running: a second Start call with the same
// dataDir just returns the existing address (mirrors cmd/unitill-desktop's
// tillAlreadyRunning re-attach behavior — useful if the native shell's
// lifecycle calls Start more than once, e.g. iOS returning from
// background). A second Start with a DIFFERENT dataDir while already
// running is an error, not a silent ignore — the caller asked for
// something this process can't currently give it. If the server died on
// its own since the last successful Start (e.g. a listener error, not a
// Stop() call), Start does not lie about still being up — it notices via
// the dead instance's closed done channel and starts fresh instead of
// returning a stale address.
func Start(dataDir string) (string, error) {
	mu.Lock()
	if inst != nil {
		select {
		case <-inst.done:
			inst = nil // died on its own; fall through and start fresh
		default:
			if inst.dataDir != dataDir {
				addr := inst.addr
				mu.Unlock()
				return "", fmt.Errorf("mobile: already running against %q, cannot Start against %q (current address %s)", inst.dataDir, dataDir, addr)
			}
			addr := inst.addr
			mu.Unlock()
			return addr, nil
		}
	}
	if starting {
		mu.Unlock()
		return "", fmt.Errorf("mobile: Start already in progress")
	}
	starting = true
	mu.Unlock()
	defer func() {
		mu.Lock()
		starting = false
		mu.Unlock()
	}()

	port, err := freePort()
	if err != nil {
		return "", fmt.Errorf("mobile: find a free port: %w", err)
	}
	addr := "127.0.0.1:" + port

	if err := os.Setenv("UT_DATA_DIR", dataDir); err != nil {
		return "", fmt.Errorf("mobile: set UT_DATA_DIR: %w", err)
	}
	if err := os.Setenv("UT_LISTEN_ADDR", addr); err != nil {
		return "", fmt.Errorf("mobile: set UT_LISTEN_ADDR: %w", err)
	}
	// The native shell supplies its own window/WebView; the server must
	// never try to open an OS browser on its own (same reasoning as
	// cmd/unitill-desktop/desktop.go's identical env var).
	if err := os.Setenv("UT_OPEN_BROWSER", "0"); err != nil {
		return "", fmt.Errorf("mobile: set UT_OPEN_BROWSER: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	newInst := &instance{dataDir: dataDir, addr: addr, cancel: cancel, done: make(chan struct{})}

	go func() {
		newInst.err = app.Run(ctx)
		close(newInst.done)
	}()

	// 10s was too tight in practice: a real low/mid-range Android phone's
	// first boot (fresh install: 18 migrations against a cold SQLite file,
	// plus wazero compiling any bundled plugins' WASM with no cache yet)
	// missed it and surfaced as "server did not become ready within 10s"
	// (reported 2026-07-28, real device). Desktop's equivalent wait
	// (cmd/unitill-desktop/desktop.go) made the same ~10s assumption, but
	// phone hardware skews weaker and more variable than desktop/POS
	// terminal hardware, so mobile gets more headroom than desktop rather
	// than copying its number.
	if err := waitUntilReady(addr, 30*time.Second, newInst); err != nil {
		cancel()
		<-newInst.done // wait for the full teardown before reporting failure
		return "", err
	}

	mu.Lock()
	inst = newInst
	mu.Unlock()
	return addr, nil
}

// Stop gracefully shuts the server down and BLOCKS until it has actually
// finished — including internal/server.Start's own shutdown drain and
// internal/app.Run's deferred database.Close() — not just until the
// shutdown signal has been sent. This matters: a native shell that calls
// Stop() and then quickly Start()s again against the SAME on-device
// dataDir (e.g. an app backgrounded then rapidly foregrounded) must not
// race the old instance's in-flight teardown against the new instance's
// db.Open() on the same SQLite file. Safe to call when not running
// (no-op). A caller wanting non-blocking behavior should call this from
// its own background thread/coroutine, same as any blocking mobile API.
func Stop() {
	mu.Lock()
	cur := inst
	inst = nil
	mu.Unlock()
	if cur == nil {
		return
	}
	cur.cancel()
	<-cur.done
}

// IsRunning reports whether a Start'd server is genuinely still up — not
// just whether Start was last called without a matching Stop. If the
// server died on its own (closed done channel, no Stop() call), this
// notices and corrects the tracked state instead of reporting stale
// success.
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	if inst == nil {
		return false
	}
	select {
	case <-inst.done:
		inst = nil
		return false
	default:
		return true
	}
}

// freePort asks the OS for an unused loopback port. Same probe-then-close
// pattern as cmd/unitill-desktop/desktop.go's freePort, and the same small,
// accepted race: internal/server.Start's own listenWithFallback tolerates
// the port being taken by the time it actually binds, falling back to a
// different one — which cmd/unitill-desktop also doesn't defend against,
// so this doesn't introduce a new gap, just matches the existing one.
func freePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	return port, err
}

// waitUntilReady polls addr's /healthz until it answers 200, timeout
// elapses, or app.Run itself already exited (a fast, fatal startup
// failure — e.g. a bad config — shouldn't make the caller wait out the
// full timeout to find out).
func waitUntilReady(addr string, timeout time.Duration, inst *instance) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-inst.done:
			if inst.err != nil {
				return fmt.Errorf("mobile: server failed to start: %w", inst.err)
			}
			return fmt.Errorf("mobile: server exited before becoming ready")
		default:
		}
		if resp, err := client.Get("http://" + addr + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("mobile: server did not become ready within %s", timeout)
}
