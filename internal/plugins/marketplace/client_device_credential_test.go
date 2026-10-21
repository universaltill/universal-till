package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// recordingTokenServer answers /v1/downloads/tokens with a valid envelope
// and records each request's Authorization header.
func recordingTokenServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issueDownloadTokenEnvelope{Data: &IssueDownloadTokenResponse{
			Token: "tok-1", BundleURL: "/b", ChecksumSHA256: "abc", Signature: "sig",
		}})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func issueTestToken(t *testing.T, client *Client) {
	t.Helper()
	if _, err := client.IssueDownloadToken(context.Background(), &IssueDownloadTokenRequest{
		PluginID: "listing-1", MerchantID: "store-1", StoreID: "store-1", DeviceID: "device-1", DeviceArch: "linux/amd64",
	}); err != nil {
		t.Fatalf("IssueDownloadToken: %v", err)
	}
}

// ADR-0120 Phase 2 F1 (ut-docs#2930): the cloud issues a paid plugin only
// to a till that authenticates with its ADR-0116 device credential, so the
// download-token request carries it.
func TestIssueDownloadToken_SendsDeviceCredential(t *testing.T) {
	srv, seen := recordingTokenServer(t)
	cfg := &config.MarketplaceConfig{
		EndpointURL:       srv.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 5,
		MerchantToken:     "device-credential-hex",
	}
	issueTestToken(t, NewClient(cfg))
	if got := seen(); len(got) != 1 || got[0] != "Bearer device-credential-hex" {
		t.Fatalf("Authorization = %q, want the device credential", got)
	}
}

// An unenrolled till has no credential: the request goes out with no
// bearer at all, and free plugins still install.
func TestIssueDownloadToken_NoDeviceCredentialSendsNoBearer(t *testing.T) {
	srv, seen := recordingTokenServer(t)
	cfg := &config.MarketplaceConfig{EndpointURL: srv.URL, APIVersion: "1.0.0", RequestTimeoutSec: 5}
	issueTestToken(t, NewClient(cfg))
	if got := seen(); len(got) != 1 || got[0] != "" {
		t.Fatalf("Authorization = %q, want none", got)
	}
}

// The device credential authenticates the till to the real cloud only: a
// dev override (a LAN box under DevMode) never receives it.
func TestIssueDownloadToken_DeviceCredentialNeverSentToDevOverride(t *testing.T) {
	cloud, cloudSeen := recordingTokenServer(t)
	dev, devSeen := recordingTokenServer(t)
	cfg := &config.MarketplaceConfig{
		EndpointURL:           cloud.URL,
		DevMode:               true,
		DevOverrideURL:        dev.URL,
		APIVersion:            "1.0.0",
		RequestTimeoutSec:     5,
		HealthCheckTimeoutSec: 1,
		FallbackTimeoutSec:    1,
		MerchantToken:         "device-credential-hex",
	}
	client := NewClient(cfg)
	if !client.devOverrideActive {
		t.Fatal("expected the dev override to be active")
	}
	issueTestToken(t, client)
	if got := devSeen(); len(got) != 1 || got[0] != "" {
		t.Fatalf("dev override saw Authorization %q, want none", got)
	}
	if got := cloudSeen(); len(got) != 0 {
		t.Fatalf("cloud should not be hit while the override answers, saw %q", got)
	}

	// Override dies: the retry against the real cloud carries the credential.
	dev.Close()
	issueTestToken(t, client)
	if got := cloudSeen(); len(got) != 1 || got[0] != "Bearer device-credential-hex" {
		t.Fatalf("cloud saw Authorization %q, want the device credential", got)
	}
}

// ut-docs#3577: the scheme-(b) OAuth merchant token is gone (the cloud has
// refused to issue one since ut-docs#3540), so no request carries any
// bearer but the ADR-0116 device credential: the catalogue list and the
// acknowledgement go out with none, even on an enrolled till.
func TestNoRequestCarriesAnOAuthBearer(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/catalog/plugins" {
			_, _ = w.Write([]byte(`{"plugins":[]}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	cfg := &config.MarketplaceConfig{
		EndpointURL:       srv.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 5,
		ClientID:          "merchant-1",
		MerchantToken:     "device-credential-hex",
	}
	client := NewClient(cfg)
	if _, err := client.ListPlugins(context.Background(), &ListPluginsRequest{}); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	_ = client.AckDownload(context.Background(), &AckDownloadRequest{PluginID: "p", Version: "1.0.0", Token: "tok-1", Success: true})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("requests seen = %v, want the list and the ack", seen)
	}
	for path, auth := range seen {
		if auth != "" {
			t.Errorf("%s carried Authorization %q, want none", path, auth)
		}
	}
}
