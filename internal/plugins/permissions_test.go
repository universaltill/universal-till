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

// TestCheckPermissionGranted_DistinguishesErrorFromDenial is the ut-docs#228
// regression: a caller that wants to treat "not granted" as a non-fatal
// fallback (e.g. omit optional data) must NOT also treat a genuine
// infrastructure failure that way -- CheckPermission's single collapsed
// error can't tell the two apart without parsing the error string;
// CheckPermissionGranted returns them as distinct return values instead.
func TestCheckPermissionGranted_DistinguishesErrorFromDenial(t *testing.T) {
	db := setupTestDB(t)
	setupAuditLog(t, db)
	ctx := context.Background()

	manifest := &Manifest{
		ID:          "com.test.granted",
		Name:        "Granted Test",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Permissions: []string{"sales:read", "sales:write"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "sales:read"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	granted, err := CheckPermissionGranted(ctx, db, manifest.ID, "sales:read")
	if err != nil || !granted {
		t.Fatalf("expected granted=true, err=nil for a real grant; got granted=%v err=%v", granted, err)
	}

	granted, err = CheckPermissionGranted(ctx, db, manifest.ID, "sales:write")
	if err != nil {
		t.Fatalf("expected err=nil for a legitimate not-granted denial (not an infra failure), got %v", err)
	}
	if granted {
		t.Fatal("expected granted=false for an ungranted-but-declared permission")
	}

	granted, err = CheckPermissionGranted(ctx, db, manifest.ID, "customers:read")
	if err != nil {
		t.Fatalf("expected err=nil for a not-declared permission (still a legitimate denial, not an infra failure), got %v", err)
	}
	if granted {
		t.Fatal("expected granted=false for a never-declared permission")
	}

	// Genuine infrastructure failure: closing the DB makes the underlying
	// query fail with sql.ErrConnDone, not sql.ErrNoRows -- this must come
	// back as a real error, not be silently folded into granted=false the
	// way a caller doing "if CheckPermission(...) == nil" would.
	db.Close()
	if _, err := CheckPermissionGranted(ctx, db, manifest.ID, "sales:read"); err == nil {
		t.Fatal("expected a real error from a closed DB, got nil (infra failure silently treated as a denial)")
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

// ut-docs#3945: a repeated, non-deliberate check audits a denial once.
func TestCheckPermissionAuditOnce_3945(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()

	manifest := &Manifest{ID: "com.test.once", Name: "Once", Version: "1.0.0", Entrypoint: "./test", Permissions: []string{"ui:slot:reports.panels"}}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	const perm = "ui:slot:reports.panels"

	// Declared but not granted: audited the first time only.
	for i, wantFirst := range []bool{true, false, false} {
		granted, first, err := CheckPermissionAuditOnce(ctx, db, manifest.ID, perm)
		if err != nil || granted || first != wantFirst {
			t.Fatalf("check %d = granted %v first %v err %v, want denied first=%v", i, granted, first, err, wantFirst)
		}
	}
	if n := countDenials(t, db, manifest.ID); n != 1 {
		t.Fatalf("audit rows after 3 denied checks = %d, want 1", n)
	}

	// Granted: no row, and it forgets the denial.
	if err := GrantPermission(ctx, db, manifest.ID, perm); err != nil {
		t.Fatal(err)
	}
	if granted, first, err := CheckPermissionAuditOnce(ctx, db, manifest.ID, perm); err != nil || !granted || first {
		t.Fatalf("granted check = %v %v %v", granted, first, err)
	}
	if n := countDenials(t, db, manifest.ID); n != 1 {
		t.Fatalf("granted check changed audit rows to %d", n)
	}

	// Re-revoked: audits again.
	if err := RevokePermission(ctx, db, manifest.ID, perm); err != nil {
		t.Fatal(err)
	}
	if granted, first, err := CheckPermissionAuditOnce(ctx, db, manifest.ID, perm); err != nil || granted || !first {
		t.Fatalf("re-revoked check = %v %v %v, want denied first", granted, first, err)
	}
	if n := countDenials(t, db, manifest.ID); n != 2 {
		t.Fatalf("audit rows after re-revocation = %d, want 2", n)
	}

	// Undeclared: one row however often it is checked.
	for i := 0; i < 2; i++ {
		granted, first, err := CheckPermissionAuditOnce(ctx, db, manifest.ID, "ui:slot:eod.footer")
		if err != nil || granted || first != (i == 0) {
			t.Fatalf("undeclared check %d = %v %v %v", i, granted, first, err)
		}
	}
	if n := countDenials(t, db, manifest.ID); n != 3 {
		t.Fatalf("audit rows after undeclared checks = %d, want 3", n)
	}
}

// ut-docs#3945 review: a grant change re-arms the audit even when no check
// saw the plugin granted in between, and so does a re-install.
func TestCheckPermissionAuditOnce_GrantChangeRearms_3945(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()

	manifest := &Manifest{ID: "com.test.rearm", Name: "Rearm", Version: "1.0.0", Entrypoint: "./test", Permissions: []string{"ui:slot:reports.panels"}}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	const perm = "ui:slot:reports.panels"
	mustFirst := func(step string) {
		t.Helper()
		if granted, first, err := CheckPermissionAuditOnce(ctx, db, manifest.ID, perm); err != nil || granted || !first {
			t.Fatalf("%s: check = granted %v first %v err %v, want a first denial", step, granted, first, err)
		}
	}
	mustFirst("initial")

	// Granted and revoked again with no check in between.
	if err := GrantPermission(ctx, db, manifest.ID, perm); err != nil {
		t.Fatal(err)
	}
	if err := RevokePermission(ctx, db, manifest.ID, perm); err != nil {
		t.Fatal(err)
	}
	mustFirst("after an unobserved grant and revoke")

	// Re-installed: its permissions are written afresh.
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("re-persist manifest: %v", err)
	}
	mustFirst("after a re-install")
}
