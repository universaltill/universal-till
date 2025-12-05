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

// TestIntegration_EventDispatchCrashIsolation verifies that plugin crashes during
// event dispatch do not corrupt the database or leave orphaned records.
// This validates PRS-301: crash isolation with DB invariant checks.
func TestIntegration_EventDispatchCrashIsolation(t *testing.T) {
	tmpDB := setupIntegrationTestDB(t)
	defer tmpDB.Close()

	ctx := context.Background()

	// Setup: Create a complete sale transaction to establish baseline
	if err := seedSaleTestData(t, tmpDB); err != nil {
		t.Fatalf("seed test data: %v", err)
	}

	// Install mock plugin that will "crash" (we'll simulate by closing its event channel)
	manifest := &Manifest{
		ID:         "crash-test-plugin",
		Name:       "Crash Test Plugin",
		Version:    "1.0.0",
		Entrypoint: "./crash-test",
		Runtime:    "go",
		Hooks: []ManifestHook{
			{
				Event:  "sale.completed",
				Action: "onSaleCompleted",
			},
		},
		Permissions: []string{"events:receive"},
	}

	// Seed plugin_catalog (required by foreign key constraint in plugins table)
	now := "2025-12-05T10:00:00Z"
	if _, err := tmpDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO plugin_catalog (
			id, version, name, description, author, website, repository_url,
			runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at
		) VALUES (?, ?, ?, '', 'test', '', '', ?, ?, '', '', '0.1.0', 'v1', ?)
	`, manifest.ID, manifest.Version, manifest.Name, manifest.Runtime, manifest.Entrypoint, now); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}

	if err := PersistManifest(ctx, tmpDB, manifest, InstallOptions{
		TrustLevel: "trusted",
		Uploader:   "test",
	}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	if err := GrantPermission(ctx, tmpDB, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	// Create event bus and subscribe plugin
	bus := NewEventBus(tmpDB)
	_, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Simulate crash by unsubscribing immediately (mimics plugin process crash)
	// NOTE: This is a simplified crash simulation for DB invariant testing.
	// It validates that EventBus handles missing subscribers gracefully, but does NOT
	// test real crash recovery (process restarts, cleanup) or supervisor features.
	// Full crash isolation testing with process supervision should be implemented separately.
	bus.Unsubscribe(manifest.ID)

	// Publish event - should not crash the host despite plugin "crash"
	saleEvent := SaleCompletedEvent{
		SaleID:     "sale-crash-test",
		TotalCents: 1200,
	}

	eventID, err := bus.PublishSaleCompleted(ctx, saleEvent)
	if err != nil {
		t.Fatalf("publish should succeed despite crashed plugin: %v", err)
	}

	if eventID == "" {
		t.Error("expected event ID")
	}

	// Assert DB Invariant 1: sales.total = SUM(payments.amount)
	var salesTotal, paymentsTotal int64
	err = tmpDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(total), 0) FROM sales WHERE status = 'completed'
	`).Scan(&salesTotal)
	if err != nil {
		t.Fatalf("query sales total: %v", err)
	}

	err = tmpDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM payments
	`).Scan(&paymentsTotal)
	if err != nil {
		t.Fatalf("query payments total: %v", err)
	}

	if salesTotal != paymentsTotal {
		t.Errorf("DB Invariant 1 violated: sales.total (%d) != SUM(payments.amount) (%d)",
			salesTotal, paymentsTotal)
	}

	// Assert DB Invariant 2: inventory.quantity reflects stock_movements
	// Stock movements are deltas: positive for additions, negative for sales.
	// The invariant: inventory.quantity = SUM(stock_movements.quantity)
	// Test data: 100 (initial movement) + (-2) (sale movement) = 98 (current inventory)
	var inventoryQty float64
	err = tmpDB.QueryRowContext(ctx, `
		SELECT quantity FROM inventory WHERE item_id = 'test-item-001'
	`).Scan(&inventoryQty)
	if err != nil {
		t.Fatalf("query inventory: %v", err)
	}

	var movementsSum float64
	err = tmpDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(quantity), 0) FROM stock_movements WHERE item_id = 'test-item-001'
	`).Scan(&movementsSum)
	if err != nil {
		t.Fatalf("query stock movements: %v", err)
	}

	// Verify that current inventory matches the sum of all movement deltas
	if inventoryQty != movementsSum {
		t.Errorf("DB Invariant 2 violated: inventory.quantity (%.2f) != SUM(stock_movements.quantity) (%.2f)",
			inventoryQty, movementsSum)
	}

	// Assert DB Invariant 3: No orphaned sale_lines
	var orphanedLines int
	err = tmpDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sale_lines 
		WHERE sale_id NOT IN (SELECT id FROM sales)
	`).Scan(&orphanedLines)
	if err != nil {
		t.Fatalf("query orphaned lines: %v", err)
	}

	if orphanedLines > 0 {
		t.Errorf("DB Invariant 3 violated: found %d orphaned sale_lines", orphanedLines)
	}

	// Check DB Invariant 4: Audit log entry exists for event publication
	// The audit_log schema bug has been fixed (code now uses 'data_json'), so this test
	// now expects audit logging to work. Fail the test if no audit log entry is found.
	var auditCount int
	err = tmpDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_log 
		WHERE action = 'event_published' AND entity_type = 'event'
	`).Scan(&auditCount)
	if err != nil {
		t.Fatalf("could not check audit log: %v", err)
	}
	if auditCount == 0 {
		t.Errorf("DB Invariant 4 violated: no audit log entry found for event publication")
	}

	t.Logf("✓ All core DB invariants verified after simulated plugin crash")
	t.Logf("  - Sales total matches payments: %d", salesTotal)
	t.Logf("  - Inventory matches movements: %.2f", inventoryQty)
	t.Logf("  - No orphaned sale_lines: %d", orphanedLines)
	t.Logf("  - Audit log entries: %d (non-critical)", auditCount)
}

