//go:build contract
// +build contract

package contract

import (
	"context"
	"fmt"
	"testing"

	"github.com/pact-foundation/pact-go/v2/consumer"
	"github.com/pact-foundation/pact-go/v2/matchers"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

type staticTokenProvider struct {
	token string
}

func (s staticTokenProvider) GetToken(_ context.Context) (string, error) {
	return s.token, nil
}

func (s staticTokenProvider) ClearCache() error {
	return nil
}

func TestMarketplaceListPluginsContract(t *testing.T) {
	mockProvider, err := consumer.NewV2Pact(consumer.MockHTTPProviderConfig{
		Consumer: "universal-till-pos",
		Provider: "ut-marketplace",
		Host:     "127.0.0.1",
		PactDir:  "tests/contract/pacts",
		LogDir:   "tests/contract/logs",
	})
	if err != nil {
		t.Fatalf("pact setup: %v", err)
	}

	mockProvider.
		AddInteraction().
		Given("catalog has plugins").
		UponReceiving("list plugins").
		WithRequest("GET", "/v1/catalog/plugins", func(b *consumer.V2RequestBuilder) {
			b.Header("Authorization", matchers.S("Bearer test-token"))
			b.Header("x-marketplace-api-version", matchers.S("1.0.0"))
			b.Query("locale", matchers.S("en-US"))
			b.Query("arch", matchers.S("amd64"))
			b.Query("capability", matchers.S("payments"))
		}).
		WillRespondWith(200, func(b *consumer.V2ResponseBuilder) {
			b.Header("Content-Type", matchers.S("application/json"))
			b.JSONBody(matchers.Map{
				"plugins": matchers.EachLike(matchers.StructMatcher{
					"id":             matchers.Like("plugin-123"),
					"listing_id":     matchers.Like("listing-123"),
					"name":           matchers.Like("Sample Plugin"),
					"description":    matchers.Like("Payments plugin"),
					"version":        matchers.Like("1.0.0"),
					"trust_tier":     matchers.Like("trusted"),
					"capabilities":   matchers.EachLike(matchers.Like("payments"), 1),
					"canonical_type": matchers.Like("payment"),
					"device_arch":    matchers.Like("amd64"),
					"artifact_url":   matchers.Like("http://example.test/plugin.tgz"),
					"artifact_hash":  matchers.Like("abc123"),
				}, 1),
				"pagination": matchers.StructMatcher{
					"page":         matchers.Like(1),
					"page_size":    matchers.Like(20),
					"total_items":  matchers.Like(1),
					"total_pages":  matchers.Like(1),
					"has_next":     matchers.Like(false),
					"has_previous": matchers.Like(false),
				},
			})
		}).
		ExecuteTest(t, func(cfg consumer.MockServerConfig) error {
			endpoint := fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port)
			client := marketplace.NewClient(&config.MarketplaceConfig{
				EndpointURL:       endpoint,
				APIVersion:        "1.0.0",
				RequestTimeoutSec: 5,
			}, staticTokenProvider{token: "test-token"})

			resp, err := client.ListPlugins(context.Background(), &marketplace.ListPluginsRequest{
				Locale:     "en-US",
				DeviceArch: "amd64",
				Capability: []string{"payments"},
			})
			if err != nil {
				return err
			}
			if resp == nil || len(resp.Plugins) == 0 {
				return fmt.Errorf("expected plugins in response")
			}
			return nil
		})
}

func TestMarketplaceIssueDownloadTokenContract(t *testing.T) {
	mockProvider, err := consumer.NewV2Pact(consumer.MockHTTPProviderConfig{
		Consumer: "universal-till-pos",
		Provider: "ut-marketplace",
		Host:     "127.0.0.1",
		PactDir:  "tests/contract/pacts",
		LogDir:   "tests/contract/logs",
	})
	if err != nil {
		t.Fatalf("pact setup: %v", err)
	}

	// Current wire contract (see internal/plugins/marketplace/client.go):
	// POST /v1/downloads/tokens with listing_id, {data,error} envelope,
	// bundle_url + checksum_sha256 + signature in the data.
	mockProvider.
		AddInteraction().
		Given("plugin artifact is available").
		UponReceiving("issue download token").
		WithRequest("POST", "/v1/downloads/tokens", func(b *consumer.V2RequestBuilder) {
			b.Header("Authorization", matchers.S("Bearer test-token"))
			b.Header("x-marketplace-api-version", matchers.S("1.0.0"))
			b.JSONBody(matchers.Map{
				"listing_id":  matchers.Like("listing-123"),
				"version":     matchers.Like("1.2.3"),
				"merchant_id": matchers.Like("merchant-1"),
				"store_id":    matchers.Like("store-1"),
				"device_id":   matchers.Like("device-1"),
				"device_arch": matchers.Like("amd64"),
			})
		}).
		WillRespondWith(200, func(b *consumer.V2ResponseBuilder) {
			b.Header("Content-Type", matchers.S("application/json"))
			b.JSONBody(matchers.Map{
				"data": matchers.StructMatcher{
					"token":           matchers.Like("tok-abc"),
					"bundle_url":      matchers.Like("/api/v1/downloads/artifact/listing-123"),
					"release_id":      matchers.Like("rel-123"),
					"version":         matchers.Like("1.2.3"),
					"checksum_sha256": matchers.Like("deadbeef"),
					"signature":       matchers.Like("abc123"),
					"expires_at":      matchers.Like("2025-01-01T00:00:00Z"),
				},
			})
		}).
		ExecuteTest(t, func(cfg consumer.MockServerConfig) error {
			endpoint := fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port)
			client := marketplace.NewClient(&config.MarketplaceConfig{
				EndpointURL:       endpoint,
				APIVersion:        "1.0.0",
				RequestTimeoutSec: 5,
			}, staticTokenProvider{token: "test-token"})

			resp, err := client.IssueDownloadToken(context.Background(), &marketplace.IssueDownloadTokenRequest{
				PluginID:   "listing-123",
				Version:    "1.2.3",
				MerchantID: "merchant-1",
				StoreID:    "store-1",
				DeviceID:   "device-1",
				DeviceArch: "amd64",
			})
			if err != nil {
				return err
			}
			if resp == nil || resp.BundleURL == "" || resp.Token == "" {
				return fmt.Errorf("expected bundle_url and token in response")
			}
			return nil
		})
}
