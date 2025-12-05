package plugins

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestCheckPermission_Granted(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	// Install plugin with permissions
	manifest := &Manifest{
		ID:          "com.test.perm",
		Name:        "Permission Test",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Permissions: []string{"sales:read", "sales:write"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Grant the permission
	if err := GrantPermission(ctx, db, manifest.ID, "sales:read"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	// Check should succeed
	if err := CheckPermission(ctx, db, manifest.ID, "sales:read"); err != nil {
		t.Errorf("CheckPermission failed: %v", err)
	}
}

func TestCheckPermission_NotGranted(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:          "com.test.notgranted",
		Name:        "Not Granted Test",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Permissions: []string{"sales:write"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Permission exists but not granted (default)
	err := CheckPermission(ctx, db, manifest.ID, "sales:write")
	if err == nil {
		t.Fatal("expected permission denied error, got nil")
	}

	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected 'permission denied' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not granted") {
		t.Errorf("expected 'not granted' in error, got: %v", err)
	}

	// Verify audit log
	var action string
	err = db.QueryRowContext(ctx, `
		SELECT action FROM audit_log WHERE action = 'permission_denied'
	`).Scan(&action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
}

func TestCheckPermission_NotDeclared(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:          "com.test.notdeclared",
		Name:        "Not Declared Test",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Permissions: []string{"sales:read"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Check permission not in manifest
	err := CheckPermission(ctx, db, manifest.ID, "customers:write")
	if err == nil {
		t.Fatal("expected permission denied error, got nil")
	}

	if !strings.Contains(err.Error(), "not declared") {
		t.Errorf("expected 'not declared' in error, got: %v", err)
	}
}

func TestGrantPermission(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:          "com.test.grant",
		Name:        "Grant Test",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Permissions: []string{"devices:usb"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Grant permission
	if err := GrantPermission(ctx, db, manifest.ID, "devices:usb"); err != nil {
		t.Fatalf("GrantPermission failed: %v", err)
	}

	// Verify granted
	var granted int
	err := db.QueryRowContext(ctx, `
		SELECT granted FROM plugin_permissions
		WHERE plugin_id = ? AND permission = ?
	`, manifest.ID, "devices:usb").Scan(&granted)
	if err != nil {
		t.Fatalf("query permission: %v", err)
	}

	if granted != 1 {
		t.Errorf("expected granted=1, got %d", granted)
	}

	// Verify audit log
	var action string
	err = db.QueryRowContext(ctx, `
		SELECT action FROM audit_log WHERE action = 'permission_granted'
	`).Scan(&action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
}

func TestGrantPermission_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	err := GrantPermission(ctx, db, "nonexistent", "some:permission")
	if err == nil {
		t.Fatal("expected error for nonexistent permission, got nil")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestRevokePermission(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:          "com.test.revoke",
		Name:        "Revoke Test",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Permissions: []string{"payments:charge"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Grant then revoke
	if err := GrantPermission(ctx, db, manifest.ID, "payments:charge"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	if err := RevokePermission(ctx, db, manifest.ID, "payments:charge"); err != nil {
		t.Fatalf("RevokePermission failed: %v", err)
	}

	// Verify revoked
	var granted int
	err := db.QueryRowContext(ctx, `
		SELECT granted FROM plugin_permissions
		WHERE plugin_id = ? AND permission = ?
	`, manifest.ID, "payments:charge").Scan(&granted)
	if err != nil {
		t.Fatalf("query permission: %v", err)
	}

	if granted != 0 {
		t.Errorf("expected granted=0 after revoke, got %d", granted)
	}

	// Verify audit log
	var action string
	err = db.QueryRowContext(ctx, `
		SELECT action FROM audit_log WHERE action = 'permission_revoked'
	`).Scan(&action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
}

func TestListPluginPermissions(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	manifest := &Manifest{
		ID:         "com.test.list",
		Name:       "List Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Permissions: []string{
			"sales:read",
			"sales:write",
			"customers:read",
		},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Grant one permission
	if err := GrantPermission(ctx, db, manifest.ID, "sales:read"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	// List permissions
	perms, err := ListPluginPermissions(ctx, db, manifest.ID)
	if err != nil {
		t.Fatalf("ListPluginPermissions failed: %v", err)
	}

	if len(perms) != 3 {
		t.Errorf("expected 3 permissions, got %d", len(perms))
	}

	// Verify granted status
	grantedCount := 0
	for _, p := range perms {
		if p.Granted {
			grantedCount++
			if p.Name != "sales:read" {
				t.Errorf("expected only 'sales:read' granted, got '%s'", p.Name)
			}
		}
	}

	if grantedCount != 1 {
		t.Errorf("expected 1 granted permission, got %d", grantedCount)
	}
}

func TestCheckMultiplePermissions(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:         "com.test.multi",
		Name:       "Multi Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Permissions: []string{
			"sales:read",
			"sales:write",
			"customers:read",
		},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Grant some permissions
	if err := GrantPermission(ctx, db, manifest.ID, "sales:read"); err != nil {
		t.Fatalf("grant sales:read: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "sales:write"); err != nil {
		t.Fatalf("grant sales:write: %v", err)
	}

	// Check multiple - should succeed
	err := CheckMultiplePermissions(ctx, db, manifest.ID, []string{"sales:read", "sales:write"})
	if err != nil {
		t.Errorf("CheckMultiplePermissions failed: %v", err)
	}

	// Check multiple with one denied - should fail
	err = CheckMultiplePermissions(ctx, db, manifest.ID, []string{"sales:read", "customers:read"})
	if err == nil {
		t.Fatal("expected error for mixed permissions, got nil")
	}

	if !strings.Contains(err.Error(), "permission checks failed") {
		t.Errorf("expected 'permission checks failed' error, got: %v", err)
	}
}

func TestHasAnyPermission(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:          "com.test.any",
		Name:        "Any Test",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Permissions: []string{"sales:read", "sales:write"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Grant one permission
	if err := GrantPermission(ctx, db, manifest.ID, "sales:read"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	// Check has any - should be true
	hasAny, err := HasAnyPermission(ctx, db, manifest.ID, []string{"sales:read", "customers:write"})
	if err != nil {
		t.Fatalf("HasAnyPermission failed: %v", err)
	}
	if !hasAny {
		t.Error("expected hasAny=true, got false")
	}

	// Check has any with all denied - should be false
	hasAny, err = HasAnyPermission(ctx, db, manifest.ID, []string{"customers:read", "customers:write"})
	if err != nil {
		t.Fatalf("HasAnyPermission failed: %v", err)
	}
	if hasAny {
		t.Error("expected hasAny=false, got true")
	}
}

// setupAuditLog creates the audit_log table for tests
func setupAuditLog(t *testing.T, db *sql.DB) {
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
}
