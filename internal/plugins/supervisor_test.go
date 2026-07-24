package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSupervisor_StartStopPlugin(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	// Create a simple test script that sleeps
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")
	script := "#!/bin/sh\nsleep 10\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	policy := RestartPolicy{
		Enabled:        false,
		MaxRestarts:    3,
		RestartWindow:  time.Minute,
		BackoffInitial: time.Second,
		BackoffMax:     time.Minute,
	}

	// Start plugin
	pluginID := "com.test.process"
	if err := supervisor.StartPlugin(ctx, pluginID, scriptPath, []string{}, policy); err != nil {
		t.Fatalf("StartPlugin failed: %v", err)
	}

	// Verify running
	if !supervisor.IsRunning(pluginID) {
		t.Error("expected plugin to be running")
	}

	// Get process info
	info, err := supervisor.GetProcessInfo(pluginID)
	if err != nil {
		t.Fatalf("GetProcessInfo failed: %v", err)
	}

	if info.PluginID != pluginID {
		t.Errorf("expected plugin_id %s, got %s", pluginID, info.PluginID)
	}
	if info.PID <= 0 {
		t.Errorf("expected valid PID, got %d", info.PID)
	}

	// Stop plugin
	if err := supervisor.StopPlugin(ctx, pluginID); err != nil {
		t.Fatalf("StopPlugin failed: %v", err)
	}

	// Verify stopped
	if supervisor.IsRunning(pluginID) {
		t.Error("expected plugin to be stopped")
	}

	// Verify audit log
	var action string
	err = db.QueryRowContext(ctx, `
		SELECT action FROM audit_log WHERE action = 'plugin_started'
	`).Scan(&action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
}

func TestSupervisor_StartAlreadyRunning(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	pluginID := "com.test.duplicate"
	policy := RestartPolicy{Enabled: false}

	// Start first time
	if err := supervisor.StartPlugin(ctx, pluginID, scriptPath, []string{}, policy); err != nil {
		t.Fatalf("StartPlugin failed: %v", err)
	}
	defer supervisor.StopPlugin(ctx, pluginID)

	// Try to start again - should fail
	err := supervisor.StartPlugin(ctx, pluginID, scriptPath, []string{}, policy)
	if err == nil {
		t.Fatal("expected error when starting already-running plugin, got nil")
	}
}

func TestSupervisor_ListRunning(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	policy := RestartPolicy{Enabled: false}

	// Start multiple plugins
	plugins := []string{"plugin.a", "plugin.b", "plugin.c"}
	for _, id := range plugins {
		if err := supervisor.StartPlugin(ctx, id, scriptPath, []string{}, policy); err != nil {
			t.Fatalf("StartPlugin %s failed: %v", id, err)
		}
		defer supervisor.StopPlugin(ctx, id)
	}

	// List running
	running := supervisor.ListRunning()
	if len(running) != 3 {
		t.Errorf("expected 3 running plugins, got %d", len(running))
	}
}

func TestSupervisor_Shutdown(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")
	script := "#!/bin/sh\nsleep 10\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	policy := RestartPolicy{Enabled: false}

	// Start plugins
	plugins := []string{"plugin.1", "plugin.2"}
	for _, id := range plugins {
		if err := supervisor.StartPlugin(ctx, id, scriptPath, []string{}, policy); err != nil {
			t.Fatalf("StartPlugin %s failed: %v", id, err)
		}
	}

	// Shutdown all
	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Verify all stopped
	running := supervisor.ListRunning()
	if len(running) != 0 {
		t.Errorf("expected 0 running plugins after shutdown, got %d", len(running))
	}
}

// AutoStartPlugins starts active, installed process-based plugins left over
// from a previous run (T023). WASM plugins (everything shipped today) sync
// through a separate path (WasmRuntime.Sync) and must be skipped here, not
// double-started.
func TestSupervisor_AutoStartPlugins(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")
	script := "#!/bin/sh\nsleep 10\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	ctx := context.Background()
	seed := func(id, runtime string, active int) {
		_, err := db.ExecContext(ctx, `
			INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active)
			VALUES (?, ?, '1.0.0', 'installed', ?, ?, ?)
		`, id, id, scriptPath, runtime, active)
		if err != nil {
			t.Fatalf("seed plugin %s: %v", id, err)
		}
	}
	seed("com.test.native", "native", 1)
	seed("com.test.wasm", "wasm", 1)   // must be skipped, not started as a process
	seed("com.test.inactive", "go", 0) // must be excluded by ListAutoStartPlugins itself

	supervisor := NewSupervisor(db)
	if err := supervisor.AutoStartPlugins(ctx); err != nil {
		t.Fatalf("AutoStartPlugins failed: %v", err)
	}
	defer supervisor.Shutdown(ctx)

	if !supervisor.IsRunning("com.test.native") {
		t.Error("expected native-runtime plugin to be auto-started")
	}
	if supervisor.IsRunning("com.test.wasm") {
		t.Error("wasm-runtime plugin must not be started as a process")
	}
	if supervisor.IsRunning("com.test.inactive") {
		t.Error("inactive plugin must not be auto-started")
	}
	if running := supervisor.ListRunning(); len(running) != 1 {
		t.Errorf("expected exactly 1 running plugin, got %d: %v", len(running), running)
	}
}

func TestSupervisor_GetProcessInfo_NotRunning(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	supervisor := NewSupervisor(db)

	_, err := supervisor.GetProcessInfo("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent plugin, got nil")
	}
}

func TestSupervisor_StopPlugin_NotRunning(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	err := supervisor.StopPlugin(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error when stopping nonexistent plugin, got nil")
	}
}

func TestSupervisor_RestartPlugin(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	supervisor := NewSupervisor(db)
	ctx := context.Background()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")
	script := "#!/bin/sh\nsleep 10\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	policy := RestartPolicy{Enabled: false}
	pluginID := "com.test.restart"

	// Start plugin
	if err := supervisor.StartPlugin(ctx, pluginID, scriptPath, []string{}, policy); err != nil {
		t.Fatalf("StartPlugin failed: %v", err)
	}
	defer supervisor.StopPlugin(ctx, pluginID)

	// Get original PID
	info1, err := supervisor.GetProcessInfo(pluginID)
	if err != nil {
		t.Fatalf("GetProcessInfo failed: %v", err)
	}

	// Restart
	if err := supervisor.RestartPlugin(ctx, pluginID); err != nil {
		t.Fatalf("RestartPlugin failed: %v", err)
	}

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Get new PID - should be different
	info2, err := supervisor.GetProcessInfo(pluginID)
	if err != nil {
		t.Fatalf("GetProcessInfo after restart failed: %v", err)
	}

	if info1.PID == info2.PID {
		t.Error("expected different PID after restart")
	}
}
