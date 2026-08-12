package plugins

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Supervisor.Shutdown must not return while a monitorProcess goroutine is
// still running (ut-docs#380): app.Run closes the database right after its
// drain, so a monitor still mid-audit-write after Shutdown returned races
// database.Close(). This registers an extra in-flight goroutine on the SAME
// WaitGroup StartPlugin registers real monitors on, and proves Shutdown
// blocks until it (and the real monitor) finish — the old Shutdown returned
// immediately after its cancel loop, never waiting on anything.
func TestSupervisor_Shutdown_WaitsForMonitorProcessGoroutines(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// setupTestDB opens ":memory:", where EVERY pooled connection gets its own
	// private empty database — a second connection would not see the tables the
	// first one created. This test touches db from several goroutines at once
	// (monitorProcess audit writes vs. the test body), so pin the pool to one
	// connection or those goroutines hit "no such table: audit_log".
	db.SetMaxOpenConns(1)
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 30\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	// A real plugin process, so Shutdown exercises its cancel/audit loop and
	// the real monitorProcess goroutine's wg registration end-to-end.
	if err := supervisor.StartPlugin(ctx, "com.test.drain", scriptPath, []string{}, RestartPolicy{Enabled: false}); err != nil {
		t.Fatalf("StartPlugin failed: %v", err)
	}

	// Simulate a monitor goroutine still finishing its post-Wait work (e.g.
	// an audit write) when Shutdown's cancel loop has already run: held open
	// until the test releases it.
	release := make(chan struct{})
	supervisor.wg.Add(1)
	go func() {
		defer supervisor.wg.Done()
		<-release
	}()

	done := make(chan error, 1)
	go func() { done <- supervisor.Shutdown(ctx) }()

	// Shutdown must be blocked on the in-flight goroutine, not returning.
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned (%v) while a monitor goroutine was still running", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return after the last monitor goroutine finished")
	}

	// The REAL monitorProcess goroutine must be gone too, not just our
	// simulated one — Shutdown only returned once the whole group drained.
	waitForNoMonitorGoroutines(t, 2*time.Second)
}

// A crash-restart cycle re-registers the respawned monitor on the WaitGroup
// (the Add happens before the previous monitor's deferred Done fires), so the
// counter never goes negative (panic) and Shutdown after the restart-limit
// audit still drains cleanly and promptly.
func TestSupervisor_Shutdown_AfterCrashRestartCycleDrainsCleanly(t *testing.T) {
	db := setupTestDB(t)
	// t.Cleanup, not defer: registered before the Shutdown drain below, so
	// (LIFO) it runs AFTER it — db must not close until the crash-loop
	// monitor has actually been told to stop (see the drain note below).
	t.Cleanup(func() { db.Close() })
	// See the note in the test above: ":memory:" is per-connection, and the
	// crash/restart monitors audit concurrently with this test's polling query.
	db.SetMaxOpenConns(1)
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "crash.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	policy := RestartPolicy{
		Enabled:        true,
		MaxRestarts:    1,
		RestartWindow:  time.Minute,
		BackoffInitial: 10 * time.Millisecond,
		BackoffMax:     10 * time.Millisecond,
	}
	if err := supervisor.StartPlugin(ctx, "com.test.crashloop", scriptPath, []string{}, policy); err != nil {
		t.Fatalf("StartPlugin failed: %v", err)
	}
	// Drain via t.Cleanup, registered right after the crash-loop starts, so
	// it still runs even if the polling loop below hits its deadline and
	// t.Fatal's (ut-docs#509): without this, a t.Fatal here unwound the test
	// via runtime.Goexit before the explicit Shutdown call further down was
	// ever reached, leaking the still-restarting monitor goroutine into
	// whichever test runs next in the same `go test` process — closed-DB
	// audit writes from the leaked crash loop then starved the shared
	// logger's mutex for minutes. Calling Shutdown twice (once here, once
	// explicitly below on the success path) is safe: the second call finds
	// an already-empty process map and an already-drained WaitGroup.
	t.Cleanup(func() { _ = supervisor.Shutdown(ctx) })

	// Wait for the full cycle: crash -> restart -> crash -> restart-limit.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM audit_log WHERE action = 'plugin_restart_limit'`).Scan(&n); err != nil {
			t.Fatalf("query audit_log: %v", err)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin never hit its restart limit")
		}
		time.Sleep(10 * time.Millisecond)
	}

	done := make(chan error, 1)
	go func() { done <- supervisor.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown hung after a crash-restart cycle — WaitGroup accounting broken")
	}
	waitForNoMonitorGoroutines(t, 2*time.Second)
}

