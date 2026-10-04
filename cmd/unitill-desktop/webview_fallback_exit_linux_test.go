//go:build desktop && linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	webview "github.com/webview/webview_go"
)

// windowModeNormalLogLine is the one line (control.go's diagnostics
// handler) that only appears once GET /diagnostics has actually observed
// current_window_mode == "normal" — proof the ut-docs#1382 fix under test
// ran to completion, independent of whatever the process does afterwards.
const windowModeNormalLogLine = `window_mode="normal"`

// TestDesktopWindowOps_ExitToOSRecordsAppliedMode drives a REAL native
// window (GTK/WebKit) through the exact ExitToOS closure showWindow wires
// into ctl (desktopWindowOps, webview_fallback.go), proving the
// ut-docs#1382 fix: exit-to-os via the disconnected/fallback HTTP channel
// now records "normal" through SetAppliedMode, so GET /diagnostics'
// current_window_mode no longer sticks at whatever it was before the exit.
// control_test.go's TestControlServer_SetAppliedMode already covers the
// sink (SetAppliedMode itself); this covers the source that, before this
// fix, silently never called it.
//
// Threading discipline matters here and a first draft of this test got it
// wrong (independent review of ut-docs#1382): calling w.Run() on a spawned
// goroutine and the deferred w.Destroy() on the test's own — two different
// goroutines/threads — aborted inside webview_destroy on ~80% of real
// runs. Production's own showWindow never does this: New(), Run() and the
// deferred Destroy() all execute on the one goroutine that creates the
// window, sequentially, with no goroutine hop between them; only Dispatch
// and Terminate (both explicitly documented safe cross-thread) are ever
// called from elsewhere. runDesktopExitToOSChild below follows the exact
// same discipline: New/Run/Destroy stay on that function's own goroutine,
// and the HTTP-driving/polling that exercises the fix runs on a background
// goroutine that only ever calls Terminate() on the window, never Run() or
// Destroy(). testing.T's Fatal/FailNow may only be called from the
// goroutine actually running the test, so the background goroutine
// reports its outcome over a channel instead of touching t directly.
//
// ut-docs#3445: even with that discipline fixed, webview_destroy() itself
// still intermittently SIGABRTs under Xvfb (~10-20% of runs here; the
// original report measured 2/19 and 3/20). Confirmed pre-existing and
// unrelated to any till code: reproduces identically on a clean `main`
// with no navigation, no JS and nothing else running. Every captured crash
// log shows the fix's own proof line (windowModeNormalLogLine) ALREADY
// printed before the abort — the crash is strictly in native teardown,
// after the thing this test exists to verify has already happened.
// Candidate fixes tried and empirically ruled out (40-iteration loops
// each, rate unchanged within noise): running a window manager
// (matchbox) under Xvfb; JSC_SIGNAL_FOR_GC=0 (made it crash earlier and
// deterministically instead — JSC treats 0 as a real signal number, not
// "disable"); WEBKIT_FORCE_SANDBOX=0; a settle delay (300ms and 1000ms)
// between the fix landing and Destroy. None of these moved the rate
// outside binomial noise for n=40, so this is treated as an upstream
// WebKitGTK/JSC native-teardown race under a minimal Xvfb display, not
// something fixable from the Go side — matching the issue's own
// non-goal ("the real desktop shell under a real display isn't known to
// be affected").
//
// Fix: isolate the risky part in a subprocess (same os.Args[0] re-exec
// pattern as internal/logging's TestFatalfExitsProcess) so a native abort
// can never fail `go test` itself. A subprocess that aborts AFTER already
// printing windowModeNormalLogLine proved the fix works before it crashed
// in code this test isn't about — logged and retried, bounded, rather
// than failed. A subprocess that fails WITHOUT that line (the assertion
// itself failed, or it crashed before getting there) fails the test for
// real, immediately, no retry.
func TestDesktopWindowOps_ExitToOSRecordsAppliedMode(t *testing.T) {
	if os.Getenv("UT_TEST_DESKTOP_EXIT_CHILD") == "1" {
		runDesktopExitToOSChild(t)
		return
	}

	const maxAttempts = 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, err, timedOut := runDesktopExitToOSSubprocess()
		if timedOut {
			t.Fatalf("subprocess hung past %s and was killed (attempt %d/%d) — not ut-docs#3445's crash, which is fast; err=%v\n%s", desktopExitSubprocessTimeout, attempt, maxAttempts, err, out)
		}
		if err == nil {
			if strings.Contains(string(out), "--- SKIP:") {
				t.Skip("no native webview available in this environment")
			}
			return
		}
		if strings.Contains(string(out), "--- FAIL:") {
			t.Fatalf("subprocess assertion failed (attempt %d/%d): %v\n%s", attempt, maxAttempts, err, out)
		}
		proved := strings.Contains(string(out), windowModeNormalLogLine)
		if !proved || !crashedWithSIGABRT(out) {
			t.Fatalf("subprocess failed before proving the fix (attempt %d/%d, proved=%v): %v\n%s", attempt, maxAttempts, proved, err, out)
		}
		t.Logf("attempt %d/%d: native webview teardown SIGABRTed after the fix was already proven (ut-docs#3445, known Xvfb/WebKitGTK race) — retrying\n%s", attempt, maxAttempts, out)
	}
	t.Fatalf("native webview teardown aborted on all %d attempts, every time after proving the fix — a worse regression than ut-docs#3445's known flake, not the flake itself", maxAttempts)
}

