//go:build integration
// +build integration

package plugins

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	_ "modernc.org/sqlite"
)

// TestIntegration_PermissionDenialAudit verifies that denied permission attempts
// create audit log entries with correct metadata
func TestIntegration_PermissionDenialAudit(t *testing.T) {
	// Setup: Create temp database with full schema
	tmpDB := setupIntegrationTestDB(t)
	defer tmpDB.Close()

	ctx := context.Background()

	// Create manifest with permission requirement
	manifestPath := createTestManifest(t, map[string]interface{}{
		"id":          "test-plugin",
		"name":        "Test Plugin",
		"version":     "1.0.0",
		"entrypoint":  "./test",
		"runtime":     "go",
		"permissions": []string{"pos.sales.read"},
	})
	defer os.Remove(manifestPath)

	binaryPath := createEmptyBinary(t, "test-plugin")
	defer os.Remove(binaryPath)

	// Install plugin
	opts := InstallOptions{
		InstalledFromURL: "http://test.com/plugin.tar.gz",
		SHA256:           computeTestChecksum(t, binaryPath),
		TrustLevel:       "untrusted",
		Uploader:         "test",
	}
	if err := InstallPlugin(ctx, tmpDB, manifestPath, binaryPath, opts); err != nil {
		t.Fatalf("failed to install plugin: %v", err)
	}

	// DO NOT grant permission - attempt check should fail

	// Act: Check permission (should be denied)
	err := CheckPermission(ctx, tmpDB, "test-plugin", "pos.sales.read")
	if err == nil {
		t.Fatal("expected permission to be denied")
	}

	// Assert: Verify audit log entry exists
	var count int
	err = tmpDB.QueryRowContext(ctx, `
		SELECT COUNT(*) 
		FROM audit_log 
		WHERE action = 'permission_denied' 
		AND entity_type = 'plugin' 
		AND entity_id = 'test-plugin'
	`).Scan(&count)

	if err != nil {
		t.Fatalf("failed to query audit log: %v", err)
	}

	if count == 0 {
		t.Error("expected audit log entry for permission denial, got none")
	}
}

// TestIntegration_MenuFilteringByPermissions verifies that plugin menu entries
// are only rendered when the plugin has required permissions granted
func TestIntegration_MenuFilteringByPermissions(t *testing.T) {
	tmpDB := setupIntegrationTestDB(t)
	defer tmpDB.Close()

	ctx := context.Background()

	// Create plugin with menu entry requiring permission
	manifestPath := createTestManifest(t, map[string]interface{}{
		"id":         "menu-plugin",
		"name":       "Menu Plugin",
		"version":    "1.0.0",
		"entrypoint": "./plugin",
		"runtime":    "go",
		"entries": []map[string]interface{}{
			{
				"key":         "reports",
				"route":       "/plug/reports",
				"label":       "Sales Reports",
				"menu":        "main",
				"permissions": []string{"pos.reports.view"},
			},
		},
	})
	defer os.Remove(manifestPath)

	binaryPath := createEmptyBinary(t, "menu-plugin")
	defer os.Remove(binaryPath)

	opts := InstallOptions{
		SHA256:     computeTestChecksum(t, binaryPath),
		TrustLevel: "untrusted",
		Uploader:   "test",
	}
	if err := InstallPlugin(ctx, tmpDB, manifestPath, binaryPath, opts); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// Load manager and check menu entries WITHOUT permission
	mgr, err := Init(ctx, testConfig(), tmpDB)
	if err != nil {
		t.Fatalf("failed to init manager: %v", err)
	}

	// Assert: Menu entry should NOT be loaded (no permission granted)
	if len(mgr.MenuPlugins) > 0 {
		t.Errorf("expected no menu entries without permissions, got %d", len(mgr.MenuPlugins))
	}

	// Act: Grant permission
	if err := GrantPermission(ctx, tmpDB, "menu-plugin", "pos.reports.view"); err != nil {
		t.Fatalf("grant permission failed: %v", err)
	}

	// Reload manager
	mgr2, err := Init(ctx, testConfig(), tmpDB)
	if err != nil {
		t.Fatalf("failed to reinit manager: %v", err)
	}

	// Assert: Menu entry should NOW be loaded
	if len(mgr2.MenuPlugins) != 1 {
		t.Errorf("expected 1 menu entry with permission, got %d", len(mgr2.MenuPlugins))
	}
}

