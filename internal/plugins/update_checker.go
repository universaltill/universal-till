package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// UpdateInfo contains information about an available plugin update
type UpdateInfo struct {
	PluginID         string
	InstalledVersion string
	AvailableVersion string
	Name             string
	Description      string
	ReleaseNotes     string
	ArtifactURL      string
	ArtifactHash     string
	DeviceArch       string
	TrustTier        string
	// CanonicalType is the marketplace listing's plugin type (ADR-0002's
	// 22-type taxonomy, e.g. "language", "theme") — carried through so a
	// caller (StartPluginUpdateScheduler, ut-docs#1953) can decide whether
	// this update is safe to auto-apply without a merchant's say-so. Not
	// persisted anywhere locally; it comes from the catalog snapshot only.
	CanonicalType string
}

// UpdateChecker detects available plugin updates from marketplace
type UpdateChecker struct {
	db          *sql.DB
	catalogRepo *marketplace.CatalogRepository
	repo        *data.PluginRepo
}

// NewUpdateChecker creates a new update checker
func NewUpdateChecker(db *sql.DB, catalogRepo *marketplace.CatalogRepository) *UpdateChecker {
	return &UpdateChecker{
		db:          db,
		catalogRepo: catalogRepo,
		repo:        data.NewPluginRepo(db),
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

	// Index the catalog and resolve each installed plugin via the same
	// two-tier byListing/byAuthorName match /plugins' management page
	// uses (extracted to catalog_match.go, ut-docs#2131, so the two call
	// sites can't drift on this question the way they used to).
	idx := IndexCatalog(snapshot)

	// Installed plugin id → the listing it was installed from.
	listingByPlugin := make(map[string]string)
	if records, err := NewInstallStatusStore(uc.db).List(ctx); err != nil {
		// Non-fatal: fall back to the author+name heuristic for everything.
		log.Warnf("[UpdateChecker] install-status listing map unavailable, falling back to author/name matching: %v", err)
	} else {
		for listingID, record := range records {
			if record.PluginID != "" {
				listingByPlugin[record.PluginID] = listingID
			}
		}
	}

	// Find updates
	var updates []UpdateInfo
	for _, inst := range installed {
		catalogPlugin, ok := idx.Resolve(listingByPlugin[inst.ID], inst.Author, inst.Name)

		if ok {
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
					CanonicalType:    catalogPlugin.CanonicalType,
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
	rows, err := uc.repo.ListInstalledPlugins(ctx)
	if err != nil {
		return nil, err
	}
	var plugins []installedPlugin
	for _, r := range rows {
		plugins = append(plugins, installedPlugin{
			ID:      r.ID,
			Name:    r.Name,
			Version: r.Version,
			Author:  r.Author,
		})
	}
	return plugins, nil
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
