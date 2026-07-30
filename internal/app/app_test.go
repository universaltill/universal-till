package app

import (
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
