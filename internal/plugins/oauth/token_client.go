package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

// TokenResponse represents OAuth2 token response
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// CachedToken stores token with expiry metadata
type CachedToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Scope     string    `json:"scope"`
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
		cachePath: "./data/plugins/auth/token.json",
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

// requestToken performs OAuth2 client-credentials flow
func (tc *TokenClient) requestToken(ctx context.Context) (*CachedToken, error) {
	if tc.cfg.ClientID == "" || tc.cfg.ClientSecret == "" {
		return nil, fmt.Errorf("marketplace OAuth2 credentials not configured")
	}

	// Construct token endpoint URL
	tokenURL := strings.TrimSuffix(tc.cfg.EndpointURL, "/") + "/oauth/token"

	// Prepare request body
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", tc.cfg.ClientID)
	data.Set("client_secret", tc.cfg.ClientSecret)
	data.Set("scope", "marketplace:read marketplace:install marketplace:telemetry")

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

	// Calculate expiry with 5-minute safety margin
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn-300) * time.Second)

	return &CachedToken{
		Token:     tokenResp.AccessToken,
		ExpiresAt: expiresAt,
		Scope:     tokenResp.Scope,
	}, nil
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
