package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
					ArtifactHash:  "abc123",
					Locale:        "en-US",
					DeviceArch:    "linux/amd64",
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

func TestClient_IssueDownloadToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/downloads/tokens" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}

		var req IssueDownloadTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.MerchantID != "merchant-1" || req.StoreID != "store-1" || req.DeviceID != "device-1" {
			t.Fatalf("unexpected request payload %+v", req)
		}

		var body bytes.Buffer
		if err := json.NewEncoder(&body).Encode(issueDownloadTokenEnvelope{
			Data: &IssueDownloadTokenResponse{
				Token:          "tok-1",
				BundleURL:      "http://example.test/plugin.tar.gz",
				ReleaseID:      "release-1",
				Version:        "1.2.3",
				ChecksumSHA256: "abc123",
				Signature:      "sig123",
				ExpiresAt:      "2026-03-16T16:00:00Z",
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body.Bytes())
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		ClientID:          "test",
		ClientSecret:      "secret",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, &mockTokenClient{token: "test-token"})

	resp, err := client.IssueDownloadToken(context.Background(), &IssueDownloadTokenRequest{
		PluginID:   "listing-1",
		Version:    "1.2.3",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err != nil {
		t.Fatalf("IssueDownloadToken failed: %v", err)
	}
	if resp.BundleURL != "http://example.test/plugin.tar.gz" {
		t.Fatalf("unexpected bundle url %q", resp.BundleURL)
	}
	if resp.Signature != "sig123" {
		t.Fatalf("unexpected signature %q", resp.Signature)
	}
}

// TestClient_IssueDownloadToken_NotEntitled pins the paid-listing gate
// (ut-docs#673): ut-cloud's downloadsvc answers a blocked download with HTTP
// 403 and a {code:"not_entitled", message:"..."} envelope, not a 200. Before
// this test the client discarded the body entirely on any non-200 status —
// the specific reason (and even the plain message) never reached the caller,
// only a generic "status 403". Callers need the code to tell "this plugin
// needs approval" apart from "the marketplace is broken."
func TestClient_IssueDownloadToken_NotEntitled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null,"error":{"code":"not_entitled","message":"store is not entitled to download this plugin"}}`))
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		ClientID:          "test",
		ClientSecret:      "secret",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, &mockTokenClient{token: "test-token"})

	_, err := client.IssueDownloadToken(context.Background(), &IssueDownloadTokenRequest{
		PluginID:   "listing-1",
		MerchantID: "merchant-1",
		StoreID:    "store-1",
		DeviceID:   "device-1",
		DeviceArch: "linux/amd64",
	})
	if err == nil {
		t.Fatal("expected an error for a not_entitled response")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "not_entitled" {
		t.Errorf("Code = %q, want %q", apiErr.Code, "not_entitled")
	}
	if apiErr.Message != "store is not entitled to download this plugin" {
		t.Errorf("Message = %q", apiErr.Message)
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

// TestPluginSummary_DecodesLiveWireFormat pins the flattened camelCase schema
// the deployed marketplace actually returns from /v1/catalog/plugins (vendor is
// a plain string, id doubles as the listing id, type/trustLevel are camelCase).
// A previous regression: Vendor was typed as an object, so decoding the live
// response failed and the POS plugins page rendered empty.
func TestPluginSummary_DecodesLiveWireFormat(t *testing.T) {
	live := []byte(`{
		"plugins":[{
			"id":"1295b44c-8226-4115-b8c8-4a02733b780d",
			"name":"Universal Till FAQ",
			"version":"0.1.2",
			"type":"page",
			"vendor":"unassigned",
			"trustLevel":"unverified",
			"requiredCapabilities":["ui.page"],
			"minHostVersion":"",
			"description":"Universal Till FAQ",
			"iconUrl":"https://x/icon.png",
			"paidListing":false
		}],
		"nextPageToken":"",
		"snapshotVersion":1783463941
	}`)

	var resp ListPluginsResponse
	if err := json.Unmarshal(live, &resp); err != nil {
		t.Fatalf("decode live wire format: %v", err)
	}
	if len(resp.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(resp.Plugins))
	}
	p := resp.Plugins[0]
	if p.ID != "1295b44c-8226-4115-b8c8-4a02733b780d" {
		t.Errorf("ID = %q", p.ID)
	}
	if p.ListingID != p.ID {
		t.Errorf("ListingID should fall back to id, got %q", p.ListingID)
	}
	if p.Vendor != "unassigned" || p.DeveloperID != "unassigned" {
		t.Errorf("vendor mapping: Vendor=%q DeveloperID=%q", p.Vendor, p.DeveloperID)
	}
	if p.CanonicalType != "page" {
		t.Errorf("CanonicalType = %q, want page (from 'type')", p.CanonicalType)
	}
	if p.TrustTier != "unverified" {
		t.Errorf("TrustTier = %q, want unverified (from 'trustLevel')", p.TrustTier)
	}
	if len(p.Capabilities) != 1 || p.Capabilities[0] != "ui.page" {
		t.Errorf("Capabilities = %v (from requiredCapabilities)", p.Capabilities)
	}
	if p.IconURL != "https://x/icon.png" {
		t.Errorf("IconURL = %q (from iconUrl)", p.IconURL)
	}
}

// TestPluginSummary_DecodesLegacySnakeCase keeps the older rich schema working
// (listing_id, canonical_type, trust_tier, snake_case URLs).
func TestPluginSummary_DecodesLegacySnakeCase(t *testing.T) {
	legacy := []byte(`{
		"listing_id":"L1","name":"X","version":"1.0.0",
		"canonical_type":"payment","trust_tier":"verified",
		"developer_id":"dev-1","artifact_url":"https://a","artifact_hash":"h",
		"icon_url":"https://i","device_arch":"linux/amd64","paid_listing":true
	}`)
	var p PluginSummary
	if err := json.Unmarshal(legacy, &p); err != nil {
		t.Fatalf("decode legacy: %v", err)
	}
	if p.ListingID != "L1" || p.ID != "L1" {
		t.Errorf("ids: ID=%q ListingID=%q", p.ID, p.ListingID)
	}
	if p.CanonicalType != "payment" || p.TrustTier != "verified" || p.DeveloperID != "dev-1" {
		t.Errorf("legacy fields: %+v", p)
	}
	if p.ArtifactURL != "https://a" || p.ArtifactHash != "h" || p.IconURL != "https://i" {
		t.Errorf("legacy urls: %+v", p)
	}
	if !p.PaidListing {
		t.Error("paid_listing not mapped")
	}
}
