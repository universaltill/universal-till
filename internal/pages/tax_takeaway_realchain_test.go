package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#1351: Germany café pilot — a fresh Add of a tax-switching item
// under Takeaway charged the item's dine-in rate (19%) instead of the
// plugin-configured takeaway override (7%), a live §12 UStG VAT
// over-collection. The pre-existing TestOrderTypeTaxSwitching* coverage in
// internal/pos/service_test.go installs a hand-rolled fakeTaxAsker, which
// bypasses internal/pages's real pluginTaxRateAsker, the event bus, the
// wazero runtime AND the plugin's settings_get read — so a bug anywhere in
// that chain was invisible to it. The tests below run the WHOLE real chain:
// a signed wasm tax plugin installed through the real marketplace installer,
// its takeaway_rate_overrides setting saved through the real typed settings
// editor handler, the production PriceResolverAdapter resolving the SKU from
// a real catalog row, and the real /api/pos/order-type + /api/pos/scan
// handlers driving the shared engine.

// buildTaxOverridesAskGuest compiles the settings-reading wasip1 tax guest
// (testdata/taxask_overrides_guest) — same shape as buildTaxAskGuest, but
// this guest answers from the takeaway_rate_overrides setting via the real
// settings_get host function instead of a fixed value.
func buildTaxOverridesAskGuest(t *testing.T) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	out := filepath.Join(t.TempDir(), "taxask_overrides.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/taxask_overrides_guest")
	cmd.Dir = filepath.Dir(file)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 overrides tax guest: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read built guest: %v", err)
	}
	return raw
}

// publishWasmTaxVersionWithSettings is publishWasmTaxVersion plus a
// manifest-declared settings key — needed so the real install path
// (PersistManifest → ReconcilePluginSettings) seeds the setting row exactly
// as ut-plugin-tax-de's own manifest does (default_value is the JSON OBJECT
// {}, scope global).
func (m *fakeMarketplace) publishWasmTaxVersionWithSettings(t *testing.T, listingID, pluginID, version string, wasmBin []byte, settings []plugins.ManifestSetting) {
	t.Helper()
	manifest := &plugins.Manifest{
		ID:            pluginID,
		Name:          "Takeaway Overrides Tax Plugin " + pluginID,
		Version:       version,
		Entrypoint:    "./plugin.wasm",
		Executable:    "plugin.wasm",
		Runtime:       "wasm",
		CanonicalType: "tax",
		DeviceArch:    "any",
		Hooks:         []plugins.ManifestHook{{Event: "tax.rate.ask", Action: "tax.rate"}},
		Permissions:   []string{"events:receive"},
		Settings:      settings,
	}
	artifact := signedFakeMktArtifactWithBinary(t, m.privateKey, manifest, "plugin.wasm", wasmBin)
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.listings[listingID]
	if l == nil {
		l = &fakeMktListing{releases: map[string]fakeMktRelease{}}
		m.listings[listingID] = l
	}
	l.releases[version] = fakeMktRelease{artifact: artifact, manifest: manifest, checksum: sha256Hex(artifact)}
	l.latest = version
}

// takeawayRealChainFixture is everything the real-chain tests share: one
// till with the settings-reading wasm tax plugin installed for real, the
// issue's exact catalog shape (Cappuccino, SKU 30005, €3.80 gross,
// tax code "Imported 19% (takeaway 7%)"), the production engine wiring, and
// the 7% override saved through the real settings-editor handler.
type takeawayRealChainFixture struct {
	dp     *syncPluginsPrimary
	engine *pos.Service
	posMux *http.ServeMux
	taxID  string
}

