package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Shutdown must not return while a monitorProcess goroutine it spawned is
// still alive (ut-docs#380): the goroutine blocks in Cmd.Wait(), then takes
// s.mu and (on the crash path) writes to the audit log — work that must all
// be finished before app.Run's drain lets database.Close() run. The
// observable here is Cmd.ProcessState: it is set by Cmd.Wait() returning, so
// if it is non-nil the moment Shutdown returns, the monitor goroutine
// provably got through its Wait (and, via the WaitGroup join, its whole
// body) first. Before the fix, Shutdown returned immediately after
// cancelling the process context, with the SIGKILL/reap still in flight —
// this assertion caught exactly that.
func TestSupervisor_Shutdown_WaitsForMonitorGoroutines(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 10\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	pluginID := "com.test.drainjoin"
	if err := supervisor.StartPlugin(ctx, pluginID, scriptPath, []string{}, RestartPolicy{Enabled: false}); err != nil {
		t.Fatalf("StartPlugin failed: %v", err)
	}

	// Grab the process record before Shutdown clears the map. The monitor
	// goroutine is still blocked in Cmd.Wait() on the sleeping script here.
	supervisor.mu.RLock()
	proc := supervisor.processes[pluginID]
	supervisor.mu.RUnlock()
	if proc == nil {
		t.Fatal("process record missing after StartPlugin")
	}

	start := time.Now()
	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	// Fast path: the cancelled process dies at once, so the join must
	// complete well inside the shutdown drain bound — a hang here would mean
	// the wait deadlocked against monitorProcess's own s.mu.Lock().
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Shutdown took %s, want well under the drain bound (process dies immediately on cancel)", elapsed)
	}
	// The join itself, asserted immediately — not eventually.
	if proc.Cmd.ProcessState == nil {
		t.Fatal("Shutdown returned while monitorProcess was still blocked in Cmd.Wait — the goroutine was not joined")
	}
}

// A monitorProcess goroutine that never exits (a process Cmd.Wait() never
// reaps — a wedged reap is a bug to fix, not a reason to never shut down)
// must not hang Shutdown forever: the join is bounded, and giving up is
// logged loudly. Same shape as app's
// TestDrainBackgroundServices_TimesOutAndLogsWhenWgNeverCompletes.
func TestSupervisor_Shutdown_TimesOutAndLogsWhenMonitorNeverExits(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	supervisor := NewSupervisor(db)
	supervisor.procWg.Add(1)          // deliberately never Done() — simulates a wedged monitorProcess
	t.Cleanup(supervisor.procWg.Done) // let the internal Wait goroutine finish after the test

	start := time.Now()
	if err := supervisor.shutdown(context.Background(), 100*time.Millisecond); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("shutdown returned after %s, before its own 100ms bound elapsed", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("shutdown took %s, want close to its 100ms bound", elapsed)
	}

	found := false
	for _, p := range logging.Recent() {
		if p.Level == "ERROR" && p.At.After(start) && strings.Contains(p.Msg, "monitor goroutines still running") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected an ERROR log noting monitor goroutines were still running after the bound")
	}
}
