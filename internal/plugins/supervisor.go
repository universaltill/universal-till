package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Supervisor manages plugin process lifecycle
type Supervisor struct {
	db        *sql.DB
	mu        sync.RWMutex
	processes map[string]*PluginProcess // key = plugin_id
	// procWg joins the monitorProcess goroutines (ut-docs#380): Add(1)
	// happens immediately before every `go s.monitorProcess(...)` — both
	// call sites already hold s.mu, which is the same lock Shutdown flips
	// stopped/cancel under, so an Add can never race a Wait that already
	// observed zero WHILE s.mu is held across both the Add and the check of
	// closed below. Done() is monitorProcess's first-registered defer, so
	// it fires only after the goroutine's whole body (including its own
	// s.mu section and audit writes) has finished — which is what lets
	// Shutdown wait on this WITHOUT holding s.mu (see closed's doc for the
	// case s.mu alone doesn't cover: a StartPlugin arriving AFTER shutdown
	// has already released s.mu and started procWg.Wait()).
	procWg sync.WaitGroup
	// closed is set once, under s.mu, at the top of shutdown's critical
	// section — before s.mu is released for the procWg.Wait() below. Every
	// StartPlugin call checks it under the SAME s.mu and refuses to spawn a
	// new monitorProcess if set. Without this (mirroring WasmRuntime's
	// identical closed guard), a StartPlugin racing in after shutdown's
	// s.mu.Unlock() would procWg.Add(1) from a counter that may already be
	// zero and being Waited on — the classic WaitGroup "Add after Wait
	// observed zero" misuse, which panics or simply leaves the new monitor
	// unjoined.
	closed bool
}

// PluginProcess represents a running plugin process
type PluginProcess struct {
	PluginID      string
	Cmd           *exec.Cmd
	StartedAt     time.Time
	RestartCount  int
	RestartPolicy RestartPolicy
	HealthCheck   HealthCheckConfig
	cancel        context.CancelFunc
	stopped       bool
}

// RestartPolicy defines how a plugin should be restarted
type RestartPolicy struct {
	Enabled        bool
	MaxRestarts    int           // Max restarts within window
	RestartWindow  time.Duration // Time window for restart count
	BackoffInitial time.Duration // Initial backoff delay
	BackoffMax     time.Duration // Maximum backoff delay
}

// HealthCheckConfig defines health check parameters
type HealthCheckConfig struct {
	Enabled  bool
	Interval time.Duration
	Timeout  time.Duration
	Endpoint string // HTTP endpoint to check (e.g., "http://localhost:8080/health")
}

// NewSupervisor creates a new plugin supervisor
func NewSupervisor(db *sql.DB) *Supervisor {
	return &Supervisor{
		db:        db,
		processes: make(map[string]*PluginProcess),
	}
}

