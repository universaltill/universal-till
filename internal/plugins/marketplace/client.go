package marketplace

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
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
	// Create HTTP client with custom transport for local development
	transport := http.DefaultTransport.(*http.Transport).Clone()

	// Skip TLS verification for localhost/127.0.0.1 (development only)
	if strings.HasPrefix(cfg.EndpointURL, "https://localhost") ||
		strings.HasPrefix(cfg.EndpointURL, "https://127.0.0.1") {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		log.Printf("[WARN] Skipping TLS verification for development endpoint: %s", cfg.EndpointURL)
	}

	return &Client{
		cfg:         cfg,
		tokenClient: tokenClient,
		httpClient: &http.Client{
			Timeout:   time.Duration(cfg.RequestTimeoutSec) * time.Second,
			Transport: transport,
		},
	}
}

// doRequest performs an authenticated HTTP request with API version metadata.
func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	// Auth is optional: when marketplace OAuth2 credentials are not configured
	// (or the marketplace has auth disabled), proceed unauthenticated.
	token, tokenErr := c.tokenClient.GetToken(ctx)
	if tokenErr != nil {
		log.Printf("[DEBUG] proceeding without marketplace auth token: %v", tokenErr)
		token = ""
	}

	endpoint := c.cfg.EndpointURL
	if c.cfg.DevOverrideURL != "" {
		endpoint = c.cfg.DevOverrideURL
	}

	reqURL, err := url.JoinPath(endpoint, path)
	if err != nil {
		return nil, fmt.Errorf("invalid request URL: %w", err)
	}

	// DEBUG: Log the request URL
	log.Printf("[DEBUG] Marketplace request: %s %s", method, reqURL)

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add OAuth2 bearer token when present
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Add API version metadata per FR-016
	req.Header.Set("x-marketplace-api-version", c.cfg.APIVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// DEBUG: Log the response status
	log.Printf("[DEBUG] Marketplace response: %d for %s %s", resp.StatusCode, method, reqURL)

	return resp, nil
}

// ResolveURL turns a possibly-relative marketplace URL (e.g. the bundle_url
// "/api/v1/downloads/artifact/...") into an absolute URL against the configured
// endpoint origin. Absolute inputs are returned unchanged.
func (c *Client) ResolveURL(ref string) (string, error) {
	endpoint := c.cfg.EndpointURL
	if c.cfg.DevOverrideURL != "" {
		endpoint = c.cfg.DevOverrideURL
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid marketplace endpoint %q: %w", endpoint, err)
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("invalid bundle url %q: %w", ref, err)
	}
	return base.ResolveReference(r).String(), nil
}

func DeviceIDFromConfig(cfg *config.MarketplaceConfig) string {
	if cfg == nil {
		return "pos-device-1"
	}
	if cfg.DeviceID != "" {
		return cfg.DeviceID
	}
	hostname, _ := os.Hostname()
	if hostname != "" {
		return hostname
	}
	return "pos-device-1"
}

// CatalogService implements catalog browsing operations.

// ListPluginsRequest matches the proto contract.
type ListPluginsRequest struct {
	Locale     string   `json:"locale,omitempty"`
	DeviceArch string   `json:"device_arch,omitempty"`
	Capability []string `json:"capability,omitempty"`
	PageToken  string   `json:"page_token,omitempty"`
}

// PluginSummary represents a plugin in the catalog (matches OpenAPI schema).
type PluginSummary struct {
	ID            string   `json:"id"`         // UUID
	ListingID     string   `json:"listing_id"` // For backward compatibility
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Vendor        *Vendor  `json:"vendor,omitempty"`
	DeveloperID   string   `json:"developer_id"` // For backward compatibility
	Version       string   `json:"version"`
	TrustTier     string   `json:"trust_tier"`
	Capabilities  []string `json:"capabilities,omitempty"`
	CanonicalType string   `json:"canonical_type"` // For backward compatibility
	PriceFlag     string   `json:"price_flag,omitempty"`
	Architectures []string `json:"architectures,omitempty"`
	DeviceArch    string   `json:"device_arch"` // For backward compatibility
	IconURL       string   `json:"icon_url,omitempty"`
	Rating        float64  `json:"rating,omitempty"`
	ReviewCount   int      `json:"review_count,omitempty"`
	DownloadCount int      `json:"download_count,omitempty"`
	ArtifactURL   string   `json:"artifact_url"`  // For backward compatibility
	ArtifactHash  string   `json:"artifact_hash"` // For backward compatibility
	Locale        string   `json:"locale,omitempty"`
	ApprovedAt    string   `json:"approved_at,omitempty"`
	CreatedAt     string   `json:"created_at,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
}

// Vendor represents plugin vendor info
type Vendor struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email,omitempty"`
	Website    string `json:"website,omitempty"`
	Verified   bool   `json:"verified"`
	VerifiedAt string `json:"verified_at,omitempty"`
}

// ListPluginsResponse contains paginated catalog results (matches OpenAPI schema).
type ListPluginsResponse struct {
	Plugins    []PluginSummary `json:"plugins"`
	Pagination *Pagination     `json:"pagination,omitempty"`
	// Legacy fields for backward compatibility
	NextPageToken   string `json:"next_page_token,omitempty"`
	SnapshotVersion int64  `json:"snapshot_version,omitempty"`
}

