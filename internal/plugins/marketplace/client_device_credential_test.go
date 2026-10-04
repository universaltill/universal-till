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
// download-token request carries it — ahead of any OAuth token, which the
// cloud never treats as store authentication.
func TestIssueDownloadToken_SendsDeviceCredential(t *testing.T) {
	for name, oauthToken := range map[string]string{"no oauth token": "", "with oauth token": "jwt.oauth.token"} {
		t.Run(name, func(t *testing.T) {
			srv, seen := recordingTokenServer(t)
			cfg := &config.MarketplaceConfig{
				EndpointURL:       srv.URL,
				APIVersion:        "1.0.0",
				RequestTimeoutSec: 5,
				MerchantToken:     "device-credential-hex",
			}
			issueTestToken(t, NewClient(cfg, &mockTokenClient{token: oauthToken}))
			if got := seen(); len(got) != 1 || got[0] != "Bearer device-credential-hex" {
				t.Fatalf("Authorization = %q, want the device credential", got)
			}
		})
	}
}

// An unenrolled till has no credential: the request stays as before (the
// OAuth token when configured, else none) and free plugins still install.
func TestIssueDownloadToken_NoDeviceCredentialKeepsOldAuth(t *testing.T) {
	srv, seen := recordingTokenServer(t)
	cfg := &config.MarketplaceConfig{EndpointURL: srv.URL, APIVersion: "1.0.0", RequestTimeoutSec: 5}
	issueTestToken(t, NewClient(cfg, &mockTokenClient{token: ""}))
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
	client := NewClient(cfg, &mockTokenClient{token: ""})
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
