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
