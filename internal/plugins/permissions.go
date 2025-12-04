package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CheckPermission verifies if a plugin has a granted permission
func CheckPermission(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	var granted int
	err := db.QueryRowContext(ctx, `
		SELECT granted
		FROM plugin_permissions
		WHERE plugin_id = ? AND permission = ?
	`, pluginID, permission).Scan(&granted)

	if err == sql.ErrNoRows {
		// Permission not defined in manifest
		if err := auditPermissionDenial(ctx, db, pluginID, permission, "permission not declared"); err != nil {
			fmt.Printf("warning: failed to audit permission denial: %v\n", err)
		}
		return fmt.Errorf("permission denied: %s not declared for plugin %s", permission, pluginID)
	}

	if err != nil {
		return fmt.Errorf("check permission: %w", err)
	}

	if granted == 0 {
		// Permission declared but not granted
		if err := auditPermissionDenial(ctx, db, pluginID, permission, "permission not granted"); err != nil {
			fmt.Printf("warning: failed to audit permission denial: %v\n", err)
		}
		return fmt.Errorf("permission denied: %s not granted for plugin %s", permission, pluginID)
	}

	return nil
}

// GrantPermission grants a permission to a plugin
func GrantPermission(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	result, err := db.ExecContext(ctx, `
		UPDATE plugin_permissions
		SET granted = 1
		WHERE plugin_id = ? AND permission = ?
	`, pluginID, permission)
	if err != nil {
		return fmt.Errorf("grant permission: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("permission %s not found for plugin %s", permission, pluginID)
	}

	// Audit the grant
	if err := auditPermissionGrant(ctx, db, pluginID, permission); err != nil {
		fmt.Printf("warning: failed to audit permission grant: %v\n", err)
	}

	return nil
}

// RevokePermission revokes a permission from a plugin
func RevokePermission(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	result, err := db.ExecContext(ctx, `
		UPDATE plugin_permissions
		SET granted = 0
		WHERE plugin_id = ? AND permission = ?
	`, pluginID, permission)
	if err != nil {
		return fmt.Errorf("revoke permission: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("permission %s not found for plugin %s", permission, pluginID)
	}

	// Audit the revocation
	if err := auditPermissionRevoke(ctx, db, pluginID, permission); err != nil {
		fmt.Printf("warning: failed to audit permission revoke: %v\n", err)
	}

	return nil
}

// ListPluginPermissions returns all permissions for a plugin with grant status
func ListPluginPermissions(ctx context.Context, db *sql.DB, pluginID string) ([]Permission, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT permission, granted
		FROM plugin_permissions
		WHERE plugin_id = ?
		ORDER BY permission
	`, pluginID)
	if err != nil {
		return nil, fmt.Errorf("query permissions: %w", err)
	}
	defer rows.Close()

	var permissions []Permission
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Name, &p.Granted); err != nil {
			return nil, fmt.Errorf("scan permission: %w", err)
		}
		permissions = append(permissions, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate permissions: %w", err)
	}

	return permissions, nil
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
	now := time.Now().UTC().Format(time.RFC3339)
	details := fmt.Sprintf("permission=%s, reason=%s", permission, reason)

	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, details, created_at)
		VALUES ('permission_denied', 'plugin', ?, ?, ?)
	`, pluginID, details, now)
	return err
}

// auditPermissionGrant logs permission grant to audit_log
func auditPermissionGrant(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	details := fmt.Sprintf("permission=%s", permission)

	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, details, created_at)
		VALUES ('permission_granted', 'plugin', ?, ?, ?)
	`, pluginID, details, now)
	return err
}

// auditPermissionRevoke logs permission revocation to audit_log
func auditPermissionRevoke(ctx context.Context, db *sql.DB, pluginID, permission string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	details := fmt.Sprintf("permission=%s", permission)

	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, details, created_at)
		VALUES ('permission_revoked', 'plugin', ?, ?, ?)
	`, pluginID, details, now)
	return err
}
