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

	mockProvider.
		AddInteraction().
		Given("plugin artifact is available").
		UponReceiving("issue download token").
		WithRequest("GET", "/v1/downloads/plugin-123/url", func(b *consumer.V2RequestBuilder) {
			b.Header("Authorization", matchers.S("Bearer test-token"))
			b.Header("x-marketplace-api-version", matchers.S("1.0.0"))
			b.Query("arch", matchers.S("amd64"))
			b.Query("platform", matchers.S("linux"))
			b.Query("version", matchers.S("1.2.3"))
		}).
		WillRespondWith(200, func(b *consumer.V2ResponseBuilder) {
			b.Header("Content-Type", matchers.S("application/json"))
			b.JSONBody(matchers.Map{
				"download_url":    matchers.Like("http://example.test/plugin.tgz"),
				"expires_at":      matchers.Like("2025-01-01T00:00:00Z"),
				"file_size_bytes": matchers.Like(1024),
				"checksum": matchers.StructMatcher{
					"sha256": matchers.Like("deadbeef"),
				},
				"version": matchers.Like("1.2.3"),
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
				PluginID:   "plugin-123",
				Version:    "1.2.3",
				DeviceArch: "amd64",
			})
			if err != nil {
				return err
			}
			if resp == nil || resp.DownloadURL == "" {
				return fmt.Errorf("expected download_url in response")
			}
			return nil
		})
}
