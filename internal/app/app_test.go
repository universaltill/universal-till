package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// drainBackgroundServices must not block past wg reaching zero: a fast
// goroutine finishing well inside the timeout must let Run proceed promptly,
// not wait out the whole bound every time.
func TestDrainBackgroundServices_ReturnsAsSoonAsWgIsDone(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		wg.Done()
	}()

	start := time.Now()
	drainBackgroundServices(&wg, logging.L(), 5*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drainBackgroundServices took %s, want well under the 5s timeout", elapsed)
	}
}

// A service that never joins (ignores ctx cancellation, or a real bug in a
// Start function) must not hang shutdown forever: drainBackgroundServices
// must return once its bound elapses, and log loudly that it gave up rather
// than closing the database silently as if nothing were still running.
func TestDrainBackgroundServices_TimesOutAndLogsWhenWgNeverCompletes(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)          // deliberately never Done() — simulates a wedged background service
	t.Cleanup(wg.Done) // let the real goroutine this spawns eventually finish after the test

	start := time.Now()
	drainBackgroundServices(&wg, logging.L(), 100*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("drainBackgroundServices returned after %s, before its own 100ms timeout elapsed", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("drainBackgroundServices took %s, want close to its 100ms timeout", elapsed)
	}

	found := false
	for _, p := range logging.Recent() {
		if p.Level == "ERROR" && strings.Contains(p.Msg, "background services still running") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected an ERROR log noting background services were still running after timeout")
	}
}

// Run must join its background goroutines on EVERY return path, not just a
// caller-driven ctx cancel. This drives a real boot all the way through
// enroll.Init (config.Init's default marketplace endpoint means a fresh,
// unenrolled till always starts enroll's background registration/signing-key
// loop) and then fails at server.Start's listen step on a deliberately
// unbindable address — an early startup error, with the caller's ctx (never
// cancelled here) still fully live. Before the fix, nothing told enroll's
// loop to stop in this case, so Run's drain would always hit its full 10s
// timeout waiting on a goroutine nothing had cancelled. Asserting Run returns
// in well under that bound is what proves the internally-derived bgCtx (not
// the caller's ctx) is what actually stops it.
func TestRun_JoinsBackgroundGoroutinesOnEarlyServerError(t *testing.T) {
	t.Setenv("UT_DATA_DIR", t.TempDir())
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", "not-a-valid-listen-address") // server.Start fails fast at bind

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Run(context.Background()) }() // ctx never cancelled by this test

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run succeeded despite an unbindable UT_LISTEN_ADDR")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("Run took %s to return a startup error — background goroutines were "+
				"apparently only stopped by the 10s drain timeout, not a real cancel", elapsed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return within 15s of an unbindable listen address")
	}
}
