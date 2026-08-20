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
	// cloudClient always talks to cfg.EndpointURL, on its own transport —
	// its TLS verification is never affected by dev-override configuration
	// or activity. devClient is nil unless a syntactically valid dev
	// override URL is configured; it has its own transport (TLS bypass only
	// when the override itself is https) and is never used for cloud
	// traffic. Two separate *http.Client/transport pairs, not a shared one
	// with conditional settings — a previous version shared one transport
	// and set InsecureSkipVerify on it whenever DevMode+DevOverrideURL were
	// merely *configured*, which silently disabled TLS verification for
	// cloud requests too whenever the override existed, even if it failed
	// its health check and was never actually used.
	cloudClient *http.Client
	devClient   *http.Client
	// devOverrideActive is decided once at construction: DevMode must be on,
	// DevOverrideURL must parse as a real http(s) URL, and it must pass a
	// bounded startup health check (GET /healthz). A dev-only escape hatch
	// must never silently redirect production traffic (FR-015) — any of
	// those failing means every request uses the real cloud endpoint for
	// this Client's lifetime.
	devOverrideActive bool
}

// NewClient creates a marketplace client with OAuth2 authentication.
func NewClient(cfg *config.MarketplaceConfig, tokenClient oauth.TokenProvider) *Client {
	cloudTransport := http.DefaultTransport.(*http.Transport).Clone()
	// Skip TLS verification for localhost/127.0.0.1 (development only) —
	// this is about EndpointURL itself pointing at a local dev marketplace,
	// unrelated to DevOverrideURL.
	if isLocalHTTPS(cfg.EndpointURL) {
		cloudTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		log.Printf("[WARN] Skipping TLS verification for development endpoint: %s", cfg.EndpointURL)
	}
	cloudClient := &http.Client{
		Timeout:   time.Duration(cfg.RequestTimeoutSec) * time.Second,
		Transport: cloudTransport,
	}

	var devClient *http.Client
	devOverrideActive := false
	if cfg.DevMode && cfg.DevOverrideURL != "" {
		switch {
		case !isValidHTTPURL(cfg.DevOverrideURL):
			log.Printf("[WARN] marketplace dev override URL is invalid, ignoring: %q", cfg.DevOverrideURL)
		default:
			devTransport := http.DefaultTransport.(*http.Transport).Clone()
			if strings.HasPrefix(cfg.DevOverrideURL, "https://") {
				// A dev override is very likely a LAN box with a self-signed
				// cert; this transport is dedicated to the override, never
				// shared with cloudClient, so bypassing verification here
				// can never affect a real cloud request.
				devTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				log.Printf("[WARN] Skipping TLS verification for dev override endpoint: %s", cfg.DevOverrideURL)
			}
			devClient = &http.Client{
				Timeout:   time.Duration(cfg.RequestTimeoutSec) * time.Second,
				Transport: devTransport,
			}

			devOverrideActive = devOverrideHealthy(devClient, cfg.DevOverrideURL, healthCheckTimeout(cfg))
			if devOverrideActive {
				log.Printf("[INFO] marketplace dev override active: %s", cfg.DevOverrideURL)
			} else {
				log.Printf("[WARN] marketplace dev override %s failed its health check, using %s instead", cfg.DevOverrideURL, cfg.EndpointURL)
			}
		}
	}

	return &Client{
		cfg:               cfg,
		tokenClient:       tokenClient,
		cloudClient:       cloudClient,
		devClient:         devClient,
		devOverrideActive: devOverrideActive,
	}
}

func isLocalHTTPS(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://localhost") || strings.HasPrefix(rawURL, "https://127.0.0.1")
}

func isValidHTTPURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func healthCheckTimeout(cfg *config.MarketplaceConfig) time.Duration {
	if cfg.HealthCheckTimeoutSec <= 0 {
		return 5 * time.Second
	}
	return time.Duration(cfg.HealthCheckTimeoutSec) * time.Second
}