// desktopExitSubprocessTimeout bounds one child run. A healthy run takes
// well under a second and the child's own driveExitToOS poll gives up
// after 5s, so anything near this is a wedged GTK/WebKit loop, not the
// ut-docs#3445 abort (which is fast). Without it a hung child would block
// CombinedOutput until `go test`'s own timeout (10m by default) panicked
// the PARENT and orphaned the child — and WebKit's helper processes, which
// inherit the child's stdout pipe (review of ut-docs#3445).
const desktopExitSubprocessTimeout = 60 * time.Second

// runDesktopExitToOSSubprocess re-execs this test binary with only the one
// test selected (anchored, so TestControlServer_ExitToOSRoutesToOps and any
// future sibling can't be swept in) and the child marker set. It returns
// the combined output, the wait error, and whether the child was killed
// for exceeding desktopExitSubprocessTimeout. -test.v is what makes the
// child print "--- SKIP:"/"--- FAIL:" lines for the caller to inspect.
func runDesktopExitToOSSubprocess() (out []byte, err error, timedOut bool) {
	ctx, cancel := context.WithTimeout(context.Background(), desktopExitSubprocessTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDesktopWindowOps_ExitToOSRecordsAppliedMode$", "-test.v")
	cmd.Env = append(os.Environ(), "UT_TEST_DESKTOP_EXIT_CHILD=1")
	// WebKitGTK forks helper processes (WebKitNetworkProcess, WebKitWebProcess)
	// that inherit the child's stdout/stderr. If one outlived an aborted child,
	// CombinedOutput would otherwise wait on the pipe for as long as it lived.
	cmd.WaitDelay = 5 * time.Second
	out, err = cmd.CombinedOutput()
	return out, err, errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// crashedWithSIGABRT reports whether out is a Go runtime fatal-signal crash
// dump for SIGABRT arriving during cgo execution — webview_destroy's crash
// shape (ut-docs#3445). The runtime itself prints this banner and calls
// os.Exit(2) (go/src/runtime/signal_unix.go's fatalsignal path); the
// process never becomes to the kernel's WIFSIGNALED/SIGABRT the way a raw,
// uncaught abort() would, so this checks the banner Go always prints for
// it, not the subprocess's wait status.
func crashedWithSIGABRT(out []byte) bool {
	return strings.Contains(string(out), "SIGABRT: abort") &&
		strings.Contains(string(out), "signal arrived during cgo execution")
}

// runDesktopExitToOSChild is the actual test body, run only inside the
// subprocess TestDesktopWindowOps_ExitToOSRecordsAppliedMode spawns — see
// that function's doc comment for why. Skips (not fails) if no native
// window can be created — this file's own build tag already restricts it
// to desktop&&linux, but a Linux CI runner with the tag set and no display
// would otherwise fail on something this test isn't about. CI's
// desktop-shell job never runs `go test -tags desktop` at all (build+vet
// only, see .github/workflows/ci.yml), so this test does not run there
// either way — it exists for a real display, e.g. under Xvfb, or for
// whenever ut-docs#1581's real test pass lands.
func runDesktopExitToOSChild(t *testing.T) {
	w := webview.New(false)
	if w == nil {
		t.Skip("no native webview available in this environment")
	}

	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	defer cs.Close()

	cs.SetOps(desktopWindowOps(w, cs))

	done := make(chan error, 1)
	go func() {
		defer w.Terminate() // documented safe from a background goroutine
		done <- driveExitToOS(cs.Addr(), cs.Token())
	}()

	// Run() and Destroy() stay on this goroutine — see the doc comment
	// above for why that's load-bearing, not stylistic.
	w.Run()
	w.Destroy()

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// driveExitToOS is the plain net/http half of the test above, deliberately
// free of *testing.T: it runs on a background goroutine, and testing.T's
// Fatal/FailNow may only be called from the goroutine running the test
// function itself (control_test.go's authedPost/authedGet do call
// t.Fatalf internally, which is exactly why they aren't reused here).
func driveExitToOS(addr, token string) error {
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/exit-to-os", strings.NewReader(""))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(controlTokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST /exit-to-os: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("POST /exit-to-os = %d, want 204", resp.StatusCode)
	}

	// ExitToOS's own Dispatch is fire-and-forget (see webview_fallback.go's
	// comment on this), so the mode change lands asynchronously on the GTK
	// main loop w.Run() is pumping — poll rather than assert once.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mode, err := currentWindowMode(addr, token)
		if err != nil {
			return err
		}
		if mode == "normal" {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New(`current_window_mode never became "normal" after POST /exit-to-os on the fallback path`)
}

func currentWindowMode(addr, token string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/diagnostics", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(controlTokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET /diagnostics: %w", err)
	}
	defer resp.Body.Close()
	var body diagnosticsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode /diagnostics body: %w", err)
	}
	return body.Data.CurrentWindowMode, nil
}