// newTakeawayRealChainFixture builds the till. saveOverrideViaEditor
// controls whether the 7% override is saved up-front through the typed
// settings editor (the merchant-hand-configured shape) — the import-path
// regression test instead saves it mid-test through the import's own
// mergeTakeawayOverrides, so it starts with the override absent.
func newTakeawayRealChainFixture(t *testing.T, saveOverrideViaEditor bool) *takeawayRealChainFixture {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	primary := newSyncPluginsPrimary(t, mkt)
	guest := buildTaxOverridesAskGuest(t)
	// The real German plugin's id, so the import path's hardcoded
	// taxDePluginID targets this fixture plugin exactly as it targets the
	// real one.
	const pluginID = taxDePluginID
	mkt.publishWasmTaxVersionWithSettings(t, "listing-taxov", pluginID, "1.0.0", guest,
		[]plugins.ManifestSetting{{
			Key:          "takeaway_rate_overrides",
			DefaultValue: map[string]any{}, // JSON object default, same as ut-plugin-tax-de's manifest
			Scope:        "global",
		}})
	primary.install(t, "listing-taxov")
	ctx := t.Context()
	dp := primary.dp

	// The issue's exact catalog shape.
	catRepo := data.NewCatalogRepo(dp.Db)
	takeawayBP := 700
	taxID, err := catRepo.CreateTaxCode(ctx, "Imported 19% (takeaway 7%)", 1900, &takeawayBP)
	if err != nil {
		t.Fatalf("create tax code: %v", err)
	}
	if _, err := catRepo.CreateItem(ctx, catalogtypes.ItemInput{
		SKU: "30005", Name: "Cappuccino", BasePrice: 380, TaxCodeID: &taxID, IsActive: true,
	}); err != nil {
		t.Fatalf("create item: %v", err)
	}

	// Production engine wiring (init.go's shape): tax-inclusive pricing (the
	// German norm), the real PriceResolverAdapter, the real plugin-backed
	// tax asker.
	engine := pos.NewServiceWithResolver(pos.Config{TaxInclusive: true},
		ui.PriceResolverAdapter{Store: ui.NewButtonStore(dp.Db)})
	engine.SetTaxRateAsker(&pluginTaxRateAsker{db: dp.Db})
	dp.Engine = engine

	if saveOverrideViaEditor {
		// Save the 7% override through the REAL typed takeaway-overrides
		// editor (plugin_settings_page.go) — the same handler the merchant's
		// settings screen posts to, including its BumpGeneration.
		settingsMux := http.NewServeMux()
		registerPluginSettings(settingsMux, dp)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+pluginID+"/settings",
			strings.NewReader("setting_takeaway_typed=1&takeaway_pct_"+taxID+"=7"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		settingsMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("save takeaway override: %d (%s)", rec.Code, rec.Body.String())
		}
		// Prove the save actually landed on the row the plugin will read — if
		// the install hadn't seeded the declared setting, the editor would
		// silently have written nothing and the assertions below would fail
		// for the wrong reason.
		stored, found, err := data.NewPluginRepo(dp.Db).GetPluginSetting(ctx, pluginID, "takeaway_rate_overrides")
		if err != nil || !found {
			t.Fatalf("takeaway_rate_overrides row missing after save (found=%v err=%v)", found, err)
		}
		if !strings.Contains(stored, taxID) || !strings.Contains(stored, "700") {
			t.Fatalf("saved override not in stored setting: %q", stored)
		}
	}

	posMux := http.NewServeMux()
	registerPOSAPI(posMux, dp)
	return &takeawayRealChainFixture{dp: primary, engine: engine, posMux: posMux, taxID: taxID}
}

// TestTakeawayOverride_RealChain_FreshAddUsesOverriddenRate is the issue's
// exact repro: fresh basket, toggle Takeaway FIRST, then add ONE Cappuccino.
// The basket must show €0.25 tax (7% inside the €3.80 gross), not €0.61
// (19%).
func TestTakeawayOverride_RealChain_FreshAddUsesOverriddenRate(t *testing.T) {
	fx := newTakeawayRealChainFixture(t, true)

	if rec := posPostForm(fx.posMux, "/api/pos/order-type", "order_type=takeaway"); rec.Code != http.StatusOK {
		t.Fatalf("set order type: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := posPostForm(fx.posMux, "/api/pos/scan", "code=30005"); rec.Code != http.StatusOK {
		t.Fatalf("scan: %d (%s)", rec.Code, rec.Body.String())
	}

	b := fx.engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].SKU != "30005" {
		t.Fatalf("expected exactly the Cappuccino line, got %+v", b.Lines)
	}
	if b.Lines[0].TaxCodeID != fx.taxID {
		t.Fatalf("resolved line lost its tax code: %+v", b.Lines[0])
	}
	// Gross-inclusive invariant: the customer owes €3.80 either way.
	if b.Total.Minor() != 380 {
		t.Fatalf("total = %d, want 380 (tax-inclusive gross)", b.Total.Minor())
	}
	// 7% inside €3.80 gross = €0.25 (380 − 380/1.07). 19% would be €0.61 —
	// the exact over-collection the café reported.
	if b.Tax.Minor() != 25 {
		t.Fatalf("VAT over-collection (ut-docs#1351): tax = %d minor units, want 25 (7%% takeaway rate); 61 means the 19%% dine-in rate was charged", b.Tax.Minor())
	}
}

