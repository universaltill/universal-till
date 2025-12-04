package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPlugin_Success(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create audit_log table
	_, err := db.Exec(`
		CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			details TEXT,
			created_at TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create audit_log: %v", err)
	}

	tmpDir := t.TempDir()

	// Create test manifest
	manifestPath := filepath.Join(tmpDir, "plugin.json")
	manifestJSON := `{
		"id": "com.test.install",
		"name": "Install Test",
		"version": "1.0.0",
		"entrypoint": "./test",
		"runtime": "go"
	}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Create test binary
	binaryPath := filepath.Join(tmpDir, "test")
	binaryContent := "test binary content"
	if err := os.WriteFile(binaryPath, []byte(binaryContent), 0755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	// Compute expected checksum
	expectedChecksum, err := ComputeSHA256(binaryPath)
	if err != nil {
		t.Fatalf("compute checksum: %v", err)
	}

	opts := InstallOptions{
		InstalledFromURL: "https://example.com/plugin.tar.gz",
		SHA256:           expectedChecksum,
		TrustLevel:       "untrusted",
		Uploader:         "admin",
	}

	ctx := context.Background()
	if err := InstallPlugin(ctx, db, manifestPath, binaryPath, opts); err != nil {
		t.Fatalf("InstallPlugin failed: %v", err)
	}

	// Verify plugin installed
	var pluginID, trustLevel string
	err = db.QueryRowContext(ctx, `
		SELECT id, trust_level FROM plugins WHERE id = 'com.test.install'
	`).Scan(&pluginID, &trustLevel)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}

	if pluginID != "com.test.install" {
		t.Errorf("expected plugin id 'com.test.install', got '%s'", pluginID)
	}
	if trustLevel != "untrusted" {
		t.Errorf("expected trust_level 'untrusted', got '%s'", trustLevel)
	}

	// Verify audit log
	var action, entityID string
	err = db.QueryRowContext(ctx, `
		SELECT action, entity_id FROM audit_log WHERE action = 'plugin_install'
	`).Scan(&action, &entityID)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}

	if entityID != "com.test.install" {
		t.Errorf("expected audit entity_id 'com.test.install', got '%s'", entityID)
	}
}

func TestInstallPlugin_ChecksumMismatch(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tmpDir := t.TempDir()

	manifestPath := filepath.Join(tmpDir, "plugin.json")
	manifestJSON := `{
		"id": "com.test.bad",
		"name": "Bad Checksum",
		"version": "1.0.0",
		"entrypoint": "./test"
	}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	binaryPath := filepath.Join(tmpDir, "test")
	if err := os.WriteFile(binaryPath, []byte("content"), 0755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	opts := InstallOptions{
		SHA256: "wrongchecksumhash",
	}

	ctx := context.Background()
	err := InstallPlugin(ctx, db, manifestPath, binaryPath, opts)
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}

	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected 'checksum mismatch' error, got: %v", err)
	}
}

func TestInstallPlugin_MissingBinary(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tmpDir := t.TempDir()

	manifestPath := filepath.Join(tmpDir, "plugin.json")
	manifestJSON := `{
		"id": "com.test.missing",
		"name": "Missing Binary",
		"version": "1.0.0",
		"entrypoint": "./test"
	}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	binaryPath := filepath.Join(tmpDir, "nonexistent")

	ctx := context.Background()
	err := InstallPlugin(ctx, db, manifestPath, binaryPath, InstallOptions{})
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
}

func TestInstallPlugin_DefaultTrustLevel(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create audit_log table
	_, err := db.Exec(`
		CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			details TEXT,
			created_at TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create audit_log: %v", err)
	}

	tmpDir := t.TempDir()

	manifestPath := filepath.Join(tmpDir, "plugin.json")
	manifestJSON := `{
		"id": "com.test.default",
		"name": "Default Trust",
		"version": "1.0.0",
		"entrypoint": "./test"
	}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	binaryPath := filepath.Join(tmpDir, "test")
	if err := os.WriteFile(binaryPath, []byte("content"), 0755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	ctx := context.Background()
	// No TrustLevel specified in opts
	if err := InstallPlugin(ctx, db, manifestPath, binaryPath, InstallOptions{}); err != nil {
		t.Fatalf("InstallPlugin failed: %v", err)
	}

	var trustLevel string
	err = db.QueryRowContext(ctx, `
		SELECT trust_level FROM plugins WHERE id = 'com.test.default'
	`).Scan(&trustLevel)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}

	if trustLevel != "untrusted" {
		t.Errorf("expected default trust_level 'untrusted', got '%s'", trustLevel)
	}
}

func TestUninstallPlugin(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create audit_log table
	_, err := db.Exec(`
		CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			details TEXT,
			created_at TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create audit_log: %v", err)
	}

	ctx := context.Background()

	// Insert test plugin
	manifest := &Manifest{
		ID:         "com.test.uninstall",
		Name:       "Uninstall Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Verify plugin exists
	var count int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugins WHERE id = ?`, manifest.ID).Scan(&count)
	if err != nil {
		t.Fatalf("query plugins: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 plugin, got %d", count)
	}

	// Uninstall
	if err := UninstallPlugin(ctx, db, manifest.ID); err != nil {
		t.Fatalf("UninstallPlugin failed: %v", err)
	}

	// Verify plugin removed
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugins WHERE id = ?`, manifest.ID).Scan(&count)
	if err != nil {
		t.Fatalf("query plugins after uninstall: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 plugins after uninstall, got %d", count)
	}

	// Verify audit log
	var action string
	err = db.QueryRowContext(ctx, `
		SELECT action FROM audit_log WHERE action = 'plugin_uninstall'
	`).Scan(&action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if action != "plugin_uninstall" {
		t.Errorf("expected action 'plugin_uninstall', got '%s'", action)
	}
}

func TestUpdatePluginTrustLevel(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create audit_log table
	_, err := db.Exec(`
		CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			details TEXT,
			created_at TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create audit_log: %v", err)
	}

	ctx := context.Background()

	// Insert test plugin
	manifest := &Manifest{
		ID:         "com.test.trust",
		Name:       "Trust Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Update trust level
	if err := UpdatePluginTrustLevel(ctx, db, manifest.ID, "trusted"); err != nil {
		t.Fatalf("UpdatePluginTrustLevel failed: %v", err)
	}

	// Verify trust level changed
	var trustLevel string
	err = db.QueryRowContext(ctx, `
		SELECT trust_level FROM plugins WHERE id = ?
	`, manifest.ID).Scan(&trustLevel)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}

	if trustLevel != "trusted" {
		t.Errorf("expected trust_level 'trusted', got '%s'", trustLevel)
	}

	// Verify audit log
	var action, details string
	err = db.QueryRowContext(ctx, `
		SELECT action, details FROM audit_log WHERE action = 'plugin_trust_change'
	`).Scan(&action, &details)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}

	if !strings.Contains(details, "trust_level=trusted") {
		t.Errorf("expected details to contain 'trust_level=trusted', got '%s'", details)
	}
}

func TestUpdatePluginTrustLevel_InvalidLevel(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	err := UpdatePluginTrustLevel(ctx, db, "test.plugin", "invalid")
	if err == nil {
		t.Fatal("expected error for invalid trust level, got nil")
	}

	if !strings.Contains(err.Error(), "invalid trust level") {
		t.Errorf("expected 'invalid trust level' error, got: %v", err)
	}
}