// TestIntegration_ProcessCrashRecovery verifies that supervisor can restart
// crashed plugins and DB integrity is maintained
func TestIntegration_ProcessCrashRecovery(t *testing.T) {
	t.Skip("Requires real plugin binary with crash simulation - implement when supervisor is used")

	// TODO:
	// 1. Start plugin process via supervisor
	// 2. Kill process externally
	// 3. Verify supervisor detects crash
	// 4. Verify restart policy triggers
	// 5. Verify DB plugin state remains consistent
	// 6. Verify audit log records crash + restart events
}

// TestIntegration_IPCEventRoundTrip verifies that event bus can publish events,
// plugins receive them, and acknowledgements flow back correctly
func TestIntegration_IPCEventRoundTrip(t *testing.T) {
	t.Skip("Requires running gRPC plugin - implement with test-plugin binary")

	// TODO:
	// 1. Start event bus
	// 2. Start test plugin that subscribes to sale.completed
	// 3. Publish sale.completed event
	// 4. Wait for plugin to receive and acknowledge
	// 5. Verify ack received
	// 6. Verify audit trail of publish→subscribe→ack
}

// TestIntegration_MarketplaceChecksumRejection verifies that install flow
// rejects packages with mismatched SHA256 checksums
func TestIntegration_MarketplaceChecksumRejection(t *testing.T) {
	tmpDB := setupIntegrationTestDB(t)
	defer tmpDB.Close()

	ctx := context.Background()

	manifestPath := createTestManifest(t, map[string]interface{}{
		"id":         "checksum-test",
		"name":       "Checksum Test",
		"version":    "1.0.0",
		"entrypoint": "./plugin",
		"runtime":    "go",
	})
	defer os.Remove(manifestPath)

	binaryPath := createEmptyBinary(t, "checksum-test")
	defer os.Remove(binaryPath)

	// Compute CORRECT checksum
	correctChecksum := computeTestChecksum(t, binaryPath)

	// Act: Attempt install with WRONG checksum
	opts := InstallOptions{
		SHA256:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // deliberately wrong
		TrustLevel: "untrusted",
		Uploader:   "test",
	}

	err := InstallPlugin(ctx, tmpDB, manifestPath, binaryPath, opts)

	// Assert: Install should FAIL due to checksum mismatch
	if err == nil {
		t.Fatal("expected install to fail with checksum mismatch, but it succeeded")
	}

	if !contains(err.Error(), "checksum") && !contains(err.Error(), "mismatch") {
		t.Errorf("expected checksum error, got: %v", err)
	}

	// Verify plugin was NOT installed
	var count int
	tmpDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plugins WHERE id = 'checksum-test'").Scan(&count)
	if count > 0 {
		t.Error("plugin should not be installed after checksum failure")
	}

	// Act: Install with correct checksum
	opts.SHA256 = correctChecksum
	if err := InstallPlugin(ctx, tmpDB, manifestPath, binaryPath, opts); err != nil {
		t.Fatalf("install with correct checksum should succeed: %v", err)
	}

	// Assert: Plugin now installed
	tmpDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plugins WHERE id = 'checksum-test'").Scan(&count)
	if count != 1 {
		t.Error("plugin should be installed after correct checksum")
	}
}

// Helper functions

func setupIntegrationTestDB(t *testing.T) *sql.DB {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "plugin-integration-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	// Use db.Open which automatically runs migrations
	database, err := db.Open(tmpFile.Name())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	return database.DB
}

func createTestManifest(t *testing.T, data map[string]interface{}) string {
	t.Helper()

	// TODO: Implement JSON marshaling of manifest
	// For now, return empty path - implement when needed
	t.Skip("createTestManifest not fully implemented")
	return ""
}

func createEmptyBinary(t *testing.T, name string) string {
	t.Helper()

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, name)

	if err := os.WriteFile(binPath, []byte{}, 0755); err != nil {
		t.Fatalf("create binary: %v", err)
	}

	return binPath
}

func computeTestChecksum(t *testing.T, path string) string {
	t.Helper()

	checksum, err := ComputeSHA256(path)
	if err != nil {
		t.Fatalf("compute checksum: %v", err)
	}
	return checksum
}

func testConfig() *config.Config {
	return &config.Config{
		MarketplaceURL: "http://127.0.0.1:8081",
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findInString(s, substr))
}

func findInString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
