package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
)

// Client provides HTTP/REST access to marketplace services.
// For production deployments, this can be replaced with gRPC implementations.
type Client struct {
	cfg         *config.MarketplaceConfig
	tokenClient oauth.TokenProvider
	httpClient  *http.Client
}

// NewClient creates a marketplace client with OAuth2 authentication.
func NewClient(cfg *config.MarketplaceConfig, tokenClient oauth.TokenProvider) *Client {
	return &Client{
		cfg:         cfg,
		tokenClient: tokenClient,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.RequestTimeoutSec) * time.Second,
		},
	}
}

// doRequest performs an authenticated HTTP request with API version metadata.
func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	token, err := c.tokenClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.cfg.EndpointURL
	if c.cfg.DevOverrideURL != "" {
		endpoint = c.cfg.DevOverrideURL
	}

	reqURL, err := url.JoinPath(endpoint, path)
	if err != nil {
		return nil, fmt.Errorf("invalid request URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add OAuth2 bearer token
	req.Header.Set("Authorization", "Bearer "+token)
	// Add API version metadata per FR-016
	req.Header.Set("x-marketplace-api-version", c.cfg.APIVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// CatalogService implements catalog browsing operations.

// ListPluginsRequest matches the proto contract.
type ListPluginsRequest struct {
	Locale       string   `json:"locale,omitempty"`
	DeviceArch   string   `json:"device_arch,omitempty"`
	Capability   []string `json:"capability,omitempty"`
	PageToken    string   `json:"page_token,omitempty"`
}

// PluginSummary represents a plugin in the catalog.
type PluginSummary struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Version              string   `json:"version"`
	Type                 string   `json:"type"`
	Vendor               string   `json:"vendor"`
	TrustLevel           string   `json:"trust_level"`
	RequiredCapabilities []string `json:"required_capabilities"`
	MinHostVersion       string   `json:"min_host_version"`
	Description          string   `json:"description"`
	IconURL              string   `json:"icon_url"`
	PaidListing          bool     `json:"paid_listing"`
}

// ListPluginsResponse contains paginated catalog results.
type ListPluginsResponse struct {
	Plugins         []PluginSummary `json:"plugins"`
	NextPageToken   string          `json:"next_page_token,omitempty"`
	SnapshotVersion int64           `json:"snapshot_version"`
}

// ListPlugins fetches the marketplace catalog with optional filters.
func (c *Client) ListPlugins(ctx context.Context, req *ListPluginsRequest) (*ListPluginsResponse, error) {
	// Build query parameters
	params := url.Values{}
	if req.Locale != "" {
		params.Set("locale", req.Locale)
	}
	if req.DeviceArch != "" {
		params.Set("device_arch", req.DeviceArch)
	}
	for _, cap := range req.Capability {
		params.Add("capability", cap)
	}
	if req.PageToken != "" {
		params.Set("page_token", req.PageToken)
	}

	path := "/api/v1/catalog/plugins"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog request failed: status %d", resp.StatusCode)
	}

	var result ListPluginsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode catalog response: %w", err)
	}

	return &result, nil
}

// DownloadService implements plugin download operations.

// IssueDownloadTokenRequest matches the proto contract.
type IssueDownloadTokenRequest struct {
	PluginID   string `json:"plugin_id"`
	Version    string `json:"version"`
	DeviceID   string `json:"device_id"`
	DeviceArch string `json:"device_arch"`
}

// IssueDownloadTokenResponse contains download authorization.
type IssueDownloadTokenResponse struct {
	ArtifactURL   string `json:"artifact_url"`
	Token         string `json:"token"`
	ExpiresAtUnix int64  `json:"expires_at_unix"`
	SHA256        []byte `json:"sha256"`
	Signature     string `json:"signature"`
}

// IssueDownloadToken requests a time-limited download token for a plugin.
func (c *Client) IssueDownloadToken(ctx context.Context, req *IssueDownloadTokenRequest) (*IssueDownloadTokenResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/downloads/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download token request failed: status %d", resp.StatusCode)
	}

	var result IssueDownloadTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode download token response: %w", err)
	}

	return &result, nil
}

// AckDownloadRequest reports download outcome.
type AckDownloadRequest struct {
	PluginID      string `json:"plugin_id"`
	Version       string `json:"version"`
	Token         string `json:"token"`
	Success       bool   `json:"success"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// AckDownload acknowledges a completed or failed download.
func (c *Client) AckDownload(ctx context.Context, req *AckDownloadRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/downloads/ack", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("download ack failed: status %d", resp.StatusCode)
	}

	return nil
}

// GetRevocationsRequest requests revocation updates.
type GetRevocationsRequest struct {
	SinceVersion int64 `json:"since_version"`
}

// Revocation represents a plugin revocation action.
type Revocation struct {
	PluginID string `json:"plugin_id"`
	Version  string `json:"version"`
	Action   string `json:"action"` // disable | delete
	Reason   string `json:"reason"`
}

// GetRevocationsResponse contains revocation feed.
type GetRevocationsResponse struct {
	Revocations   []Revocation `json:"revocations"`
	LatestVersion int64        `json:"latest_version"`
}

// GetRevocations fetches plugin revocations since a given version.
func (c *Client) GetRevocations(ctx context.Context, req *GetRevocationsRequest) (*GetRevocationsResponse, error) {
	path := fmt.Sprintf("/api/v1/revocations?since_version=%d", req.SinceVersion)

	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("revocations request failed: status %d", resp.StatusCode)
	}

	var result GetRevocationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode revocations response: %w", err)
	}

	return &result, nil
}

// TelemetryService implements telemetry reporting.

// PluginStatus represents installed plugin state.
type PluginStatus struct {
	PluginID         string `json:"plugin_id"`
	InstalledVersion string `json:"installed_version"`
	Status           string `json:"status"` // enabled | disabled | revoked | failed
	Source           string `json:"source"` // cloud | manual
	DeviceID         string `json:"device_id"`
}

// ReportPluginStatusRequest contains plugin status updates.
type ReportPluginStatusRequest struct {
	Statuses []PluginStatus `json:"statuses"`
}

// ReportPluginStatus sends plugin installation state to the marketplace.
func (c *Client) ReportPluginStatus(ctx context.Context, req *ReportPluginStatusRequest) error {
	if !c.cfg.TelemetryOptIn {
		// Silently skip telemetry if not opted in
		return nil
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/telemetry/status", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("telemetry report failed: status %d", resp.StatusCode)
	}

	return nil
}