// StartPlugin launches a plugin process
func (s *Supervisor) StartPlugin(ctx context.Context, pluginID, entrypoint string, args []string, policy RestartPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Refuse after Shutdown (see closed's doc): once shutdown has released
	// s.mu to wait on procWg, a new process's monitorProcess goroutine must
	// not register on it.
	if s.closed {
		return fmt.Errorf("plugin %s: supervisor is shut down", pluginID)
	}

	// Check if already running
	if proc, exists := s.processes[pluginID]; exists && !proc.stopped {
		return fmt.Errorf("plugin %s is already running", pluginID)
	}

	// Create process context
	procCtx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(procCtx, entrypoint, args...)
	proc := &PluginProcess{
		PluginID:      pluginID,
		Cmd:           cmd,
		StartedAt:     time.Now(),
		RestartPolicy: policy,
		cancel:        cancel,
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start plugin process: %w", err)
	}

	s.processes[pluginID] = proc

	// Audit the start
	if err := s.auditLifecycle(ctx, pluginID, "plugin_started", ""); err != nil {
		fmt.Printf("warning: failed to audit plugin start: %v\n", err)
	}

	// Monitor process in background (Add under s.mu — see procWg's doc)
	s.procWg.Add(1)
	go s.monitorProcess(procCtx, proc)

	return nil
}

// StopPlugin stops a running plugin process
func (s *Supervisor) StopPlugin(ctx context.Context, pluginID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	proc, exists := s.processes[pluginID]
	if !exists {
		return fmt.Errorf("plugin %s is not running", pluginID)
	}

	proc.stopped = true
	proc.cancel()

	// Audit the stop
	if err := s.auditLifecycle(ctx, pluginID, "plugin_stopped", ""); err != nil {
		fmt.Printf("warning: failed to audit plugin stop: %v\n", err)
	}

	delete(s.processes, pluginID)
	return nil
}

// RestartPlugin restarts a running plugin process
func (s *Supervisor) RestartPlugin(ctx context.Context, pluginID string) error {
	s.mu.RLock()
	proc, exists := s.processes[pluginID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("plugin %s is not running", pluginID)
	}

	// Get original command info
	entrypoint := proc.Cmd.Path
	args := proc.Cmd.Args[1:] // Exclude the command itself
	policy := proc.RestartPolicy

	// Stop the process
	if err := s.StopPlugin(ctx, pluginID); err != nil {
		return fmt.Errorf("stop plugin for restart: %w", err)
	}

	// Wait a moment before restart
	time.Sleep(time.Second)

	// Start again
	return s.StartPlugin(ctx, pluginID, entrypoint, args, policy)
}

// monitorProcess watches a plugin process and handles restarts
func (s *Supervisor) monitorProcess(ctx context.Context, proc *PluginProcess) {
	// Registered FIRST so it runs LAST (defers are LIFO): Done only fires
	// after the deferred s.mu.Unlock below, i.e. after this goroutine is
	// completely finished — audit writes included. Done itself never needs
	// s.mu, so Shutdown can procWg.Wait() lock-free without deadlocking
	// against the s.mu.Lock() this goroutine takes after Cmd.Wait returns.
	defer s.procWg.Done()

	err := proc.Cmd.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	if proc.stopped {
		// Intentional stop, no restart
		return
	}

	// Process crashed
	details := ""
	if err != nil {
		details = fmt.Sprintf("error=%s", err.Error())
	}

	if err := s.auditLifecycle(ctx, proc.PluginID, "plugin_crashed", details); err != nil {
		fmt.Printf("warning: failed to audit plugin crash: %v\n", err)
	}

	// Check restart policy
	if !proc.RestartPolicy.Enabled {
		return
	}

	if proc.RestartCount >= proc.RestartPolicy.MaxRestarts {
		details := fmt.Sprintf("max_restarts=%d reached", proc.RestartPolicy.MaxRestarts)
		if err := s.auditLifecycle(ctx, proc.PluginID, "plugin_restart_limit", details); err != nil {
			fmt.Printf("warning: failed to audit restart limit: %v\n", err)
		}
		return
	}

	// Calculate backoff
	backoff := proc.RestartPolicy.BackoffInitial * time.Duration(1<<proc.RestartCount)
	if backoff > proc.RestartPolicy.BackoffMax {
		backoff = proc.RestartPolicy.BackoffMax
	}

	time.Sleep(backoff)

	// Restart
	proc.RestartCount++
	entrypoint := proc.Cmd.Path
	args := proc.Cmd.Args[1:]

	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, entrypoint, args...)

	if err := cmd.Start(); err != nil {
		cancel()
		details := fmt.Sprintf("restart_failed: %s", err.Error())
		if err := s.auditLifecycle(ctx, proc.PluginID, "plugin_restart_failed", details); err != nil {
			fmt.Printf("warning: failed to audit restart failure: %v\n", err)
		}
		return
	}

	proc.Cmd = cmd
	proc.cancel = cancel
	proc.StartedAt = time.Now()

	details = fmt.Sprintf("restart_count=%d", proc.RestartCount)
	if err := s.auditLifecycle(ctx, proc.PluginID, "plugin_restarted", details); err != nil {
		fmt.Printf("warning: failed to audit plugin restart: %v\n", err)
	}

	// Continue monitoring (Add under s.mu — held by the deferred unlock
	// above — and before this invocation's own Done fires, so the counter
	// never dips to zero between a restart and its new monitor).
	s.procWg.Add(1)
	go s.monitorProcess(procCtx, proc)
}

// IsRunning checks if a plugin is currently running
func (s *Supervisor) IsRunning(pluginID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	proc, exists := s.processes[pluginID]
	return exists && !proc.stopped
}

// GetProcessInfo returns information about a running plugin process
func (s *Supervisor) GetProcessInfo(pluginID string) (*ProcessInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	proc, exists := s.processes[pluginID]
	if !exists {
		return nil, fmt.Errorf("plugin %s is not running", pluginID)
	}

	return &ProcessInfo{
		PluginID:     proc.PluginID,
		PID:          proc.Cmd.Process.Pid,
		StartedAt:    proc.StartedAt,
		RestartCount: proc.RestartCount,
		Uptime:       time.Since(proc.StartedAt),
	}, nil
}

// ProcessInfo contains information about a running plugin process
type ProcessInfo struct {
	PluginID     string
	PID          int
	StartedAt    time.Time
	RestartCount int
	Uptime       time.Duration
}

// ListRunning returns a list of all running plugins
func (s *Supervisor) ListRunning() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	running := make([]string, 0, len(s.processes))
	for pluginID, proc := range s.processes {
		if !proc.stopped {
			running = append(running, pluginID)
		}
	}

	return running
}

