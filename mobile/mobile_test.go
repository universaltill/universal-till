package mobile

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// mobileTestEnv points the till at an isolated temp data dir and disables
// auth (no PIN session needed to hit /healthz) for every test in this file —
// mirrors how other live-verification runs in this repo set up a throwaway
// till instance.
// Plain t.TempDir() is safe here: app.Run now joins every background
// goroutine it starts (directly or via server.Start) before returning, so by
// the time Stop() returns (it blocks on app.Run's return), nothing is still
// writing into the data dir. Before that fix, a straggler could still be
// mid-write for a moment after Stop() returned, and t.TempDir()'s own
// RemoveAll cleanup raced it, flaking with "TempDir RemoveAll cleanup:
// directory not empty" (main CI 2026-07-30, run 30531313594) — worked around
// there with a manual dir + retrying removal. That workaround is gone: a
// single, un-retried RemoveAll succeeding right after Stop() is now the
// actual proof the join works, not a tolerated flake. Cleanup order (LIFO):
// Stop runs first, then t.TempDir()'s own (single-attempt) removal.
func mobileTestEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_ENV_FILE", t.TempDir()+"/does-not-exist.env") // don't pick up a stray local pos.env
	dataDir := t.TempDir()
	t.Cleanup(Stop)
	return dataDir
}

func TestStartAndStop_RealServerBoots(t *testing.T) {
	dataDir := mobileTestEnv(t)

	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if addr == "" {
		t.Fatal("expected a non-empty address")
	}
	if !IsRunning() {
		t.Fatal("expected IsRunning() to be true after a successful Start")
	}

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
	}

	// Stop() blocks until the server has FULLY torn down (not just until
	// the shutdown signal was sent) — so a single check right after it
	// returns is deterministic, not a race against an async shutdown.
	Stop()
	if IsRunning() {
		t.Fatal("expected IsRunning() to be false after Stop")
	}
	if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
		t.Fatalf("server at %s still answering immediately after Stop returned", addr)
	}
}

func TestStart_IdempotentWhileRunning(t *testing.T) {
	dataDir := mobileTestEnv(t)

	addr1, err := Start(dataDir)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// A second Start call with the SAME dataDir while already running must
	// NOT try to boot a second server (which would fail: the first already
	// holds the DB and whatever port it bound) — it should just hand back
	// the same address.
	addr2, err := Start(dataDir)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if addr1 != addr2 {
		t.Fatalf("second Start returned %q, want the same address %q", addr2, addr1)
	}
}

func TestStart_DifferentDataDirWhileRunningErrors(t *testing.T) {
	dataDir := mobileTestEnv(t)

	if _, err := Start(dataDir); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// A second Start with a DIFFERENT dataDir while already running must
	// fail loudly, not silently ignore the request and keep serving the
	// first dataDir under a caller's mistaken belief it switched. (This
	// other dir sees no live server write, so an inline t.TempDir is fine.)
	if _, err := Start(t.TempDir()); err == nil {
		t.Fatal("expected Start against a different dataDir while running to error")
	}
}

func TestStart_FastFailWhenServerDiesImmediately(t *testing.T) {
	_ = mobileTestEnv(t) // Start below never boots a server; env setup only

	// A regular FILE where the data dir should be a directory makes
	// db.Open fail immediately (DBPath = filepath.Join(dataDir,
	// "unitill-pos.db") can never be created under a non-directory) —
	// exercises waitUntilReady's fast-fail path (app.Run's goroutine
	// returns an error before the /healthz poll ever succeeds) rather
	// than waiting out the full 10s timeout.
	notADir := filepath.Join(t.TempDir(), "this-is-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed a non-directory dataDir: %v", err)
	}

	_, err := Start(notADir)
	if err == nil {
		t.Fatal("expected Start to fail fast when the server can't open its database")
	}
	if IsRunning() {
		t.Fatal("expected IsRunning() to be false after a failed Start")
	}
}

// A server that dies on its own (e.g. a listener error) without Stop()
// ever being called must not leave IsRunning()/Start() reporting stale
// success — this is the "abandoned runErrCh" gap flagged in code review:
// cancelling the instance's own context directly (as a real crash would,
// from the caller's point of view) simulates that, without needing to
// engineer an actual server-internal failure.
func TestIsRunning_DetectsServerDiedWithoutStop(t *testing.T) {
	dataDir := mobileTestEnv(t)

	if _, err := Start(dataDir); err != nil {
		t.Fatalf("Start: %v", err)
	}

	mu.Lock()
	died := inst
	mu.Unlock()
	died.cancel()
	<-died.done // wait for the real teardown, same as Stop() would

	if IsRunning() {
		t.Fatal("expected IsRunning() to detect the dead instance and report false")
	}

	// A subsequent Start with the same dataDir must actually restart the
	// server, not return the old (now-dead) address as if nothing happened.
	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start after an unobserved crash: %v", err)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz on the restarted server: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
	}
}

func TestStop_SafeWhenNotRunning(t *testing.T) {
	_ = mobileTestEnv(t)
	Stop() // must not panic
	if IsRunning() {
		t.Fatal("IsRunning() should be false when Start was never called")
	}
}
