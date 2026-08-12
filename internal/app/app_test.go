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
	drainBackgroundServices(&wg, logging.L(), 5*time.Second, "background services")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drainBackgroundServices took %s, want well under the 5s timeout", elapsed)
	}
}

// Run joins a SECOND, independent WaitGroup the same way (deps.AsyncWork,
// ut-docs#513) — this proves drainBackgroundServices behaves identically for
// any caller, not just the wg it happens to be named after, with a distinct
// label plumbed through to the timeout log line. Deliberately as thin as
// TestDrainBackgroundServices_ReturnsAsSoonAsWgIsDone above: the drain
// mechanism itself (return-on-done, timeout-and-log) is already covered by
// that test and TestDrainBackgroundServices_TimesOutAndLogsWhenWgNeverCompletes
// below — this only guards that a second, differently-labelled caller works
// the same way, which is what ut-docs#513 actually adds.
func TestDrainBackgroundServices_WaitsForAsyncWorkLikeAnyOtherWaitGroup(t *testing.T) {
	var asyncWork sync.WaitGroup
	asyncWork.Add(1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		asyncWork.Done()
	}()

	start := time.Now()
	drainBackgroundServices(&asyncWork, logging.L(), 5*time.Second, "async print/kitchen/invoice work")
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
	drainBackgroundServices(&wg, logging.L(), 100*time.Millisecond, "background services")
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

// listenPort feeds the mDNS advertiser's SRV record (ut-docs#264) — it must
// parse a normal ":8080"-style UT_LISTEN_ADDR and fall back sanely rather
// than ever return 0 (mdns.NewMDNSService rejects a zero port outright).
func TestListenPort_ParsesConfiguredPort(t *testing.T) {
	cases := map[string]int{
		":8080":          8080,
		"0.0.0.0:9090":   9090,
		"127.0.0.1:3000": 3000,
		"not-an-address": 8080, // fallback
		"":               8080, // fallback
		":0":             8080, // fallback — a literal 0 is as useless as unparseable
	}
	for addr, want := range cases {
		if got := listenPort(addr); got != want {
			t.Errorf("listenPort(%q) = %d, want %d", addr, got, want)
		}
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
		// Two signals distinguish "a healthy boot that happened to be slow"
		// (a loaded CI runner once took 3.7s of legitimate startup — a plain
		// wall-clock threshold flaked there, 2026-07-31) from the regression
		// this test exists for. A real bgCtx/join regression ALWAYS costs the
		// full drain bound on top of boot time AND logs the drain-timeout
		// error; a slow-but-healthy boot does neither.
		if elapsed := time.Since(start); elapsed >= backgroundDrainTimeout {
			t.Fatalf("Run took %s (>= the %s drain timeout) to return a startup error — "+
				"background goroutines were only stopped by the drain giving up, not a real cancel",
				elapsed, backgroundDrainTimeout)
		}
		for _, p := range logging.Recent() {
			if p.Level == "ERROR" && p.At.After(start) &&
				strings.Contains(p.Msg, "background services still running") {
				t.Fatalf("Run's drain hit its timeout on an early startup error: %s", p.Msg)
			}
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s of an unbindable listen address")
	}
}
