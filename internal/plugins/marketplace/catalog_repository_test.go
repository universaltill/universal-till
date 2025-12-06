package marketplace

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

func TestCatalogRepository_FetchAndGet(t *testing.T) {
	// Setup mock client
	mockToken := &mockTokenClient{token: "test-token"}
	cfg := &config.MarketplaceConfig{
		EndpointURL:       "http://localhost:8082",
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	// Create repository
	tmpDir := t.TempDir()
	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	// Fetch catalog
	ctx := context.Background()
	snapshot, err := repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if len(snapshot.Plugins) == 0 {
		t.Error("expected plugins in snapshot")
	}

	// Get from cache
	cached, isStale, err := repo.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if isStale {
		t.Error("fresh snapshot marked as stale")
	}

	if len(cached.Plugins) != len(snapshot.Plugins) {
		t.Errorf("cached plugin count mismatch: %d != %d", len(cached.Plugins), len(snapshot.Plugins))
	}
}

func TestCatalogRepository_StaleDetection(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	cfg := &config.MarketplaceConfig{
		EndpointURL:       "http://localhost:8082",
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	// Override staleAfter for testing
	repo.staleAfter = 100 * time.Millisecond

	// Fetch catalog
	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Wait for staleness
	time.Sleep(150 * time.Millisecond)

	// Check stale marker
	_, isStale, err := repo.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if !isStale {
		t.Error("expected catalog to be marked as stale")
	}
}

func TestCatalogRepository_DiskPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	cfg := &config.MarketplaceConfig{
		EndpointURL:       "http://localhost:8082",
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	// First repository - fetch and save
	repo1, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	snapshot1, err := repo1.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Second repository - should load from disk
	repo2, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	snapshot2, _, err := repo2.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if snapshot2.SnapshotVersion != snapshot1.SnapshotVersion {
		t.Error("snapshot version mismatch after disk reload")
	}
}

func TestCatalogRepository_Filter(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	cfg := &config.MarketplaceConfig{
		EndpointURL:       "http://localhost:8082",
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	// Fetch catalog
	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Filter by type
	filtered, err := repo.Filter("payment", "", "")
	if err != nil {
		t.Fatalf("Filter failed: %v", err)
	}

	for _, p := range filtered {
		if p.CanonicalType != "payment" {
			t.Errorf("unexpected plugin type in filtered results: %s", p.CanonicalType)
		}
	}
}

func TestCatalogRepository_OfflineReplay(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	cfg := &config.MarketplaceConfig{
		EndpointURL:       "http://localhost:8082",
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	// Fetch while online
	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Simulate offline by using invalid endpoint
	cfg.EndpointURL = "http://invalid-endpoint:9999"

	// Should still get cached data
	snapshot, isStale, err := repo.Get()
	if err != nil {
		t.Fatalf("Get failed when offline: %v", err)
	}

	if len(snapshot.Plugins) == 0 {
		t.Error("expected cached plugins when offline")
	}

	// Snapshot should be available even if stale
	t.Logf("Offline snapshot available, stale=%v", isStale)
}
