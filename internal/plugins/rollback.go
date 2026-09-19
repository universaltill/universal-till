package plugins

import (
	"context"
	"database/sql"
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

// VersionInfo contains information about a plugin version. Path is
// deliberately excluded from JSON (json:"-") — it's a local on-disk
// snapshot location, never client-facing data (ut-docs#2239).
type VersionInfo struct {
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
	Path        string    `json:"-"`
	IsActive    bool      `json:"is_active"`
}

// GetVersionHistory retrieves version history for a plugin: every snapshot
// under pluginBaseDir/pluginID/versions/ (the tree StoreVersion writes and
// Rollback reads), flagged with which one is currently active.
//
// Its only caller used to be the sync path's own install-status record
// (ut-docs#1566) — Rollback itself was live via POST /api/plugins/{id}/rollback
// but nothing under web/ui could discover a target version to roll back to.
// GET /api/plugins/{id}/versions (internal/pages/plugin_api.go,
// handleListPluginVersions) now wires this in as that discovery step, and
// web/ui/pages/plugins.html's "Versions" control surfaces it to an operator
// (ut-docs#2239).
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

	// Snapshot the version we're leaving, same as an update does (ut-docs#2239
	// review) — without this, a rollback silently sheds the ability to roll
	// forward again: the version being left was never necessarily stored
	// (e.g. it was itself the very first install, never previously rolled
	// away from), so if this call is skipped a shop that rolls back and then
	// decides the earlier version was wrong too has nowhere to go back to.
	// Only attempt it when the live per-version install directory actually
	// exists — StoreVersion unconditionally clears any existing snapshot
	// before copying, so calling it against a missing source would destroy
	// an already-good prior snapshot instead of leaving it alone. Warn-only
	// on failure either way, same as the update handler's own StoreVersion
	// call — a snapshot failure must never block the rollback the operator
	// is here to complete.
	currentSourcePath := filepath.Join(rm.pluginBaseDir, pluginID, currentVersion)
	if _, statErr := os.Stat(currentSourcePath); statErr == nil {
		if err := rm.StoreVersion(pluginID, currentVersion, currentSourcePath); err != nil {
			log.Warnf("[Rollback] Failed to store version %s for plugin %s before rolling back to %s: %v", currentVersion, pluginID, targetVersion, err)
		}
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

	// Same protection for layout amendments (ADR-0088) — a rolled-back
	// manifest may hide a protected key or restructure a key another
	// plugin has since taken; a fresh install would refuse it, so must this.
	if err := validateLayoutEntries(ctx, repo, tx, pluginID, manifest.Entries); err != nil {
		return fmt.Errorf("rollback to %s rejected: %w", targetVersion, err)
	}

	// Update plugins table
	if err := repo.UpdatePluginVersion(ctx, tx, pluginID, targetVersion, manifest.Entrypoint, "installed"); err != nil {
		return err
	}

	// Re-insert entries from rolled-back manifest (delete+bulk insert)
	var entryRows []data.PluginEntryRow
	for _, e := range manifest.Entries {
		configJSON := entryConfigJSON(e)
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

// StoreVersion saves a plugin version for potential rollback: snapshots the
// live per-version install directory (pluginBaseDir/pluginID/version/, the
// layout installer_marketplace.go's installBundleFile leaves behind) into
// this manager's own versions/ tree (pluginBaseDir/pluginID/versions/
// version/), which Rollback reads from later. sourcePath == "" keeps the
// pre-ut-docs#495 no-op-copy behavior (directory created, nothing to copy —
// some callers store a version marker with no known source location yet).
func (rm *RollbackManager) StoreVersion(pluginID, version, sourcePath string) error {
	log := logging.L()

	versionDir := filepath.Join(rm.pluginBaseDir, pluginID, "versions", version)

	if sourcePath != "" && filepath.Clean(sourcePath) != filepath.Clean(versionDir) {
		// Fresh snapshot every time: an interrupted previous StoreVersion
		// call (partial copy) must never be trusted as complete.
		if err := os.RemoveAll(versionDir); err != nil {
			return fmt.Errorf("failed to clear stale version directory: %w", err)
		}
		if err := copyVersionFiles(sourcePath, versionDir); err != nil {
			_ = os.RemoveAll(versionDir)
			return fmt.Errorf("failed to copy plugin files from %s: %w", sourcePath, err)
		}
	} else if err := os.MkdirAll(versionDir, 0755); err != nil {
		return fmt.Errorf("failed to create version directory: %w", err)
	}

	log.Infof("[Rollback] Stored version %s for plugin %s", version, pluginID)

	// Clean up old versions
	if err := rm.cleanupOldVersions(pluginID); err != nil {
		log.Warnf("[Rollback] Failed to cleanup old versions: %v", err)
	}

	return nil
}

// copyVersionFiles recursively copies sourceDir's tree into destDir,
// preserving relative structure and regular-file permissions.
func copyVersionFiles(sourceDir, destDir string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(sourceDir, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(destDir, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
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
