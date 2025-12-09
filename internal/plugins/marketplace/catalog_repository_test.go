package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

func TestCatalogRepository_FetchAndGet(t *testing.T) {
	// Setup mock client
	mockToken := &mockTokenClient{token: "test-token"}
	// Start mock marketplace server
	mockPlugins := []map[string]interface{}{
		{
			"listing_id":     "test-plugin",
			"name":           "Test Plugin",
			"version":        "1.0.0",
			"artifact_url":   "http://example.com/plugin.tar.gz",
			"sha256":         "deadbeef",
			"canonical_type": "payment",
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": mockPlugins}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	tmpDir := t.TempDir()
	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	snapshot, err := repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if len(snapshot.Plugins) == 0 {
		t.Error("expected plugins in snapshot")
	}

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	repo.staleAfter = 100 * time.Millisecond

	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo1, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	snapshot1, err := repo1.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
			{
				"listing_id":     "other-plugin",
				"name":           "Other Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/other.tar.gz",
				"sha256":         "cafebabe",
				"canonical_type": "report",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Simulate offline by using invalid endpoint
	cfg.EndpointURL = "http://invalid-endpoint:9999"

	snapshot, isStale, err := repo.Get()
	if err != nil {
		t.Fatalf("Get failed when offline: %v", err)
	}

	if len(snapshot.Plugins) == 0 {
		t.Error("expected cached plugins when offline")
	}

	t.Logf("Offline snapshot available, stale=%v", isStale)
}
