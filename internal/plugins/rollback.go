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
	"github.com/universaltill/universal-till/internal/data"
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
	repo := data.NewPluginRepo(rm.db)

	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		return []VersionInfo{}, nil
	}

	// Read version directories
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin versions: %w", err)
	}

	// Get current version from database
	currentVersion, _, err := repo.GetActivePluginVersion(ctx, pluginID)
	if err != nil {
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
	repo := data.NewPluginRepo(rm.db)

	// Verify target version exists
	targetPath := filepath.Join(rm.pluginBaseDir, pluginID, "versions", targetVersion)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		return fmt.Errorf("target version %s not found for plugin %s", targetVersion, pluginID)
	}

	// Get current version
	currentVersion, ok, err := repo.GetActivePluginVersion(ctx, pluginID)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}
	if !ok {
		return fmt.Errorf("plugin %s has no active version", pluginID)
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

	// A legacy on-disk manifest can carry payment keys that predate
	// ADR-0031's validation — rolling back must not restore a colliding
	// tender key that a fresh install would reject.
	if err := validatePaymentEntryKeys(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Same protection for page-entry keys (ut-docs#472) — a legacy on-disk
	// manifest can carry a page key that predates this validation and now
	// collides with another currently-installed plugin.
	if err := validatePageEntryKeys(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Same protection for page-entry routes (ut-docs#499) — a legacy
	// on-disk manifest can carry a route that predates this validation and
	// now collides with another currently-installed plugin's page entry.
	if err := validatePageEntryRoutes(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Update plugins table
	if err := repo.UpdatePluginVersion(ctx, tx, pluginID, targetVersion, manifest.Entrypoint, "installed"); err != nil {
		return err
	}

	// Re-insert entries from rolled-back manifest (delete+bulk insert)
	var entryRows []data.PluginEntryRow
	for _, e := range manifest.Entries {
		configJSON := ""
		if len(e.Config) > 0 {
			b, _ := json.Marshal(e.Config)
			configJSON = string(b)
		}
		entryRows = append(entryRows, data.PluginEntryRow{
			ID:            uuid.NewString(),
			Type:          e.Type,
			Key:           e.Key,
			Label:         e.Label,
			IconPath:      e.IconPath,
			SortOrder:     e.SortOrder,
			ParentPageKey: e.ParentPageKey,
			MenuGroup:     e.MenuGroup,
			Route:         e.Route,
			TargetAction:  e.TargetAction,
			TriggerEvent:  e.TriggerEvent,
			ConfigJSON:    configJSON,
		})
	}
	if err := repo.ReplacePluginEntries(ctx, tx, pluginID, entryRows); err != nil {
		return err
	}

	// Create audit log entry
	auditData := map[string]interface{}{
		"plugin_id":       pluginID,
		"from_version":    currentVersion,
		"to_version":      targetVersion,
		"rollback_reason": "user_requested",
	}

	if actorID == "" {
		actorID = "system"
	}

	if err := repo.InsertAudit(ctx, tx, "plugin_rollback", pluginID, auditData, time.Now()); err != nil {
		return err
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
