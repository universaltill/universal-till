package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

func newHealthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewClient_DevOverrideIgnoredWhenDevModeOff(t *testing.T) {
	cloud := newHealthyServer(t)
	dev := newHealthyServer(t) // healthy, but DevMode is off — must not matter

	cfg := &config.MarketplaceConfig{
		EndpointURL:       cloud.URL,
		DevMode:           false,
		DevOverrideURL:    dev.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 5,
	}
	client := NewClient(cfg, &mockTokenClient{token: "t"})

	if client.devOverrideActive {
		t.Fatal("expected the dev override to be inactive when DevMode is off, regardless of health")
	}
	if got := client.endpoint(); got != cloud.URL {
		t.Fatalf("expected the cloud endpoint, got %q", got)
	}
}

func TestNewClient_DevOverrideIgnoredWhenURLInvalid(t *testing.T) {
	cloud := newHealthyServer(t)

	cfg := &config.MarketplaceConfig{
		EndpointURL:           cloud.URL,
		DevMode:               true,
		DevOverrideURL:        "not a url", // no scheme/host
		APIVersion:            "1.0.0",
		RequestTimeoutSec:     5,
		HealthCheckTimeoutSec: 1,
	}
	client := NewClient(cfg, &mockTokenClient{token: "t"})

	if client.devOverrideActive {
		t.Fatal("expected an invalid dev override URL to be ignored")
	}
	if got := client.endpoint(); got != cloud.URL {
		t.Fatalf("expected the cloud endpoint as fallback, got %q", got)
	}
}

func TestNewClient_DevOverrideIgnoredWhenUnhealthy(t *testing.T) {
	cloud := newHealthyServer(t)
	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // /healthz fails
	}))
	defer unhealthy.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:           cloud.URL,
		DevMode:               true,
		DevOverrideURL:        unhealthy.URL,
		APIVersion:            "1.0.0",
		RequestTimeoutSec:     5,
		HealthCheckTimeoutSec: 1,
	}
	client := NewClient(cfg, &mockTokenClient{token: "t"})

	if client.devOverrideActive {
		t.Fatal("expected an unhealthy dev override to be ignored")
	}
	if got := client.endpoint(); got != cloud.URL {
		t.Fatalf("expected the cloud endpoint as fallback, got %q", got)
	}
}

func TestNewClient_DevOverrideActiveWhenHealthy(t *testing.T) {
	cloud := newHealthyServer(t)
	dev := newHealthyServer(t)

	cfg := &config.MarketplaceConfig{
		EndpointURL:           cloud.URL,
		DevMode:               true,
		DevOverrideURL:        dev.URL,
		APIVersion:            "1.0.0",
		RequestTimeoutSec:     5,
		HealthCheckTimeoutSec: 1,
	}
	client := NewClient(cfg, &mockTokenClient{token: "t"})

	if !client.devOverrideActive {
		t.Fatal("expected a healthy, valid dev override under DevMode to be active")
	}
	if got := client.endpoint(); got != dev.URL {
		t.Fatalf("expected the dev override endpoint, got %q", got)
	}
}

// newHealthyTLSServer is like newHealthyServer but self-signed HTTPS —
// exactly the "LAN box with a self-signed cert" scenario the dev-override
// feature exists for.
func newHealthyTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestNewClient_SelfSignedHTTPSOverrideActivates guards the bug an earlier
// version of this code had: a self-signed-cert https:// dev override could
// never pass its own health check, because the health-check HTTP client
// didn't share the override's TLS-bypass transport.
func TestNewClient_SelfSignedHTTPSOverrideActivates(t *testing.T) {
	cloud := newHealthyServer(t)
	dev := newHealthyTLSServer(t)

	cfg := &config.MarketplaceConfig{
		EndpointURL:           cloud.URL,
		DevMode:               true,
		DevOverrideURL:        dev.URL,
		APIVersion:            "1.0.0",
		RequestTimeoutSec:     5,
		HealthCheckTimeoutSec: 2,
	}
	client := NewClient(cfg, &mockTokenClient{token: "t"})

	if !client.devOverrideActive {
		t.Fatal("expected a self-signed HTTPS dev override to pass its health check and activate")
	}
	if got := client.endpoint(); got != dev.URL {
		t.Fatalf("expected the dev override endpoint, got %q", got)
	}
}

// TestNewClient_CloudTLSNeverBypassedByDevOverrideConfig guards the more
// serious bug: an earlier version shared ONE transport between cloud and
// dev-override traffic, so merely *configuring* an https:// dev override
// (DevMode on) set InsecureSkipVerify on the transport that ALSO served
// real cloud requests — even when the override failed its health check and
// was never actually used. A stray/misconfigured DevOverrideURL in a
// staging or production-like environment must never weaken TLS verification
// for the real cloud endpoint.
func TestNewClient_CloudTLSNeverBypassedByDevOverrideConfig(t *testing.T) {
	cloud := newHealthyServer(t) // plain HTTP; verification isn't exercised
	// An https override that will fail its health check (nothing is
	// listening there), so devOverrideActive ends up false — the exact
	// case where the old code still leaked InsecureSkipVerify onto the
	// shared transport.
	cfg := &config.MarketplaceConfig{
		EndpointURL:           cloud.URL,
		DevMode:               true,
		DevOverrideURL:        "https://127.0.0.1:1", // valid URL shape, nothing listening
		APIVersion:            "1.0.0",
		RequestTimeoutSec:     5,
		HealthCheckTimeoutSec: 1,
	}
	client := NewClient(cfg, &mockTokenClient{token: "t"})

	if client.devOverrideActive {
		t.Fatal("expected the unreachable dev override to fail its health check")
	}

	transport, ok := client.cloudClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		return // no custom TLS config at all means verification is on (default)
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("cloudClient must never have InsecureSkipVerify set just because a dev override was configured")
	}
}

func TestDoRequest_FallsBackToCloudWhenDevOverrideDiesMidSession(t *testing.T) {
	var cloudHit bool
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHit = true
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer cloud.Close()

	// Healthy at construction time, so devOverrideActive is true — this is
	// what makes the test exercise doWithFallback's mid-request retry, not
	// just NewClient's startup health check (that's covered separately by
	// TestNewClient_DevOverrideIgnoredWhenUnhealthy).
	dev := newHealthyServer(t)

	cfg := &config.MarketplaceConfig{
		EndpointURL:           cloud.URL,
		DevMode:               true,
		DevOverrideURL:        dev.URL,
		APIVersion:            "1.0.0",
		RequestTimeoutSec:     5,
		HealthCheckTimeoutSec: 1,
		FallbackTimeoutSec:    1,
	}
	client := NewClient(cfg, &mockTokenClient{token: "t"})
	if !client.devOverrideActive {
		t.Fatal("expected the dev override to be active after passing its startup health check")
	}

	// Now kill the dev server — simulates it going down mid-session, after
	// the client already decided to trust it.
	dev.Close()

	resp, err := client.doRequest(context.Background(), http.MethodGet, "/v1/whatever", nil)
	if err != nil {
		t.Fatalf("doRequest: %v", err)
	}
	defer resp.Body.Close()
	if !cloudHit {
		t.Fatal("expected the request to fall back to and reach the cloud endpoint")
	}
}
