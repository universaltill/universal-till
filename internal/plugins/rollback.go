package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/logging"
)

// RollbackManager handles plugin version rollback operations
type RollbackManager struct {
	db            *sql.DB
	pluginBaseDir string
	maxVersions   int // maximum versions to keep
}

// NewRollbackManager creates a new rollback manager
func NewRollbackManager(db *sql.DB, pluginBaseDir string) *RollbackManager {
	return &RollbackManager{
		db:            db,
		pluginBaseDir: pluginBaseDir,
		maxVersions:   3, // keep last 3 versions
	}
}

// VersionInfo contains information about a plugin version
type VersionInfo struct {
	Version     string
	InstalledAt time.Time
	Path        string
	IsActive    bool
}

// GetVersionHistory retrieves version history for a plugin
func (rm *RollbackManager) GetVersionHistory(ctx context.Context, pluginID string) ([]VersionInfo, error) {
	// Check plugin directory
	pluginDir := filepath.Join(rm.pluginBaseDir, pluginID, "versions")

	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		return []VersionInfo{}, nil
	}

	// Read version directories
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin versions: %w", err)
	}

	// Get current version from database
	var currentVersion string
	err = rm.db.QueryRowContext(ctx,
		`SELECT version FROM plugins WHERE id = ? AND is_active = 1 LIMIT 1`,
		pluginID).Scan(&currentVersion)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get current version: %w", err)
	}

	var versions []VersionInfo
	for _, entry := range entries {
		if entry.IsDir() {
			versionPath := filepath.Join(pluginDir, entry.Name())
			info, err := os.Stat(versionPath)
			if err != nil {
				continue
			}

			versions = append(versions, VersionInfo{
				Version:     entry.Name(),
				InstalledAt: info.ModTime(),
				Path:        versionPath,
				IsActive:    entry.Name() == currentVersion,
			})
		}
	}

	return versions, nil
}

// Rollback rolls back a plugin to a previous version
func (rm *RollbackManager) Rollback(ctx context.Context, pluginID, targetVersion, actorID string) error {
	log := logging.L()

	// Verify target version exists
	targetPath := filepath.Join(rm.pluginBaseDir, pluginID, "versions", targetVersion)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		return fmt.Errorf("target version %s not found for plugin %s", targetVersion, pluginID)
	}

	// Get current version
	var currentVersion string
	err := rm.db.QueryRowContext(ctx,
		`SELECT version FROM plugins WHERE id = ? AND is_active = 1 LIMIT 1`,
		pluginID).Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	if currentVersion == targetVersion {
		return fmt.Errorf("plugin is already at version %s", targetVersion)
	}

	// Load manifest from target version
	manifestPath := filepath.Join(targetPath, "manifest.json")
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to open manifest: %w", err)
	}
	defer manifestFile.Close()

	manifest, err := ParseManifest(manifestFile)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Begin transaction
	tx, err := rm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	// Update plugins table
	_, err = tx.ExecContext(ctx, `
		UPDATE plugins 
		SET version = ?, 
		    entrypoint = ?,
		    updated_at = ?,
		    install_state = 'installed'
		WHERE id = ?
	`, targetVersion, manifest.Entrypoint, now, pluginID)
	if err != nil {
		return fmt.Errorf("failed to update plugin version: %w", err)
	}

	// Delete existing entries for this plugin
	_, err = tx.ExecContext(ctx, `DELETE FROM plugin_entries WHERE plugin_id = ?`, pluginID)
	if err != nil {
		return fmt.Errorf("failed to delete old entries: %w", err)
	}

	// Re-insert entries from rolled-back manifest
	for _, e := range manifest.Entries {
		configJSON := ""
		if len(e.Config) > 0 {
			b, _ := json.Marshal(e.Config)
			configJSON = string(b)
		}

		entryID := uuid.NewString()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO plugin_entries (
				id, plugin_id, type, key, label, icon_path, sort_order, is_active,
				parent_page_key, menu_group, route, target_action, trigger_event,
				config_json, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		`, entryID, pluginID, e.Type, e.Key, e.Label, e.IconPath, e.SortOrder,
			e.ParentPageKey, e.MenuGroup, e.Route, e.TargetAction, e.TriggerEvent,
			configJSON, now, now)
		if err != nil {
			return fmt.Errorf("failed to insert plugin entry %s: %w", e.Key, err)
		}
	}

	// Create audit log entry
	auditID := uuid.New().String()
	auditData := map[string]interface{}{
		"plugin_id":       pluginID,
		"from_version":    currentVersion,
		"to_version":      targetVersion,
		"rollback_reason": "user_requested",
	}
	auditDataJSON, _ := json.Marshal(auditData)

	if actorID == "" {
		actorID = "system"
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
		VALUES (?, ?, 'plugin', ?, 'rollback', ?, ?)
	`, auditID, actorID, pluginID, string(auditDataJSON), now)
	if err != nil {
		return fmt.Errorf("failed to create audit log: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Infof("[Rollback] Plugin %s rolled back from %s to %s", pluginID, currentVersion, targetVersion)
	return nil
}

// StoreVersion saves a plugin version for potential rollback
func (rm *RollbackManager) StoreVersion(pluginID, version, sourcePath string) error {
	log := logging.L()

	versionDir := filepath.Join(rm.pluginBaseDir, pluginID, "versions", version)

	// Create version directory
	if err := os.MkdirAll(versionDir, 0755); err != nil {
		return fmt.Errorf("failed to create version directory: %w", err)
	}

	// Copy plugin files to version directory
	// For now, we assume sourcePath is already in the correct location
	// In a full implementation, you'd copy files here

	log.Infof("[Rollback] Stored version %s for plugin %s", version, pluginID)

	// Clean up old versions
	if err := rm.cleanupOldVersions(pluginID); err != nil {
		log.Warnf("[Rollback] Failed to cleanup old versions: %v", err)
	}

	return nil
}

// cleanupOldVersions removes old plugin versions, keeping only maxVersions
func (rm *RollbackManager) cleanupOldVersions(pluginID string) error {
	versionsDir := filepath.Join(rm.pluginBaseDir, pluginID, "versions")

	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return err
	}

	// If we have more than maxVersions, delete oldest
	if len(entries) > rm.maxVersions {
		// Get modification times
		type versionEntry struct {
			name    string
			modTime time.Time
		}

		var versions []versionEntry
		for _, entry := range entries {
			if entry.IsDir() {
				info, err := entry.Info()
				if err != nil {
					continue
				}
				versions = append(versions, versionEntry{
					name:    entry.Name(),
					modTime: info.ModTime(),
				})
			}
		}

		// Sort by modification time (oldest first)
		for i := 0; i < len(versions)-1; i++ {
			for j := i + 1; j < len(versions); j++ {
				if versions[i].modTime.After(versions[j].modTime) {
					versions[i], versions[j] = versions[j], versions[i]
				}
			}
		}

		// Delete oldest versions
		toDelete := len(versions) - rm.maxVersions
		for i := 0; i < toDelete; i++ {
			dirPath := filepath.Join(versionsDir, versions[i].name)
			if err := os.RemoveAll(dirPath); err != nil {
				return fmt.Errorf("failed to remove old version %s: %w", versions[i].name, err)
			}
		}
	}

	return nil
}