// devOverrideHealthy does one bounded GET /healthz against the override —
// the same health endpoint the real marketplace exposes. Uses the
// caller-supplied client so a self-signed-cert override is checked with
// the same TLS bypass its real requests will use; a bare default-transport
// client here would make an https override with a self-signed cert fail
// the health check even when it's genuinely reachable.
func devOverrideHealthy(client *http.Client, baseURL string, timeout time.Duration) bool {
	healthURL, err := url.JoinPath(baseURL, "/healthz")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// endpoint returns the marketplace base URL to prefer for the next request.
func (c *Client) endpoint() string {
	if c.devOverrideActive {
		return c.cfg.DevOverrideURL
	}
	return c.cfg.EndpointURL
}

// fallbackTimeout bounds how long a dev-override attempt gets before
// doWithFallback retries against the real cloud endpoint.
func (c *Client) fallbackTimeout() time.Duration {
	if c.cfg.FallbackTimeoutSec <= 0 {
		return 10 * time.Second
	}
	return time.Duration(c.cfg.FallbackTimeoutSec) * time.Second
}

// doWithFallback tries send against the dev override first (bounded by
// fallbackTimeout, so a hung/dead dev server can't stall a real request,
// and on devClient — never cloudClient) when it's active, then always
// falls back to the real cloud endpoint (on cloudClient) on any transport
// error. A non-2xx HTTP response from the override is NOT treated as a
// fallback trigger here — send only returns a Go error for transport-level
// failures (connection refused, timeout); an override that responds with a
// real error status propagates that response to the caller as-is. When the
// override isn't active, it just calls the cloud endpoint once.
func (c *Client) doWithFallback(ctx context.Context, send func(ctx context.Context, endpoint string, client *http.Client) (*http.Response, error)) (*http.Response, error) {
	if !c.devOverrideActive {
		return send(ctx, c.cfg.EndpointURL, c.cloudClient)
	}

	devCtx, cancel := context.WithTimeout(ctx, c.fallbackTimeout())
	defer cancel()
	resp, err := send(devCtx, c.cfg.DevOverrideURL, c.devClient)
	if err == nil {
		return resp, nil
	}
	log.Printf("[WARN] marketplace dev override request failed (%v), falling back to %s", err, c.cfg.EndpointURL)
	return send(ctx, c.cfg.EndpointURL, c.cloudClient)
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

	// Buffered once so a dev-override retry against the cloud endpoint can
	// send the same body again — an io.Reader can only be consumed once.
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
	}

	return c.doWithFallback(ctx, func(ctx context.Context, endpoint string, client *http.Client) (*http.Response, error) {
		reqURL, err := url.JoinPath(endpoint, path)
		if err != nil {
			return nil, fmt.Errorf("invalid request URL: %w", err)
		}

		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}

		log.Printf("[DEBUG] Marketplace request: %s %s", method, reqURL)

		req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
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

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		log.Printf("[DEBUG] Marketplace response: %d for %s %s", resp.StatusCode, method, reqURL)
		return resp, nil
	})
}

