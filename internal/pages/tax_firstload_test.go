package pages

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/pos"
)

// ADR-0172 §3 (ut-docs#6286): after Ed25519 verification and the §2.4
// eligibility check, and BEFORE anything is persisted or activated, the till
// loads the tax plugin in an unregistered sandbox and sends one dry
// tax.rate.ask. These tests drive the REAL install path against the fake
// marketplace with real compiled wasip1 guests.

const (
	autoTaxListing  = "listing-tax-xx"
	autoTaxPluginID = "com.universaltill.tax-xx"
	autoTaxCountry  = "XX"
)

// autoTaxManifest is a manifest that passes every §2 rule.
func autoTaxManifest() *plugins.Manifest {
	return &plugins.Manifest{
		ID: autoTaxPluginID, Name: "Tax XX", Version: "1.0.0",
		Markets:     []string{autoTaxCountry},
		Hooks:       []plugins.ManifestHook{{Event: "tax.rate.ask", Action: "tax.rate"}},
		Entries:     []plugins.ManifestEntry{{Type: "tax", Key: "rate-switch", Label: "Rate switch"}},
		Permissions: []string{"events:receive", "sales:read", "storage"},
	}
}

// autoTaxCatalogEntry is the catalogue listing for autoTaxManifest.
func autoTaxCatalogEntry() marketplace.PluginSummary {
	return marketplace.PluginSummary{
		ID: autoTaxListing, ListingID: autoTaxListing, Name: "Tax XX", Version: "1.0.0",
		CanonicalType: "tax", Markets: []string{autoTaxCountry}, TrustTier: "official",
		Permissions: []string{"events:receive", "sales:read", "storage"},
	}
}

func TestTaxFirstLoadCheck(t *testing.T) {
	tests := []struct {
		guest      string
		wantActive bool
	}{
		{"taxdecline_guest", true}, // a decline is a well-formed answer
		{"taxask_guest", true},     // an override is a well-formed answer
		{"taxtrap_guest", false},
		{"taxmalformed_guest", false},
		{"taxbadrate_guest", false},
	}
	for _, tc := range tests {
		t.Run(tc.guest, func(t *testing.T) {
			dp := newBasePluginTestDeps(t)
			mkt := newFakeMarketplace(t, map[string]string{})
			mkt.publishCustomTaxManifest(t, autoTaxListing, autoTaxManifest(), buildFiscalGuest(t, tc.guest))
			mkt.setCatalog(autoTaxCatalogEntry())
			dp.Cfg.Marketplace = mkt.config()
			plugins.SharedBus(dp.Db).ResetSubscribers()

			spec := basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry}
			err := resolveAndInstallBasePlugin(t.Context(), dp, spec)

			active, aerr := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), autoTaxPluginID)
			if aerr != nil {
				t.Fatalf("PluginActive: %v", aerr)
			}
			_, statErr := os.Stat(filepath.Join(paths.Plugins(), autoTaxPluginID))
			onDisk := statErr == nil

			if tc.wantActive {
				if err != nil {
					t.Fatalf("a well-formed answer or a decline must pass the first-load check: %v", err)
				}
				if !active {
					t.Fatal("expected the plugin installed and active")
				}
				return
			}
			if err == nil || !errors.Is(err, errBasePluginRefused) {
				t.Fatalf("want an error wrapping errBasePluginRefused, got %v", err)
			}
			if active || onDisk {
				t.Fatalf("a plugin failing the first-load ask must never be persisted or activated (active=%v onDisk=%v)", active, onDisk)
			}
			// The spec stays pending: the retry tick keeps it.
			if aerr := addPendingBasePlugins(t.Context(), dp, []basePluginSpec{spec}); aerr != nil {
				t.Fatalf("queue: %v", aerr)
			}
			basePluginRetryTick(t.Context(), dp)
			pending, _ := loadPendingBasePlugins(t.Context(), dp)
			if len(pending) != 1 || pending[0] != spec {
				t.Fatalf("the spec must stay pending after a failed first-load check, got %+v", pending)
			}
			// ...and a sale still completes at the item's own rate: no tax
			// plugin is registered, so the ask declines and is NOT blocked
			// (the ut-docs#368 tender block only exists for an ACTIVE one).
			asker := &pluginTaxRateAsker{db: dp.Db}
			rate, ok, blocked := asker.AskTaxRateBP(pos.BasketLine{ItemID: "i1", TaxCodeID: "std", TaxRateBP: 2000}, "")
			if ok || rate != 0 || blocked {
				t.Fatalf("item rates must apply and checkout must not be blocked: rate=%d ok=%v blocked=%v", rate, ok, blocked)
			}
		})
	}
}

// §2.4 runs on the VERIFIED manifest, before persist: a listing that looks
// clean in the catalogue but whose signed manifest subscribes to a fiscal
// event or asks for network is refused after download, nothing persisted.
func TestTaxAutoInstall_RefusesFiscalOrNetworkManifestAfterDownload(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m *plugins.Manifest)
	}{
		{"fiscal.sign.ask", func(m *plugins.Manifest) {
			m.Hooks = append(m.Hooks, plugins.ManifestHook{Event: "fiscal.sign.ask", Action: "sign"})
		}},
		{"provides fiscal.register", func(m *plugins.Manifest) { m.Provides = []string{"fiscal.register"} }},
		{"net: permission", func(m *plugins.Manifest) { m.Permissions = append(m.Permissions, "net:example.com") }},
		{"register scope", func(m *plugins.Manifest) { m.Permissions = append(m.Permissions, "fiscal_register_de:read") }},
		{"id outside the first-party prefix", func(m *plugins.Manifest) { m.ID = "com.example.tax-xx" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dp := newBasePluginTestDeps(t)
			mkt := newFakeMarketplace(t, map[string]string{})
			m := autoTaxManifest()
			tc.mutate(m)
			mkt.publishCustomTaxManifest(t, autoTaxListing, m, buildFiscalGuest(t, "taxdecline_guest"))
			mkt.setCatalog(autoTaxCatalogEntry()) // the listing LOOKS clean
			dp.Cfg.Marketplace = mkt.config()

			err := resolveAndInstallBasePlugin(t.Context(), dp, basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry})
			if err == nil || !errors.Is(err, errBasePluginRefused) {
				t.Fatalf("want errBasePluginRefused, got %v", err)
			}
			for _, id := range []string{m.ID, autoTaxPluginID} {
				if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), id); active {
					t.Fatalf("%s must not be installed", id)
				}
				if _, statErr := os.Stat(filepath.Join(paths.Plugins(), id)); statErr == nil {
					t.Fatalf("%s must not be on disk", id)
				}
			}
		})
	}
}
