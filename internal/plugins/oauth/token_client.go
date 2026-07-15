package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/paths"
)

// TokenResponse represents JWT token response (actual marketplace uses camelCase)
type TokenResponse struct {
	Token      string `json:"token"`     // JWT token
	ExpiresAt  string `json:"expiresAt"` // ISO 8601 timestamp (camelCase in actual API)
	MerchantID string `json:"merchantId,omitempty"`
	DeviceID   string `json:"deviceId,omitempty"`
	// Legacy fields for backward compatibility
	ExpiresAtSnake  string `json:"expires_at,omitempty"` // snake_case fallback
	MerchantIDSnake string `json:"merchant_id,omitempty"`
	DeviceIDSnake   string `json:"device_id,omitempty"`
	AccessToken     string `json:"access_token,omitempty"`
	TokenType       string `json:"token_type,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
	Scope           string `json:"scope,omitempty"`
}

// CachedToken stores token with expiry metadata
type CachedToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Scope     string    `json:"scope"`
}

// TokenProvider defines the interface for obtaining OAuth2 tokens
type TokenProvider interface {
	GetToken(ctx context.Context) (string, error)
	ClearCache() error
}

// TokenClient manages OAuth2 client-credentials tokens with on-disk caching
type TokenClient struct {
	cfg       *config.MarketplaceConfig
	cachePath string
	mu        sync.RWMutex
	cached    *CachedToken
	client    *http.Client
}

// NewTokenClient creates a new OAuth2 token manager
func NewTokenClient(cfg *config.MarketplaceConfig) *TokenClient {
	return &TokenClient{
		cfg:       cfg,
		cachePath: paths.Plugins("auth", "token.json"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetToken returns a valid access token, refreshing if needed
func (tc *TokenClient) GetToken(ctx context.Context) (string, error) {
	tc.mu.RLock()
	if tc.cached != nil && time.Now().Before(tc.cached.ExpiresAt) {
		token := tc.cached.Token
		tc.mu.RUnlock()
		return token, nil
	}
	tc.mu.RUnlock()

	// Need new token
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// Double-check after acquiring write lock
	if tc.cached != nil && time.Now().Before(tc.cached.ExpiresAt) {
		return tc.cached.Token, nil
	}

	// Load from disk cache first
	if cached, err := tc.loadFromDisk(); err == nil && time.Now().Before(cached.ExpiresAt) {
		tc.cached = cached
		return cached.Token, nil
	}

	// Request new token from marketplace
	token, err := tc.requestToken(ctx)
	if err != nil {
		return "", fmt.Errorf("oauth2 token request failed: %w", err)
	}

	// Cache in memory and on disk
	tc.cached = token
	if err := tc.saveToDisk(token); err != nil {
		// Log but don't fail - we have the token
		fmt.Fprintf(os.Stderr, "warning: failed to cache token to disk: %v\n", err)
	}

	return token.Token, nil
}

// requestToken performs JWT merchant token request (matches OpenAPI /v1/auth/merchant-token)
func (tc *TokenClient) requestToken(ctx context.Context) (*CachedToken, error) {
	if tc.cfg.ClientID == "" || tc.cfg.ClientSecret == "" {
		return nil, fmt.Errorf("marketplace OAuth2 credentials not configured")
	}

	// Construct token endpoint URL (OpenAPI spec: /v1/auth/merchant-token)
	tokenURL := strings.TrimSuffix(tc.cfg.EndpointURL, "/") + "/v1/auth/merchant-token"

	// Prepare request body (matches GenerateTokenRequest schema)
	requestBody := map[string]string{
		"merchant_id": tc.cfg.ClientID,
		"device_id":   tc.getDeviceID(),
		"pos_version": "2.5.0", // TODO: Get from config
	}

	bodyJSON, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := tc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request returned %d", resp.StatusCode)
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	// Handle new JWT response format
	token := tokenResp.Token
	if token == "" {
		// Fallback to legacy OAuth2 format
		token = tokenResp.AccessToken
	}

	// Parse expiry (try both camelCase and snake_case for compatibility)
	var expiresAt time.Time
	expiresAtStr := tokenResp.ExpiresAt
	if expiresAtStr == "" {
		expiresAtStr = tokenResp.ExpiresAtSnake
	}
	if expiresAtStr != "" {
		// Parse ISO 8601 timestamp
		expiresAt, err = time.Parse(time.RFC3339, expiresAtStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse expires_at: %w", err)
		}
	} else if tokenResp.ExpiresIn > 0 {
		// Fallback to legacy expires_in (seconds)
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn-300) * time.Second)
	} else {
		// Default: 1 hour expiry
		expiresAt = time.Now().Add(55 * time.Minute)
	}

	return &CachedToken{
		Token:     token,
		ExpiresAt: expiresAt,
		Scope:     tokenResp.Scope,
	}, nil
}

// getDeviceID returns a stable device identifier for marketplace auth flows.
func (tc *TokenClient) getDeviceID() string {
	if tc.cfg.DeviceID != "" {
		return tc.cfg.DeviceID
	}
	// Generate a stable device ID based on hostname
	hostname, _ := os.Hostname()
	if hostname != "" {
		return hostname
	}
	return "pos-device-1"
}

// loadFromDisk reads cached token from disk
func (tc *TokenClient) loadFromDisk() (*CachedToken, error) {
	data, err := os.ReadFile(tc.cachePath)
	if err != nil {
		return nil, err
	}

	var cached CachedToken
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}

	return &cached, nil
}

// saveToDisk persists token to disk securely
func (tc *TokenClient) saveToDisk(token *CachedToken) error {
	// Ensure directory exists
	dir := filepath.Dir(tc.cachePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}

	// Write with restrictive permissions (owner read/write only)
	return os.WriteFile(tc.cachePath, data, 0600)
}

// ClearCache removes cached token (useful for testing or credential rotation)
func (tc *TokenClient) ClearCache() error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.cached = nil
	if err := os.Remove(tc.cachePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
