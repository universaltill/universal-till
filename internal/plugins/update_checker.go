package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// UpdateInfo contains information about an available plugin update
type UpdateInfo struct {
	PluginID        string
	InstalledVersion string
	AvailableVersion string
	Name            string
	Description     string
	ReleaseNotes    string
	ArtifactURL     string
	ArtifactHash    string
	DeviceArch      string
	TrustTier       string
}

// UpdateChecker detects available plugin updates from marketplace
type UpdateChecker struct {
	db          *sql.DB
	catalogRepo *marketplace.CatalogRepository
}

// NewUpdateChecker creates a new update checker
func NewUpdateChecker(db *sql.DB, catalogRepo *marketplace.CatalogRepository) *UpdateChecker {
	return &UpdateChecker{
		db:          db,
		catalogRepo: catalogRepo,
	}
}

// CheckForUpdates compares installed plugins with marketplace catalog and returns available updates
func (uc *UpdateChecker) CheckForUpdates(ctx context.Context) ([]UpdateInfo, error) {
	log := logging.L()

	// Get installed plugins
	installed, err := uc.getInstalledPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get installed plugins: %w", err)
	}

	if len(installed) == 0 {
		return []UpdateInfo{}, nil
	}

	// Get marketplace catalog
	snapshot, _, err := uc.catalogRepo.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to get catalog: %w", err)
	}

	// Build map of marketplace plugins by ID
	catalogMap := make(map[string]marketplace.PluginSummary)
	for _, p := range snapshot.Plugins {
		// Use the plugin's canonical ID (derived from listing)
		// For now, we'll use developer_id + name as a simple key
		// In production, you'd want a proper plugin ID mapping
		key := p.DeveloperID + "/" + p.Name
		
		// Keep the highest version for each plugin
		if existing, ok := catalogMap[key]; ok {
			if compareVersions(p.Version, existing.Version) > 0 {
				catalogMap[key] = p
			}
		} else {
			catalogMap[key] = p
		}
	}

	// Find updates
	var updates []UpdateInfo
	for _, inst := range installed {
		// Try to find in catalog
		key := inst.Author + "/" + inst.Name
		
		if catalogPlugin, ok := catalogMap[key]; ok {
			// Compare versions
			if compareVersions(catalogPlugin.Version, inst.Version) > 0 {
				update := UpdateInfo{
					PluginID:         inst.ID,
					InstalledVersion: inst.Version,
					AvailableVersion: catalogPlugin.Version,
					Name:             catalogPlugin.Name,
					Description:      catalogPlugin.Description,
					ArtifactURL:      catalogPlugin.ArtifactURL,
					ArtifactHash:     strings.TrimPrefix(catalogPlugin.ArtifactHash, "sha256:"),
					DeviceArch:       catalogPlugin.DeviceArch,
					TrustTier:        catalogPlugin.TrustTier,
				}
				updates = append(updates, update)
				
				log.Infof("[UpdateChecker] Update available for %s: %s -> %s", 
					inst.Name, inst.Version, catalogPlugin.Version)
			}
		}
	}

	return updates, nil
}

// installedPlugin represents a plugin installed on the system
type installedPlugin struct {
	ID      string
	Name    string
	Version string
	Author  string
}

// getInstalledPlugins retrieves all active installed plugins
func (uc *UpdateChecker) getInstalledPlugins(ctx context.Context) ([]installedPlugin, error) {
	rows, err := uc.db.QueryContext(ctx, `
		SELECT id, name, version, COALESCE(author, '') 
		FROM plugins 
		WHERE is_active = 1 AND install_state = 'installed'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plugins []installedPlugin
	for rows.Next() {
		var p installedPlugin
		if err := rows.Scan(&p.ID, &p.Name, &p.Version, &p.Author); err != nil {
			return nil, err
		}
		plugins = append(plugins, p)
	}

	return plugins, rows.Err()
}

// compareVersions compares two semantic version strings
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal
func compareVersions(v1, v2 string) int {
	// Simple version comparison (semantic versioning)
	// For production, use a proper semver library
	
	// Remove 'v' prefix if present
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")
	
	// Split into parts
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")
	
	// Pad to same length
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}
	
	for len(parts1) < maxLen {
		parts1 = append(parts1, "0")
	}
	for len(parts2) < maxLen {
		parts2 = append(parts2, "0")
	}
	
	// Compare each part
	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		fmt.Sscanf(parts1[i], "%d", &n1)
		fmt.Sscanf(parts2[i], "%d", &n2)
		
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	
	return 0
}

// GetUpdateInfo retrieves update information for a specific plugin
func (uc *UpdateChecker) GetUpdateInfo(ctx context.Context, pluginID string) (*UpdateInfo, error) {
	updates, err := uc.CheckForUpdates(ctx)
	if err != nil {
		return nil, err
	}

	for _, update := range updates {
		if update.PluginID == pluginID {
			return &update, nil
		}
	}

	return nil, fmt.Errorf("no update available for plugin %s", pluginID)
}
