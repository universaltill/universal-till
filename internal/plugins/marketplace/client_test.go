package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

func TestClient_ListPlugins(t *testing.T) {
	// Mock marketplace server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing or invalid auth header")
		}
		// Verify API version header
		if r.Header.Get("x-marketplace-api-version") != "1.0.0" {
			t.Errorf("missing or invalid API version header")
		}

		resp := ListPluginsResponse{
			Plugins: []PluginSummary{
				{
					ListingID:     "550e8400-e29b-41d4-a716-446655440001",
					Name:          "Test Plugin",
					Version:       "1.0.0",
					CanonicalType: "page",
					DeveloperID:   "test-dev",
					TrustTier:     "verified",
					ArtifactURL:   "https://example.com/plugin.tar.gz",
					ArtifactHash:   "abc123",
					Locale:         "en-US",
					DeviceArch:     "linux/amd64",
					ApprovedAt:    "2024-01-01T00:00:00Z",
				},
			},
			SnapshotVersion: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Setup client
	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		ClientID:          "test",
		ClientSecret:      "secret",
		RequestTimeoutSec: 30,
	}

	// Mock token client
	tokenClient := &mockTokenClient{token: "test-token"}
	client := NewClient(cfg, tokenClient)

	// Test ListPlugins
	ctx := context.Background()
	req := &ListPluginsRequest{
		DeviceArch: "linux/amd64",
	}
	resp, err := client.ListPlugins(ctx, req)
	if err != nil {
		t.Fatalf("ListPlugins failed: %v", err)
	}

	if len(resp.Plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(resp.Plugins))
	}
	if resp.Plugins[0].ListingID != "550e8400-e29b-41d4-a716-446655440001" {
		t.Errorf("unexpected plugin listing ID: %s", resp.Plugins[0].ListingID)
	}
}

func TestClient_ReportPluginStatus_OptOut(t *testing.T) {
	// Setup client with telemetry disabled
	cfg := &config.MarketplaceConfig{
		EndpointURL:       "http://example.com",
		TelemetryOptIn:    false,
		RequestTimeoutSec: 30,
	}
	tokenClient := &mockTokenClient{token: "test-token"}
	client := NewClient(cfg, tokenClient)

	// Should not error when telemetry is disabled
	ctx := context.Background()
	req := &ReportPluginStatusRequest{
		Statuses: []PluginStatus{
			{PluginID: "test", InstalledVersion: "1.0.0"},
		},
	}
	err := client.ReportPluginStatus(ctx, req)
	if err != nil {
		t.Errorf("expected no error when telemetry is disabled, got: %v", err)
	}
}

// mockTokenClient implements oauth.TokenClient interface for testing
type mockTokenClient struct {
	token string
}

func (m *mockTokenClient) GetToken(ctx context.Context) (string, error) {
	return m.token, nil
}

func (m *mockTokenClient) ClearCache() error {
	return nil
}
