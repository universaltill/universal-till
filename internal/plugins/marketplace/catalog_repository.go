package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CatalogSnapshot represents a cached marketplace catalog
type CatalogSnapshot struct {
	Plugins         []PluginSummary `json:"plugins"`
	SnapshotVersion int64           `json:"snapshot_version"`
	FetchedAt       time.Time       `json:"fetched_at"`
	Locale          string          `json:"locale"`
	DeviceArch      string          `json:"device_arch"`
}

// CatalogRepository manages on-disk catalog snapshots with stale markers
type CatalogRepository struct {
	client       *Client
	snapshotPath string
	mu           sync.RWMutex
	cached       *CatalogSnapshot
	staleAfter   time.Duration
}

// NewCatalogRepository creates a catalog repository
func NewCatalogRepository(client *Client, cacheDir string) (*CatalogRepository, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache dir: %w", err)
	}

	return &CatalogRepository{
		client:       client,
		snapshotPath: filepath.Join(cacheDir, "catalog-snapshot.json"),
		staleAfter:   15 * time.Minute,
	}, nil
}

// Fetch retrieves the latest catalog from the marketplace
func (cr *CatalogRepository) Fetch(ctx context.Context, locale, deviceArch string) (*CatalogSnapshot, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	req := &ListPluginsRequest{
		Locale:     locale,
		DeviceArch: deviceArch,
	}

	resp, err := cr.client.ListPlugins(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch catalog: %w", err)
	}

	snapshot := &CatalogSnapshot{
		Plugins:         resp.Plugins,
		SnapshotVersion: resp.SnapshotVersion,
		FetchedAt:       time.Now(),
		Locale:          locale,
		DeviceArch:      deviceArch,
	}

	// Save to disk
	if err := cr.saveSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("failed to save snapshot: %w", err)
	}

	cr.cached = snapshot
	return snapshot, nil
}

// Get returns the cached catalog, marking it as stale if expired
func (cr *CatalogRepository) Get() (*CatalogSnapshot, bool, error) {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	// Try memory cache first
	if cr.cached != nil {
		isStale := time.Since(cr.cached.FetchedAt) > cr.staleAfter
		return cr.cached, isStale, nil
	}

	// Load from disk
	snapshot, err := cr.loadSnapshot()
	if err != nil {
		return nil, false, err
	}

	if snapshot == nil {
		return nil, false, fmt.Errorf("no catalog snapshot available")
	}

	isStale := time.Since(snapshot.FetchedAt) > cr.staleAfter
	return snapshot, isStale, nil
}

// GetOrFetch returns cached catalog or fetches fresh if unavailable
func (cr *CatalogRepository) GetOrFetch(ctx context.Context, locale, deviceArch string) (*CatalogSnapshot, bool, error) {
	// Try to get cached first
	snapshot, isStale, err := cr.Get()
	if err == nil {
		return snapshot, isStale, nil
	}

	// No cache available, fetch fresh
	snapshot, err = cr.Fetch(ctx, locale, deviceArch)
	if err != nil {
		return nil, false, err
	}

	return snapshot, false, nil
}

// Filter returns plugins matching the given criteria
func (cr *CatalogRepository) Filter(pluginType, vendor, trustLevel string) ([]PluginSummary, error) {
	snapshot, _, err := cr.Get()
	if err != nil {
		return nil, err
	}

	var filtered []PluginSummary
	for _, p := range snapshot.Plugins {
		match := true

		if pluginType != "" && p.Type != pluginType {
			match = false
		}
		if vendor != "" && p.Vendor != vendor {
			match = false
		}
		if trustLevel != "" && p.TrustLevel != trustLevel {
			match = false
		}

		if match {
			filtered = append(filtered, p)
		}
	}

	return filtered, nil
}

// saveSnapshot writes the snapshot to disk
func (cr *CatalogRepository) saveSnapshot(snapshot *CatalogSnapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cr.snapshotPath, data, 0644)
}

// loadSnapshot reads the snapshot from disk
func (cr *CatalogRepository) loadSnapshot() (*CatalogSnapshot, error) {
	data, err := os.ReadFile(cr.snapshotPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var snapshot CatalogSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}

	return &snapshot, nil
}
