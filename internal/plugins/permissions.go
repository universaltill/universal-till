package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// CheckPermission verifies if a plugin has a granted permission
func CheckPermission(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	repo := data.NewPluginRepo(db)
	granted, exists, err := repo.CheckPermission(ctx, pluginID, permission)
	if err != nil {
		return err
	}
	if !exists {
		// Permission not defined in manifest
		if err := auditPermissionDenial(ctx, db, pluginID, permission, "permission not declared"); err != nil {
			fmt.Printf("warning: failed to audit permission denial: %v\n", err)
		}
		return fmt.Errorf("permission denied: %s not declared for plugin %s", permission, pluginID)
	}
	if !granted {
		if err := auditPermissionDenial(ctx, db, pluginID, permission, "permission not granted"); err != nil {
			fmt.Printf("warning: failed to audit permission denial: %v\n", err)
		}
		return fmt.Errorf("permission denied: %s not granted for plugin %s", permission, pluginID)
	}
	return nil
}

// GrantPermission grants a permission to a plugin
func GrantPermission(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	repo := data.NewPluginRepo(db)
	if err := repo.SetPermission(ctx, pluginID, permission, true); err != nil {
		return err
	}

	// Audit the grant
	if err := auditPermissionGrant(ctx, db, pluginID, permission); err != nil {
		fmt.Printf("warning: failed to audit permission grant: %v\n", err)
	}

	return nil
}

// RevokePermission revokes a permission from a plugin
func RevokePermission(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	repo := data.NewPluginRepo(db)
	if err := repo.SetPermission(ctx, pluginID, permission, false); err != nil {
		return err
	}

	// Audit the revocation
	if err := auditPermissionRevoke(ctx, db, pluginID, permission); err != nil {
		fmt.Printf("warning: failed to audit permission revoke: %v\n", err)
	}

	return nil
}

// ListPluginPermissions returns all permissions for a plugin with grant status
func ListPluginPermissions(ctx context.Context, db *sql.DB, pluginID string) ([]Permission, error) {
	repo := data.NewPluginRepo(db)
	rows, err := repo.ListPermissions(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	var out []Permission
	for _, r := range rows {
		out = append(out, Permission{Name: r.Permission, Granted: r.Granted})
	}
	return out, nil
}

// Permission represents a plugin permission
type Permission struct {
	Name    string
	Granted bool
}

// CheckMultiplePermissions verifies if a plugin has all required permissions
func CheckMultiplePermissions(ctx context.Context, db *sql.DB, pluginID string, permissions []string) error {
	var errors []string

	for _, perm := range permissions {
		if err := CheckPermission(ctx, db, pluginID, perm); err != nil {
			errors = append(errors, err.Error())
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("permission checks failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

// HasAnyPermission checks if a plugin has at least one of the specified permissions
func HasAnyPermission(ctx context.Context, db *sql.DB, pluginID string, permissions []string) (bool, error) {
	for _, perm := range permissions {
		if err := CheckPermission(ctx, db, pluginID, perm); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// auditPermissionDenial logs permission denial to audit_log
func auditPermissionDenial(ctx context.Context, db *sql.DB, pluginID, permission, reason string) error {
	repo := data.NewPluginRepo(db)
	return repo.InsertAudit(ctx, nil, "permission_denied", pluginID, map[string]any{
		"permission": permission,
		"reason":     reason,
	}, time.Now())
}

// auditPermissionGrant logs permission grant to audit_log
func auditPermissionGrant(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	repo := data.NewPluginRepo(db)
	return repo.InsertAudit(ctx, nil, "permission_granted", pluginID, map[string]any{
		"permission": permission,
	}, time.Now())
}

// auditPermissionRevoke logs permission revocation to audit_log
func auditPermissionRevoke(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	repo := data.NewPluginRepo(db)
	return repo.InsertAudit(ctx, nil, "permission_revoked", pluginID, map[string]any{
		"permission": permission,
	}, time.Now())
}
