package plugins

import (
	"context"
	"strings"
	"testing"
)

func TestUninstallPlugin(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create audit_log table
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			data_json TEXT,
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

// TestUninstallPlugin_PreservesFiscalRegisterStorage pins ADR-0072/
// ut-docs#1106 review finding B1: UninstallPlugin is reachable from fully
// automatic paths (sync-driven pruning, version-pin-mismatch rollback), not
// only a deliberate operator uninstall, so a blanket plugin_storage wipe
// would let an automatic sync hiccup destroy a shop's §146a Abs. 4 AO
// bookkeeping. Everything under the fiscal_register: prefix must survive an
// uninstall; anything else in the plugin's namespace still gets cleared.
func TestUninstallPlugin_PreservesFiscalRegisterStorage(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			data_json TEXT,
			created_at TEXT NOT NULL
		)
	`); err != nil {
		t.Fatalf("create audit_log: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS plugin_storage (
			plugin_id  TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      BLOB NOT NULL,
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (plugin_id, key)
		)
	`); err != nil {
		t.Fatalf("create plugin_storage: %v", err)
	}

	ctx := context.Background()
	manifest := &Manifest{
		ID:         "com.universaltill.tax-de",
		Name:       "German Tax",
		Version:    "1.0.0",
		Entrypoint: "./test",
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// One fiscal-register entry (must survive) and one unrelated key (must
	// still be cleared, same as before this fix).
	if _, err := db.Exec(`INSERT INTO plugin_storage (plugin_id, key, value) VALUES (?, ?, ?)`,
		manifest.ID, "fiscal_register:abc-123", []byte(`{"id":"abc-123"}`)); err != nil {
		t.Fatalf("seed fiscal_register entry: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_storage (plugin_id, key, value) VALUES (?, ?, ?)`,
		manifest.ID, "tse_result:sale-1", []byte(`{"sale_id":"sale-1"}`)); err != nil {
		t.Fatalf("seed tse_result entry: %v", err)
	}

	if err := UninstallPlugin(ctx, db, manifest.ID); err != nil {
		t.Fatalf("UninstallPlugin failed: %v", err)
	}

	var fiscalRegisterCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ? AND key = ?`,
		manifest.ID, "fiscal_register:abc-123").Scan(&fiscalRegisterCount); err != nil {
		t.Fatalf("query fiscal_register entry: %v", err)
	}
	if fiscalRegisterCount != 1 {
		t.Errorf("expected fiscal_register:abc-123 to survive uninstall, got count=%d", fiscalRegisterCount)
	}

	var tseResultCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ? AND key = ?`,
		manifest.ID, "tse_result:sale-1").Scan(&tseResultCount); err != nil {
		t.Fatalf("query tse_result entry: %v", err)
	}
	if tseResultCount != 0 {
		t.Errorf("expected tse_result:sale-1 to be cleared by uninstall, got count=%d", tseResultCount)
	}
}

func TestUpdatePluginTrustLevel(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create audit_log table
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			data_json TEXT,
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
		SELECT action, data_json FROM audit_log WHERE action = 'plugin_trust_change'
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

func TestUpdatePluginTrustLevel_AllValidLevels(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create audit_log table
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			data_json TEXT,
			created_at TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create audit_log: %v", err)
	}

	ctx := context.Background()

	// Insert test plugin
	manifest := &Manifest{
		ID:         "com.test.alllevels",
		Name:       "All Levels Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Test all valid trust levels
	validLevels := []string{"untrusted", "verified", "trusted", "revoked"}
	for _, level := range validLevels {
		if err := UpdatePluginTrustLevel(ctx, db, manifest.ID, level); err != nil {
			t.Errorf("UpdatePluginTrustLevel failed for level '%s': %v", level, err)
		}

		// Verify trust level changed
		var trustLevel string
		err = db.QueryRowContext(ctx, `
			SELECT trust_level FROM plugins WHERE id = ?
		`, manifest.ID).Scan(&trustLevel)
		if err != nil {
			t.Fatalf("query plugin: %v", err)
		}

		if trustLevel != level {
			t.Errorf("expected trust_level '%s', got '%s'", level, trustLevel)
		}
	}
}
