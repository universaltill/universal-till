//go:build desktop && linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	webview "github.com/webview/webview_go"
)

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
// called from elsewhere. This test follows the exact same discipline: New/
// Run/Destroy stay on this test function's own goroutine below, and the
// HTTP-driving/polling that exercises the fix runs on a background
// goroutine that only ever calls Terminate() on the window, never Run()
// or Destroy(). testing.T's Fatal/FailNow may only be called from the
// goroutine actually running the test, so the background goroutine
// reports its outcome over a channel instead of touching t directly.
//
// Skips (not fails) if no native window can be created — this file's own
// build tag already restricts it to desktop&&linux, but a Linux CI runner
// with the tag set and no display would otherwise fail on something this
// test isn't about. CI's desktop-shell job never runs `go test -tags
// desktop` at all (build+vet only, see .github/workflows/ci.yml), so this
// test does not run there either way — it exists for a real display, e.g.
// under Xvfb, or for whenever ut-docs#1581's real test pass lands.
func TestDesktopWindowOps_ExitToOSRecordsAppliedMode(t *testing.T) {
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