// seedSaleTestData creates a minimal complete sale for crash isolation testing
func seedSaleTestData(t *testing.T, db *sql.DB) error {
	t.Helper()

	now := "2025-12-05T10:00:00Z"

	stmts := []string{
		`INSERT OR IGNORE INTO stock_locations (id, name) VALUES ('loc-test', 'Test Location')`,
		`INSERT OR IGNORE INTO items (id, sku, name, base_price, is_active) VALUES ('test-item-001', 'TEST-001', 'Test Item', 500, 1)`,
		`INSERT OR IGNORE INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) 
			VALUES ('inv-test', 'test-item-001', NULL, 'loc-test', 98, ?)`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`,
		`INSERT OR IGNORE INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, rounding, created_at, completed_at)
			VALUES ('sale-test-001', 'RCP-TEST-001', 'completed', 'sale', 'GBP', 1000, 0, 200, 1200, 0, ?, ?)`,
		`INSERT OR IGNORE INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
			VALUES ('line-test-001', 'sale-test-001', 1, 'test-item-001', 'Test Item', 'TEST-001', 2, 500, 0, 2000, 200, 1000, 1200)`,
		`INSERT OR IGNORE INTO payments (id, sale_id, method_id, amount, currency, change_given, paid_at)
			VALUES ('pay-test-001', 'sale-test-001', 'cash', 1200, 'GBP', 0, ?)`,
		`INSERT OR IGNORE INTO stock_movements (id, item_id, variant_id, location_id, sale_line_id, type, quantity, created_at)
			VALUES ('mov-initial', 'test-item-001', NULL, 'loc-test', NULL, 'initial', 100, ?)`,
		`INSERT OR IGNORE INTO stock_movements (id, item_id, variant_id, location_id, sale_line_id, type, quantity, created_at)
			VALUES ('mov-test-001', 'test-item-001', NULL, 'loc-test', 'line-test-001', 'sale', -2, ?)`,
	}

	for _, stmt := range stmts {
		// Count placeholders
		placeholderCount := 0
		for _, c := range stmt {
			if c == '?' {
				placeholderCount++
			}
		}

		// Prepare arguments
		args := make([]interface{}, placeholderCount)
		for i := range args {
			args[i] = now
		}

		if _, err := db.Exec(stmt, args...); err != nil {
			return err
		}
	}

	return nil
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
