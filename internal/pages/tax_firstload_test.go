package pages

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
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

// A guest that never returns from the ask (a busy loop) is refused within the
// ask deadline, not hung: DryAskWasm bounds the call and wazero closes the
// module on context done. Nothing is persisted; the spec stays pending.
func TestTaxFirstLoadCheck_HangingGuestIsRefusedWithinDeadline(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	mkt.publishCustomTaxManifest(t, autoTaxListing, autoTaxManifest(), buildFiscalGuest(t, "taxhang_guest"))
	mkt.setCatalog(autoTaxCatalogEntry())
	dp.Cfg.Marketplace = mkt.config()
	plugins.SharedBus(dp.Db).ResetSubscribers()

	spec := basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry}
	start := time.Now()
	err := resolveAndInstallBasePlugin(t.Context(), dp, spec)
	elapsed := time.Since(start)

	if err == nil || !errors.Is(err, errBasePluginRefused) {
		t.Fatalf("a hanging guest must be refused (errBasePluginRefused), got %v", err)
	}
	// 2s ask deadline + compile/download slack; well under the 15s dry-ask cap.
	// The bound is generous: the 2s ask deadline is wall-clock but the wazero
	// compile scales with CPU, so a loaded runner needs headroom.
	if elapsed > 12*time.Second {
		t.Fatalf("the refusal took %s; the first-load check must be bounded", elapsed)
	}
	// ...and it must be a real deadline refusal, not an instant trap or load
	// error: the guest spun until the 2s ask deadline cut it off.
	if elapsed < 2*time.Second {
		t.Fatalf("the refusal took only %s; a hanging guest should run to the 2s ask deadline (was it refused for another reason? %v)", elapsed, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a context deadline refusal, got %v", err)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), autoTaxPluginID); active {
		t.Fatal("a hanging plugin must not be activated")
	}
	if _, statErr := os.Stat(filepath.Join(paths.Plugins(), autoTaxPluginID)); statErr == nil {
		t.Fatal("a hanging plugin must not be on disk")
	}
	if err := addPendingBasePlugins(t.Context(), dp, []basePluginSpec{spec}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	pending, _ := loadPendingBasePlugins(t.Context(), dp)
	if len(pending) != 1 || pending[0] != spec {
		t.Fatalf("the spec must stay pending, got %+v", pending)
	}
}

// R4: the dry ask is not a trust shortcut. A bundle whose manifest signature
// does not verify is refused by the verifier BEFORE the first-load check
// runs, even though its module (taxbadrate_guest) would FAIL the dry ask: were the
// order reversed, the error would be a first-load refusal and this goes red.
func TestTaxAutoInstall_UnverifiedBundleRefusedBeforeDryAsk(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	// Sign with a key the till does not trust.
	_, rogue, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mkt.privateKey = rogue
	mkt.publishCustomTaxManifest(t, autoTaxListing, autoTaxManifest(), buildFiscalGuest(t, "taxbadrate_guest"))
	mkt.setCatalog(autoTaxCatalogEntry())
	dp.Cfg.Marketplace = mkt.config()
	plugins.SharedBus(dp.Db).ResetSubscribers()

	err = resolveAndInstallBasePlugin(t.Context(), dp, basePluginSpec{CanonicalType: "tax", Market: autoTaxCountry})
	if err == nil {
		t.Fatal("an unverifiable bundle must not install")
	}
	if errors.Is(err, errBasePluginRefused) || strings.Contains(err.Error(), "first-load") {
		t.Fatalf("refusal must come from verification, not the dry ask: %v", err)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), autoTaxPluginID); active {
		t.Fatal("must not be active")
	}
	if _, statErr := os.Stat(filepath.Join(paths.Plugins(), autoTaxPluginID)); statErr == nil {
		t.Fatal("must not be on disk")
	}
}

// R3: the pre-sandbox refusals never touch the sandbox (a call
// that reached DryAskWasm would fail with a load error, not these messages).
func TestTaxFirstLoadCheck_RefusesNonWasmAndModuleless(t *testing.T) {
	tests := []struct {
		name string
		m    plugins.Manifest
		want string
	}{
		{"binary runtime", plugins.Manifest{Runtime: "binary", Executable: "x"}, "cannot be sandboxed"},
		{"empty runtime", plugins.Manifest{Executable: "x"}, "cannot be sandboxed"},
		{"wasm, no module", plugins.Manifest{Runtime: "wasm"}, "no module named in the manifest"},
		{"wasm, bare ./ entrypoint", plugins.Manifest{Runtime: "wasm", Entrypoint: "./"}, "no module named in the manifest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := taxFirstLoadCheck(t.Context(), &common.Deps{}, &tc.m, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// R2: the answer boundaries. Pinned to what the code does, including the
// lenient `null` cases (core's reader treats rate <= 0 as no opinion).
func TestValidTaxAskAnswer(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty is a decline", ``, false},
		{"whitespace is a decline", " \n\t ", false},
		{"null", `null`, false},
		{"empty object", `{}`, false},
		{"no rate_bp", `{"other":1}`, false},
		{"rate_bp null", `{"rate_bp":null}`, false},
		{"rate_bp 0", `{"rate_bp":0}`, false},
		{"rate_bp 1", `{"rate_bp":1}`, false},
		{"rate_bp 10000", `{"rate_bp":10000}`, false},
		{"rate_bp 10001", `{"rate_bp":10001}`, true},
		{"rate_bp -1", `{"rate_bp":-1}`, true},
		{"rate_bp float", `{"rate_bp":20.5}`, true},
		{"rate_bp string", `{"rate_bp":"2000"}`, true},
		{"rate_bp bool", `{"rate_bp":true}`, true},
		// Core decodes into a struct, whose keys match case-insensitively.
		{"RATE_BP upper-case out of range", `{"RATE_BP":99999}`, true},
		{"duplicate keys differing in case, last wins", `{"rate_bp":5,"RATE_BP":99999}`, true},
		{"array", `[]`, true},
		{"string", `"str"`, true},
		{"number", `5`, true},
		{"not json", `not json`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validTaxAskAnswer(json.RawMessage(tc.raw))
			if (err != nil) != tc.wantErr {
				t.Fatalf("validTaxAskAnswer(%q) err=%v, wantErr=%v", tc.raw, err, tc.wantErr)
			}
		})
	}
}