// A wedged monitor (a plugin process that never dies, or a monitor stuck on a
// blocked audit write) must not hang shutdown forever: once the caller's ctx
// expires, Shutdown returns anyway — and logs LOUDLY that it gave up, same as
// app.drainBackgroundServices does.
func TestSupervisor_Shutdown_TimesOutLoudlyOnWedgedMonitor(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)

	// Simulate a wedged monitor goroutine: registered but never finishing
	// (until test cleanup lets the leaked waiter goroutine exit).
	supervisor.wg.Add(1)
	t.Cleanup(supervisor.wg.Done)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- supervisor.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown returned %v, want nil even on timeout (a wedged plugin must not fail shutdown)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown never returned despite its ctx expiring")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("Shutdown returned after %s, before its ctx deadline — it never actually waited", elapsed)
	}

	found := false
	for _, p := range logging.Recent() {
		if p.Level == "ERROR" && p.At.After(start.Add(-time.Second)) &&
			strings.Contains(p.Msg, "plugin monitor goroutines still running") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a loud ERROR log about monitor goroutines still running at shutdown timeout")
	}
}

// A plugin sleeping through its restart backoff must not force Shutdown to
// block behind that backoff just to acquire s.mu (ut-docs#502): before this
// fix, monitorProcess held s.mu across its entire restart path, including
// the backoff time.Sleep, so Shutdown's own s.mu.Lock() at the top of its
// cancel loop queued behind it for however long the backoff was — up to
// BackoffMax (30s in production, per AutoStartPlugins' policy), well past
// server.Start's 5s shutdownCtx budget, defeating the ctx-bounded wg.Wait
// ut-docs#380 added (Shutdown never even reached it).
func TestSupervisor_Shutdown_DoesNotBlockOnMonitorProcessRestartBackoff(t *testing.T) {
	db := setupTestDB(t)
	// t.Cleanup, not defer — see the sibling crash-restart test above for why:
	// registered before the Shutdown drain below so it runs after it.
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "crash.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	// A long backoff, matching production's AutoStartPlugins policy (30s) —
	// the whole point of this test is proving Shutdown doesn't block behind it.
	policy := RestartPolicy{
		Enabled:        true,
		MaxRestarts:    5,
		RestartWindow:  time.Minute,
		BackoffInitial: 30 * time.Second,
		BackoffMax:     30 * time.Second,
	}
	if err := supervisor.StartPlugin(ctx, "com.test.backoffblock", scriptPath, []string{}, policy); err != nil {
		t.Fatalf("StartPlugin failed: %v", err)
	}
	// Drain via t.Cleanup — see ut-docs#509 and the sibling crash-restart
	// test's comment above: without this, a t.Fatal in the polling loop
	// below (a 30s-backoff crash loop, here) leaks the still-sleeping
	// monitor goroutine into whichever test runs next.
	t.Cleanup(func() { _ = supervisor.Shutdown(ctx) })

	// Wait for the crash to be audited: monitorProcess has released s.mu and
	// is now inside (or about to enter) its 30s backoff sleep.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM audit_log WHERE action = 'plugin_crashed'`).Scan(&n); err != nil {
			t.Fatalf("query audit_log: %v", err)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin never crashed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give monitorProcess a moment to actually reach (and release s.mu into)
	// its backoff sleep, past the crash-audit write above.
	time.Sleep(50 * time.Millisecond)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- supervisor.Shutdown(shutdownCtx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return — still blocked, likely on s.mu held through the restart backoff sleep")
	}
	// Well under shutdownCtx's own 2s budget: Shutdown must DRAIN here — the
	// ctx cancellation wakes the sleeping monitor immediately — not merely
	// give up loudly once its deadline expires. A 3s bound would be satisfied
	// by the give-up path too (measured: 2.00s), so it left the ctx.Done()
	// half of the fix untested; the drain path measures ~0.1s.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Shutdown took %s — should wake the backoff sleep via ctx and drain, not sit out its whole ctx budget", elapsed)
	}
	// And it really drained: the monitor is gone, not still sleeping out the
	// rest of its 30s backoff while the caller goes on to close the database
	// (the whole point of ut-docs#380's join).
	waitForNoMonitorGoroutines(t, 2*time.Second)
}

// waitForNoMonitorGoroutines polls the runtime's goroutine stacks until no
// monitorProcess frame remains (a just-Done()d goroutine can still be
// unwinding for an instant) or the bound elapses.
func waitForNoMonitorGoroutines(t *testing.T, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), "monitorProcess") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("monitorProcess goroutine still running %s after Shutdown returned:\n%s", within, buf[:n])
		}
		time.Sleep(10 * time.Millisecond)
	}
}