// ResolveURL turns a possibly-relative marketplace URL (e.g. the bundle_url
// "/api/v1/downloads/artifact/...") into an absolute URL against the configured
// endpoint origin. Absolute inputs are returned unchanged.
func (c *Client) ResolveURL(ref string) (string, error) {
	endpoint := c.endpoint()
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
	Vendor        string   `json:"vendor,omitempty"` // catalog API returns a plain name string
	PaidListing   bool     `json:"paid_listing,omitempty"`
	DeveloperID   string   `json:"developer_id"` // For backward compatibility
	Version       string   `json:"version"`
	TrustTier     string   `json:"trust_tier"`
	Capabilities  []string `json:"capabilities,omitempty"`
	Permissions   []string `json:"permissions,omitempty"` // manifest capability scopes (FR-006 permission badges)
	CanonicalType string   `json:"canonical_type"`        // For backward compatibility
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

// UnmarshalJSON maps the live catalog wire format onto PluginSummary. The
// deployed marketplace returns a flattened camelCase schema (id, type,
// trustLevel, vendor as a plain string, requiredCapabilities, minHostVersion,
// paidListing); the POS historically expected a richer snake_case schema. We
// accept both so the catalog decodes regardless of which the server sends.
func (p *PluginSummary) UnmarshalJSON(data []byte) error {
	var w struct {
		ID                   string   `json:"id"`
		ListingID            string   `json:"listing_id"`
		Name                 string   `json:"name"`
		Description          string   `json:"description"`
		Vendor               string   `json:"vendor"`
		DeveloperID          string   `json:"developer_id"`
		Version              string   `json:"version"`
		Type                 string   `json:"type"`
		CanonicalType        string   `json:"canonical_type"`
		TrustLevel           string   `json:"trustLevel"`
		TrustTier            string   `json:"trust_tier"`
		RequiredCapabilities []string `json:"requiredCapabilities"`
		Capabilities         []string `json:"capabilities"`
		Permissions          []string `json:"permissions"`
		MinHostVersion       string   `json:"minHostVersion"`
		PriceFlag            string   `json:"price_flag"`
		PaidListing          bool     `json:"paidListing"`
		PaidListingSnake     bool     `json:"paid_listing"`
		Architectures        []string `json:"architectures"`
		DeviceArch           string   `json:"device_arch"`
		IconURLCamel         string   `json:"iconUrl"`
		IconURL              string   `json:"icon_url"`
		Rating               float64  `json:"rating"`
		ReviewCount          int      `json:"review_count"`
		DownloadCount        int      `json:"download_count"`
		ArtifactURL          string   `json:"artifact_url"`
		ArtifactHash         string   `json:"artifact_hash"`
		Locale               string   `json:"locale"`
		ApprovedAt           string   `json:"approved_at"`
		CreatedAt            string   `json:"created_at"`
		UpdatedAt            string   `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}

	// The catalog id IS the listing id used for install/download-token.
	p.ID = firstNonEmptyStr(w.ID, w.ListingID)
	p.ListingID = firstNonEmptyStr(w.ListingID, w.ID)
	p.Name = w.Name
	p.Description = w.Description
	p.Vendor = w.Vendor
	p.DeveloperID = firstNonEmptyStr(w.DeveloperID, w.Vendor)
	p.Version = w.Version
	p.CanonicalType = firstNonEmptyStr(w.CanonicalType, w.Type)
	p.TrustTier = firstNonEmptyStr(w.TrustTier, w.TrustLevel)
	p.PriceFlag = w.PriceFlag
	p.PaidListing = w.PaidListing || w.PaidListingSnake
	p.Architectures = w.Architectures
	p.DeviceArch = w.DeviceArch
	p.IconURL = firstNonEmptyStr(w.IconURL, w.IconURLCamel)
	p.Rating = w.Rating
	p.ReviewCount = w.ReviewCount
	p.DownloadCount = w.DownloadCount
	p.ArtifactURL = w.ArtifactURL
	p.ArtifactHash = w.ArtifactHash
	p.Locale = w.Locale
	p.ApprovedAt = w.ApprovedAt
	p.CreatedAt = w.CreatedAt
	p.UpdatedAt = w.UpdatedAt

	p.Capabilities = w.Capabilities
	if len(p.Capabilities) == 0 {
		p.Capabilities = w.RequiredCapabilities
	}
	p.Permissions = w.Permissions
	return nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

	// Auth is optional: when marketplace OAuth2 credentials are not configured
	// (or the marketplace has auth disabled), proceed unauthenticated.
	token, tokenErr := c.tokenClient.GetToken(ctx)
	if tokenErr != nil {
		log.Printf("[DEBUG] proceeding without marketplace auth token: %v", tokenErr)
		token = ""
	}

	resp, err := c.doWithFallback(ctx, func(ctx context.Context, endpoint string, client *http.Client) (*http.Response, error) {
		reqURL, err := url.JoinPath(endpoint, "/v1/catalog/plugins")
		if err != nil {
			return nil, fmt.Errorf("invalid request URL: %w", err)
		}
		parsedURL, err := url.Parse(reqURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse URL: %w", err)
		}
		parsedURL.RawQuery = params.Encode()

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		if token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+token)
		}
		httpReq.Header.Set("x-marketplace-api-version", c.cfg.APIVersion)

		log.Printf("[DEBUG] Marketplace request: GET %s", parsedURL.String())
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		log.Printf("[DEBUG] Marketplace response: %d", resp.StatusCode)
		return resp, nil
	})
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

// APIError is a structured error decoded from a marketplace JSON envelope's
// `error` field (`{code, message}`, matching ut-cloud's `apiError` shape).
// Callers can `errors.As` it to react to a specific code (e.g. "not_entitled"
// — ut-docs#673) instead of parsing prose out of Error().
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("marketplace: %s: %s", e.Code, e.Message)
	case e.Message != "":
		return fmt.Sprintf("marketplace: %s", e.Message)
	case e.Code != "":
		return fmt.Sprintf("marketplace: %s", e.Code)
	default:
		return "marketplace: request failed"
	}
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

	// Decode the envelope regardless of status first — ut-cloud answers a
	// blocked download (e.g. a paid listing needing approval) with a non-200
	// status AND a structured {code, message} error body, not just a status
	// code (ut-docs#673). Bailing on status before decoding, as this used to,
	// silently threw that reason away.
	var envelope issueDownloadTokenEnvelope
	decodeErr := json.NewDecoder(resp.Body).Decode(&envelope)
	if envelope.Error != nil {
		return nil, &APIError{Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download token request failed: status %d", resp.StatusCode)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("failed to decode download token response: %w", decodeErr)
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
	reqURL, err := url.JoinPath(c.endpoint(), "/v1/revocations")
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

	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	httpReq.Header.Set("x-marketplace-api-version", c.cfg.APIVersion)

	// Dead code today (zero callers — revocation checking uses a separate
	// implementation), so this intentionally isn't wired through
	// doWithFallback; cloudClient is the safe default if that ever changes.
	resp, err := c.cloudClient.Do(httpReq)
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