// Pagination contains pagination metadata
type Pagination struct {
	Page        int  `json:"page"`
	PageSize    int  `json:"page_size"`
	TotalItems  int  `json:"total_items"`
	TotalPages  int  `json:"total_pages"`
	HasNext     bool `json:"has_next"`
	HasPrevious bool `json:"has_previous"`
}

// ListPlugins fetches the marketplace catalog with optional filters.
func (c *Client) ListPlugins(ctx context.Context, req *ListPluginsRequest) (*ListPluginsResponse, error) {
	// Build query parameters (matches OpenAPI spec)
	params := url.Values{}
	if req.Locale != "" {
		params.Set("locale", req.Locale)
	}
	if req.DeviceArch != "" {
		// Map to 'arch' parameter as per OpenAPI
		params.Set("arch", req.DeviceArch)
	}
	for _, cap := range req.Capability {
		// Map to 'capability' parameter as per OpenAPI
		params.Add("capability", cap)
	}
	// Note: Real API uses page/page_size instead of page_token
	if req.PageToken != "" {
		params.Set("page_token", req.PageToken)
	}

	// Build URL properly with query parameters
	endpoint := c.cfg.EndpointURL
	if c.cfg.DevOverrideURL != "" {
		endpoint = c.cfg.DevOverrideURL
	}

	reqURL, err := url.JoinPath(endpoint, "/v1/catalog/plugins")
	if err != nil {
		return nil, fmt.Errorf("invalid request URL: %w", err)
	}

	// Parse URL and add query params
	parsedURL, err := url.Parse(reqURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}
	parsedURL.RawQuery = params.Encode()

	// Get token
	// Auth is optional: when marketplace OAuth2 credentials are not configured
	// (or the marketplace has auth disabled), proceed unauthenticated.
	token, tokenErr := c.tokenClient.GetToken(ctx)
	if tokenErr != nil {
		log.Printf("[DEBUG] proceeding without marketplace auth token: %v", tokenErr)
		token = ""
	}

	// Create request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("x-marketplace-api-version", c.cfg.APIVersion)

	// DEBUG
	log.Printf("[DEBUG] Marketplace request: GET %s", parsedURL.String())

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[DEBUG] Marketplace response: %d", resp.StatusCode)

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

// IssueDownloadTokenRequest matches the proto contract (legacy fields maintained for compatibility).
type IssueDownloadTokenRequest struct {
	PluginID   string `json:"listing_id"`
	Version    string `json:"version,omitempty"`
	MerchantID string `json:"merchant_id"`
	StoreID    string `json:"store_id"`
	DeviceID   string `json:"device_id"`
	DeviceArch string `json:"device_arch"`
}

type issueDownloadTokenEnvelope struct {
	Data  *IssueDownloadTokenResponse `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// IssueDownloadTokenResponse contains download authorization.
type IssueDownloadTokenResponse struct {
	Token              string `json:"token"`
	BundleURL          string `json:"bundle_url"`
	ReleaseID          string `json:"release_id"`
	Version            string `json:"version"`
	ChecksumSHA256     string `json:"checksum_sha256"`
	Signature          string `json:"signature"`
	ExpiresAt          string `json:"expires_at"`
	ResumableSupported bool   `json:"resumable_supported"`
	ResumableURL       string `json:"resumable_url"`
}

// IssueDownloadToken requests a pre-signed download URL for a plugin.
func (c *Client) IssueDownloadToken(ctx context.Context, req *IssueDownloadTokenRequest) (*IssueDownloadTokenResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal download request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/downloads/tokens", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download token request failed: status %d", resp.StatusCode)
	}

	var envelope issueDownloadTokenEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("failed to decode download token response: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("download token request failed: %s", envelope.Error.Message)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("download token request failed: missing response data")
	}
	return envelope.Data, nil
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

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/download/ack", bytes.NewReader(body))
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
	endpoint := c.cfg.EndpointURL
	if c.cfg.DevOverrideURL != "" {
		endpoint = c.cfg.DevOverrideURL
	}

	reqURL, err := url.JoinPath(endpoint, "/v1/revocations")
	if err != nil {
		return nil, fmt.Errorf("invalid request URL: %w", err)
	}

	parsedURL, err := url.Parse(reqURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	query := parsedURL.Query()
	query.Set("since_version", fmt.Sprintf("%d", req.SinceVersion))
	parsedURL.RawQuery = query.Encode()

	// Auth is optional: when marketplace OAuth2 credentials are not configured
	// (or the marketplace has auth disabled), proceed unauthenticated.
	token, tokenErr := c.tokenClient.GetToken(ctx)
	if tokenErr != nil {
		log.Printf("[DEBUG] proceeding without marketplace auth token: %v", tokenErr)
		token = ""
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("x-marketplace-api-version", c.cfg.APIVersion)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
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

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/telemetry/status", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("telemetry report failed: status %d", resp.StatusCode)
	}

	return nil
}
