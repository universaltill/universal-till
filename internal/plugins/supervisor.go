package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// Supervisor manages plugin process lifecycle
type Supervisor struct {
	db        *sql.DB
	mu        sync.RWMutex
	processes map[string]*PluginProcess // key = plugin_id
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

	// Monitor process in background
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

	// Continue monitoring
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

// Shutdown stops all running plugin processes
func (s *Supervisor) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for pluginID, proc := range s.processes {
		proc.stopped = true
		proc.cancel()

		if err := s.auditLifecycle(ctx, pluginID, "plugin_shutdown", ""); err != nil {
			fmt.Printf("warning: failed to audit plugin shutdown: %v\n", err)
		}
	}

	s.processes = make(map[string]*PluginProcess)
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