// TestTakeawayOverride_RealChain_AddThenToggle is the reverse order — add
// while dine-in (19% applies), then switch the whole sale to Takeaway — the
// mid-order switch SetOrderType exists for. Same chain, both directions.
func TestTakeawayOverride_RealChain_AddThenToggle(t *testing.T) {
	fx := newTakeawayRealChainFixture(t, true)

	if rec := posPostForm(fx.posMux, "/api/pos/scan", "code=30005"); rec.Code != http.StatusOK {
		t.Fatalf("scan: %d (%s)", rec.Code, rec.Body.String())
	}
	b := fx.engine.Basket()
	if b.Tax.Minor() != 61 {
		t.Fatalf("dine-in tax = %d, want 61 (19%% inside €3.80)", b.Tax.Minor())
	}

	if rec := posPostForm(fx.posMux, "/api/pos/order-type", "order_type=takeaway"); rec.Code != http.StatusOK {
		t.Fatalf("set order type: %d (%s)", rec.Code, rec.Body.String())
	}
	b = fx.engine.Basket()
	if b.Tax.Minor() != 25 {
		t.Fatalf("after switching to takeaway: tax = %d, want 25", b.Tax.Minor())
	}
	if b.Total.Minor() != 380 {
		t.Fatalf("gross-inclusive invariant broken by the switch: total = %d, want 380", b.Total.Minor())
	}

	// And back to dine-in — the switch must be fully reversible.
	if rec := posPostForm(fx.posMux, "/api/pos/order-type", "order_type=dine_in"); rec.Code != http.StatusOK {
		t.Fatalf("set order type back: %d", rec.Code)
	}
	if b = fx.engine.Basket(); b.Tax.Minor() != 61 {
		t.Fatalf("after switching back to dine-in: tax = %d, want 61", b.Tax.Minor())
	}
}

// TestTakeawayOverride_RealChain_ImportSeededOverrideInvalidatesAskCache is
// ut-docs#1351's actual root cause. pluginTaxRateAsker memoizes each ask
// answer per bus GENERATION, and every path that writes plugin settings
// bumps the generation — except the catalog import's takeaway-overrides
// merge (ut-docs#512, import_page.go mergeTakeawayOverrides). The pilot
// till's timeline hits exactly that gap:
//
//  1. catalog imported while the tax plugin was disabled/not-yet-configured:
//     tax codes created and items assigned, overrides silently skipped
//     (the documented ut-docs#531 branch);
//  2. a takeaway Cappuccino rings at its dine-in 19% — correct at that
//     moment (no override configured), and the plugin's "no opinion" is
//     cached for this exact (item, tax code, 1900bp, "takeaway") payload;
//  3. the merchant re-imports the same catalog to get the overrides in
//     (the tax codes dedup to the SAME ids, so the payload is unchanged)
//     — the 7% override lands in plugin_settings, but nothing bumps the
//     generation;
//  4. fresh basket, toggle Takeaway, add ONE Cappuccino: the asker serves
//     the STALE cached "no opinion" instead of re-asking the plugin, and
//     the basket shows €0.61 (19%) instead of €0.25 — indefinitely, until
//     an unrelated plugin reload or settings save happens to bump.
//
// Step 3 calls mergeTakeawayOverrides directly — the same function the
// import commit handler calls, which (post-fix) owns the generation bump;
// everything downstream of the write is the full real chain (real wasm
// plugin, real settings row, real handlers).
func TestTakeawayOverride_RealChain_ImportSeededOverrideInvalidatesAskCache(t *testing.T) {
	fx := newTakeawayRealChainFixture(t, false) // no override configured yet
	ctx := t.Context()

	// (2) A takeaway sale before the override exists: 19% is correct here,
	// and the plugin's "no opinion" for this payload is now cached.
	if rec := posPostForm(fx.posMux, "/api/pos/order-type", "order_type=takeaway"); rec.Code != http.StatusOK {
		t.Fatalf("set order type: %d", rec.Code)
	}
	if rec := posPostForm(fx.posMux, "/api/pos/scan", "code=30005"); rec.Code != http.StatusOK {
		t.Fatalf("scan: %d", rec.Code)
	}
	if b := fx.engine.Basket(); b.Tax.Minor() != 61 {
		t.Fatalf("pre-override takeaway tax = %d, want 61 (no override configured yet)", b.Tax.Minor())
	}
	// Sale completes — the tender path resets the engine (pos_api.go's
	// completeTender calls engine.Reset()).
	fx.engine.Reset()

	// (3) The re-import merges the discovered override — the ut-docs#512
	// path, exactly what the import commit handler runs.
	added, failed := mergeTakeawayOverrides(ctx, fx.dp.dp.Db, map[string]int{fx.taxID: 700})
	if failed || added != 1 {
		t.Fatalf("merge takeaway override: added=%d failed=%v", added, failed)
	}

	// (4) The issue's repro, verbatim: fresh basket, toggle Takeaway, add
	// ONE Cappuccino from empty.
	if rec := posPostForm(fx.posMux, "/api/pos/order-type", "order_type=takeaway"); rec.Code != http.StatusOK {
		t.Fatalf("set order type: %d", rec.Code)
	}
	if rec := posPostForm(fx.posMux, "/api/pos/scan", "code=30005"); rec.Code != http.StatusOK {
		t.Fatalf("scan: %d", rec.Code)
	}
	b := fx.engine.Basket()
	if b.Total.Minor() != 380 {
		t.Fatalf("total = %d, want 380", b.Total.Minor())
	}
	if b.Tax.Minor() != 25 {
		t.Fatalf("VAT over-collection (ut-docs#1351): tax = %d minor units, want 25 — the import's override write did not invalidate the tax-ask cache, so the stale pre-override answer (19%%) is still being served", b.Tax.Minor())
	}
}
