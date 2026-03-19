package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

func TestTokenClient_GetToken_Success(t *testing.T) {
	// Setup mock OAuth server

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/merchant-token" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var reqBody map[string]string
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if reqBody["merchant_id"] != "test-client" {
			t.Errorf("expected merchant_id 'test-client', got %s", reqBody["merchant_id"])
		}
		if reqBody["device_id"] != "test-device-1" {
			t.Errorf("expected device_id 'test-device-1', got %s", reqBody["device_id"])
		}
		resp := TokenResponse{
			Token:     "test-token-12345",
			ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			Scope:     "marketplace:read",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Setup client with temp cache
	tmpDir := t.TempDir()
	cfg := &config.MarketplaceConfig{
		EndpointURL:  server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		DeviceID:     "test-device-1",
	}
	client := NewTokenClient(cfg)
	client.cachePath = filepath.Join(tmpDir, "token.json")

	// Test getting token
	ctx := context.Background()
	token, err := client.GetToken(ctx)
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if token != "test-token-12345" {
		t.Errorf("expected token test-token-12345, got %s", token)
	}

	// Verify cached in memory
	if client.cached == nil {
		t.Error("token not cached in memory")
	}

	// Verify cached on disk
	data, err := os.ReadFile(client.cachePath)
	if err != nil {
		t.Fatalf("failed to read cache file: %v", err)
	}
	var cached CachedToken
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatalf("failed to unmarshal cache: %v", err)
	}
	if cached.Token != "test-token-12345" {
		t.Errorf("cached token mismatch: got %s", cached.Token)
	}
}

func TestTokenClient_GetToken_UsesCache(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := TokenResponse{
			AccessToken: "cached-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cfg := &config.MarketplaceConfig{
		EndpointURL:  server.URL,
		ClientID:     "test",
		ClientSecret: "secret",
	}
	client := NewTokenClient(cfg)
	client.cachePath = filepath.Join(tmpDir, "token.json")

	ctx := context.Background()
	// First call - should hit server
	token1, err := client.GetToken(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Second call - should use cache
	token2, err := client.GetToken(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if token1 != token2 {
		t.Errorf("tokens don't match: %s != %s", token1, token2)
	}
	if callCount != 1 {
		t.Errorf("expected 1 server call, got %d", callCount)
	}
}

func TestTokenClient_GetToken_MissingCredentials(t *testing.T) {
	cfg := &config.MarketplaceConfig{
		EndpointURL: "http://example.com",
		// ClientID and ClientSecret intentionally empty
	}
	client := NewTokenClient(cfg)
	ctx := context.Background()

	_, err := client.GetToken(ctx)
	if err == nil {
		t.Error("expected error for missing credentials")
	}
	if err.Error() != "oauth2 token request failed: marketplace OAuth2 credentials not configured" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTokenClient_ClearCache(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "token.json")
	client := &TokenClient{
		cachePath: cachePath,
		cached: &CachedToken{
			Token:     "test",
			ExpiresAt: time.Now().Add(1 * time.Hour),
		},
	}

	// Save to disk
	if err := client.saveToDisk(client.cached); err != nil {
		t.Fatal(err)
	}

	// Clear cache
	if err := client.ClearCache(); err != nil {
		t.Fatal(err)
	}

	// Verify memory cache cleared
	if client.cached != nil {
		t.Error("memory cache not cleared")
	}

	// Verify disk cache removed
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Error("disk cache file still exists")
	}
}
