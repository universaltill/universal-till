package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#3049: the payment broker calls the cloud with the live enrolment
// (endpoint + ADR-0116 device credential), resolved per call like the cloud
// link's target, and refuses when the till holds no complete enrolment.
func TestPayBrokerTarget(t *testing.T) {
	cfg := func(endpoint, store, token string) *config.Config {
		c := &config.Config{}
		c.Marketplace.EndpointURL = endpoint
		c.Marketplace.StoreID = store
		c.Marketplace.MerchantToken = token
		return c
	}
	tg, ok := payBrokerTarget(cfg("https://cloud.example.test/api", "store-1", "cred-1"))
	if !ok || tg.BaseURL != "https://cloud.example.test/api" || tg.Bearer != "cred-1" {
		t.Fatalf("enrolled: %+v ok=%v", tg, ok)
	}
	if _, ok := payBrokerTarget(nil); ok {
		t.Fatal("nil config: want no target")
	}
	for name, c := range map[string]*config.Config{
		"no token":    cfg("https://cloud.example.test/api", "store-1", ""),
		"no endpoint": cfg("", "store-1", "cred-1"),
		"no store":    cfg("https://cloud.example.test/api", "", "cred-1"),
	} {
		if tg, ok := payBrokerTarget(c); ok {
			t.Fatalf("%s: got target %+v, want none", name, tg)
		}
	}
}
