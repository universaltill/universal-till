package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ADR-0172 §2 (ut-docs#6285): which tax plugin, if any, a till may install
// without a click. Table tests for every refusal ADR-0172 §8 slice 2 names.

func cleanTaxManifest() *plugins.Manifest {
	return &plugins.Manifest{
		ID:            "com.universaltill.tax-xx",
		Version:       "1.0.0",
		CanonicalType: "tax",
		Markets:       []string{"XX"},
		Hooks:         []plugins.ManifestHook{{Event: "tax.rate.ask", Action: "tax.rate"}},
		Permissions:   []string{"events:receive", "sales:read", "storage"},
	}
}

func TestTaxAutoInstallEligible_Manifest(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(m *plugins.Manifest)
		wantErr string // substring of the reason; "" = eligible
	}{
		{"clean first-party tax plugin", func(m *plugins.Manifest) {}, ""},
		{"subscribes fiscal.sign.ask", func(m *plugins.Manifest) {
			m.Hooks = append(m.Hooks, plugins.ManifestHook{Event: "fiscal.sign.ask", Action: "sign"})
		}, "fiscal"},
		{"subscribes another fiscal.* event", func(m *plugins.Manifest) {
			m.Hooks = append(m.Hooks, plugins.ManifestHook{Event: "fiscal.sign.start", Action: "x"})
		}, "fiscal"},
		{"entry trigger_event fiscal.*", func(m *plugins.Manifest) {
			m.Entries = []plugins.ManifestEntry{{Type: "tax", Key: "k", TriggerEvent: "fiscal.sign.ask"}}
		}, "fiscal"},
		{"provides fiscal.register", func(m *plugins.Manifest) { m.Provides = []string{"fiscal.register"} }, "fiscal"},
		{"net: permission", func(m *plugins.Manifest) {
			m.Permissions = append(m.Permissions, "net:test-api.service.hmrc.gov.uk")
		}, "net:"},
		{"fiscal_register_de:read scope", func(m *plugins.Manifest) {
			m.Permissions = append(m.Permissions, "fiscal_register_de:read")
		}, "fiscal_register_de:read"},
		{"unknown future permission", func(m *plugins.Manifest) {
			m.Permissions = append(m.Permissions, "customers:write")
		}, "customers:write"},
		{"id outside the first-party prefix", func(m *plugins.Manifest) { m.ID = "com.example.tax-xx" }, "first-party"},
		{"look-alike id prefix", func(m *plugins.Manifest) { m.ID = "com.universaltill-evil.tax" }, "first-party"},
		{"not a tax plugin", func(m *plugins.Manifest) { m.CanonicalType = "page" }, "tax"},
		{"empty markets", func(m *plugins.Manifest) { m.Markets = nil }, "markets"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := cleanTaxManifest()
			tc.mutate(m)
			err := taxAutoInstallEligible(m, "XX")
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want eligible, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want a refusal mentioning %q, got eligible", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("refusal %q does not mention %q", err, tc.wantErr)
			}
		})
	}
	if err := taxAutoInstallEligible(cleanTaxManifest(), "YY"); err == nil {
		t.Fatal("a manifest whose markets do not name the shop's country is never eligible")
	}
	if err := taxAutoInstallEligible(nil, "XX"); err == nil {
		t.Fatal("a nil manifest is never eligible")
	}
}

func taxListing(id string, mut func(p *marketplace.PluginSummary)) marketplace.PluginSummary {
	p := marketplace.PluginSummary{
		ID: id, ListingID: id, Name: "Tax " + id, Version: "1.0.0", CanonicalType: "tax",
		Markets: []string{"XX"}, TrustTier: "official",
		Permissions: []string{"events:receive", "sales:read", "storage"},
	}
	if mut != nil {
		mut(&p)
	}
	return p
}

func TestResolveCountryTaxPlugin(t *testing.T) {
	tests := []struct {
		name         string
		catalog      []marketplace.PluginSummary
		wantID       string // "" = none chosen
		wantListings int
	}{
		{"one clean listing is chosen", []marketplace.PluginSummary{taxListing("a", nil)}, "a", 1},
		{"two eligible listings: none, never picks", []marketplace.PluginSummary{taxListing("a", nil), taxListing("b", nil)}, "", 2},
		{"empty markets is never auto-installed", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) { p.Markets = nil })}, "", 1},
		{"paid listing", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) { p.PaidListing = true })}, "", 1},
		{"third-party listing", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) { p.TrustTier = "verified" })}, "", 1},
		{"unverified listing", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) { p.TrustTier = "" })}, "", 1},
		{"listing declares a net: permission", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) {
			p.Permissions = append(p.Permissions, "net:example.com")
		})}, "", 1},
		{"listing declares a fiscal register scope", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) {
			p.Permissions = append(p.Permissions, "fiscal_register_de:read")
		})}, "", 1},
		{"other market only", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) { p.Markets = []string{"YY"} })}, "", 0},
		{"not a tax listing", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) { p.CanonicalType = "page" })}, "", 0},
		{"incompatible architecture", []marketplace.PluginSummary{taxListing("a", func(p *marketplace.PluginSummary) { p.Architectures = []string{"plan9/mips"} })}, "", 1},
		{"one clean plus one paid: the clean one", []marketplace.PluginSummary{taxListing("a", nil), taxListing("b", func(p *marketplace.PluginSummary) { p.PaidListing = true })}, "a", 2},
		{"nothing published", nil, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dp := newBasePluginTestDeps(t)
			mkt := newFakeMarketplace(t, map[string]string{})
			mkt.setCatalog(tc.catalog...)
			dp.Cfg.Marketplace = mkt.config()

			got, listings, err := resolveCountryTaxPlugin(t.Context(), dp, "xx")
			if err != nil {
				t.Fatalf("resolveCountryTaxPlugin: %v", err)
			}
			if listings != tc.wantListings {
				t.Errorf("listings = %d, want %d", listings, tc.wantListings)
			}
			switch {
			case tc.wantID == "" && got != nil:
				t.Errorf("want none chosen, got %s", got.ID)
			case tc.wantID != "" && (got == nil || got.ID != tc.wantID):
				t.Errorf("want %s chosen, got %+v", tc.wantID, got)
			}
		})
	}
}

func TestResolveCountryTaxPlugin_CatalogUnreachable(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	dp.Cfg.Marketplace.EndpointURL = unreachableURL()
	_, _, err := resolveCountryTaxPlugin(t.Context(), dp, "XX")
	if err == nil {
		t.Fatal("an unreachable catalogue must be an error, not 'nothing eligible'")
	}
}