// pluginShutdownDrainTimeout bounds how long Shutdown waits for the
// monitorProcess goroutines to exit after their processes are cancelled.
// server.Start wraps the whole supervisor.Shutdown call in a 5-second
// shutdown context shared with srv.Shutdown, called first — so that context
// can already be most of the way to its own deadline by the time this runs.
// shutdown's select also watches ctx.Done() (not just this timeout) so a
// mostly-spent shutdownCtx doesn't let the two waits add up past the 5s the
// caller actually budgeted; this constant is the FALLBACK bound for a fresh
// ctx with time still on it, not a guarantee this always finishes under it.
const pluginShutdownDrainTimeout = 4 * time.Second

// Shutdown stops all running plugin processes and waits — bounded — for
// every monitorProcess goroutine to actually exit before returning
// (ut-docs#380). server.Start invokes this from a wg-joined goroutine, so
// once this returns (whether by a clean join or by the bound/ctx giving up
// and logging loudly), app.Run's drain has done everything it can to keep a
// monitor goroutine from racing database.Close() with an audit write.
func (s *Supervisor) Shutdown(ctx context.Context) error {
	return s.shutdown(ctx, pluginShutdownDrainTimeout)
}

// shutdown is Shutdown with the drain bound as a parameter, so tests can
// exercise the timeout branch without a real multi-second wait (same reason
// app's drainBackgroundServices takes its timeout as a parameter).
func (s *Supervisor) shutdown(ctx context.Context, drainTimeout time.Duration) error {
	s.mu.Lock()
	s.closed = true // refuse new StartPlugin calls from here on (see closed's doc)
	for pluginID, proc := range s.processes {
		proc.stopped = true
		proc.cancel()

		if err := s.auditLifecycle(ctx, pluginID, "plugin_shutdown", ""); err != nil {
			fmt.Printf("warning: failed to audit plugin shutdown: %v\n", err)
		}
	}
	s.processes = make(map[string]*PluginProcess)
	// NOT deferred to function end: the wait below must run without s.mu,
	// or it would deadlock against monitorProcess's own s.mu.Lock() after
	// Cmd.Wait returns.
	s.mu.Unlock()

	// Bounded join, mirroring app.drainBackgroundServices: a monitor
	// goroutine that never exits (a reap that never completes) is a bug to
	// fix, not a reason to never shut down — after the bound, log loudly
	// and return anyway. The inner Wait goroutine is intentionally leaked
	// on timeout (harmless; it exits whenever the wedged monitor does).
	// ctx.Done() is watched too, and separately logged: server.Start's
	// shutdownCtx may already be mostly spent by srv.Shutdown before this
	// runs, and without this branch drainTimeout would run to its own full
	// length on top of that instead of respecting the caller's remaining
	// budget.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.procWg.Wait()
	}()
	select {
	case <-done:
	case <-ctx.Done():
		logging.L().Errorf("plugin supervisor shutdown: caller context done (%v) before monitor goroutines finished — continuing shutdown anyway", ctx.Err())
	case <-time.After(drainTimeout):
		logging.L().Errorf("plugin supervisor shutdown: monitor goroutines still running %s after stop — continuing shutdown anyway", drainTimeout)
	}
	return nil
}

// AutoStartPlugins starts all active plugins at boot (T023)
func (s *Supervisor) AutoStartPlugins(ctx context.Context) error {
	repo := data.NewPluginRepo(s.db)
	rows, err := repo.ListAutoStartPlugins(ctx)
	if err != nil {
		return fmt.Errorf("failed to query plugins: %w", err)
	}

	var started, failed []string
	for _, row := range rows {
		pluginID := row.ID
		entrypoint := row.Entrypoint
		runtime := row.Runtime

		// Skip non-executable runtimes for now
		if runtime != "go" && runtime != "native" {
			continue
		}

		// Default restart policy for auto-started plugins
		policy := RestartPolicy{
			Enabled:        true,
			MaxRestarts:    3,
			RestartWindow:  5 * time.Minute,
			BackoffInitial: 1 * time.Second,
			BackoffMax:     30 * time.Second,
		}

		// Start the plugin
		if err := s.StartPlugin(ctx, pluginID, entrypoint, []string{}, policy); err != nil {
			fmt.Printf("warning: failed to auto-start plugin %s: %v\n", pluginID, err)
			failed = append(failed, pluginID)
			continue
		}

		started = append(started, pluginID)
	}

	fmt.Printf("Auto-started %d plugins (%d failed)\n", len(started), len(failed))
	if len(failed) > 0 {
		fmt.Printf("Failed to start: %v\n", failed)
	}

	return nil
}

// auditLifecycle logs plugin lifecycle events to audit_log
func (s *Supervisor) auditLifecycle(ctx context.Context, pluginID, action, details string) error {
	return data.NewPluginRepo(s.db).InsertAuditRaw(ctx, nil, action, "plugin", pluginID, details, time.Now())
}
