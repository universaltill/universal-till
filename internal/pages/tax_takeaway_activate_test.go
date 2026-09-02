package pages

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#1370: the OTHER direction of ut-docs#512/#1351. The catalog is
// imported (or hand-built) FIRST — tax codes with a pinned takeaway rate
// exist — and the German tax plugin is installed or re-enabled LATER. The
// import path's mergeTakeawayOverrides only ever runs at import-commit time
// with the plugin ALREADY active (the ut-docs#531 branch skips it
// otherwise), so nothing ever folded those tax codes' pinned takeaway rates
// into the plugin's takeaway_rate_overrides setting. The settings screen then
// rendered value="" placeholder="7" — LOOKS configured, isn't — and every
// takeaway sale charged the dine-in 19%: a live §12 UStG VAT over-collection
// with the operator having done nothing wrong.
//
// Product decision (2026-09-01): a successful install/enable of the country
// plugin IS the consent boundary — the pinned takeaway rates become ACTIVE
// overrides immediately. Add-only: a merchant-set override is never
// overwritten.
//
// These tests run the WHOLE real chain like tax_takeaway_realchain_test.go:
// the same signed wasm tax plugin installed through the REAL HTTP install
// handler (the one the fix hooks into), the real settings row, the production
// PriceResolverAdapter, and the real /api/pos/order-type + /api/pos/scan
// handlers. They reuse that file's fixture pieces rather than a parallel one.

// installTaxDeViaHTTP installs the fixture's tax plugin through the REAL
// POST /api/plugins/install-from-marketplace handler (handleInstallFromMarketplace)
// — not through primary.install's cloudInstallPlugin shortcut — inside the
// primary's own paths scope, so the test proves the operator-facing install
// button's path is the one that reconciles. Returns the response so a caller
// can assert the envelope.
func installTaxDeViaHTTP(t *testing.T, primary *syncPluginsPrimary, listingID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerPluginAPI(mux, primary.dp)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/install-from-marketplace",
		strings.NewReader(`{"listing_id":"`+listingID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	withPaths(primary.dataDir, func() { mux.ServeHTTP(rec, req) })
	if rec.Code != http.StatusOK {
		t.Fatalf("install %s via HTTP handler: %d (%s)", listingID, rec.Code, rec.Body.String())
	}
	return rec
}

// setTaxDeActiveViaHTTP flips the fixture's tax plugin through the REAL
// POST /api/plugins/{id}/enable|disable handlers (setPluginActiveHandler),
// inside the primary's paths scope so the post-flip ReloadPlugins finds the
// installed wasm where the install put it.
func setTaxDeActiveViaHTTP(t *testing.T, primary *syncPluginsPrimary, verb string) {
	t.Helper()
	mux := http.NewServeMux()
	registerPluginAPI(mux, primary.dp)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+taxDePluginID+"/"+verb, nil)
	rec := httptest.NewRecorder()
	withPaths(primary.dataDir, func() { mux.ServeHTTP(rec, req) })
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s via HTTP handler: %d (%s)", verb, taxDePluginID, rec.Code, rec.Body.String())
	}
}

// storedTakeawayOverrides reads the plugin's takeaway_rate_overrides row as
// the plugin itself would see it (unwrapping a string-wrapped value the way
// hostSettingsGet does) and decodes it — so an assertion on a single entry
// pinpoints "reconcile didn't write" vs "write happened but the ask path
// didn't pick it up".
func storedTakeawayOverrides(t *testing.T, primary *syncPluginsPrimary) map[string]int {
	t.Helper()
	return storedTakeawayOverridesDB(t, primary.dp.Db)
}

// storedTakeawayOverridesDB is storedTakeawayOverrides for the tests that
// build their till from a bare real-schema DB (import-from-file, Plugin
// Store) rather than a syncPluginsPrimary.
func storedTakeawayOverridesDB(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	raw, found, err := data.NewPluginRepo(db).GetPluginSetting(t.Context(), taxDePluginID, "takeaway_rate_overrides")
	if err != nil {
		t.Fatalf("read takeaway_rate_overrides: %v", err)
	}
	if !found {
		t.Fatalf("takeaway_rate_overrides row missing — the install did not seed the manifest-declared setting")
	}
	var out map[string]int
	if err := json.Unmarshal([]byte(unwrapSettingValue(raw)), &out); err != nil {
		t.Fatalf("takeaway_rate_overrides is not a JSON map: %q (%v)", raw, err)
	}
	return out
}

// pinnedCatalogThenPublishTaxDe builds ut-docs#1370's precondition on an
// already-built primary: the issue's catalog shape (tax code "Imported 19%
// (takeaway 7%)" + Cappuccino SKU 30005) exists FIRST, and only THEN is the
// settings-reading tax plugin published to the fake marketplace as
// "listing-taxov" — published, not installed, so each test picks the
// activation path it pins. Returns the tax code id the reconcile must write.
func pinnedCatalogThenPublishTaxDe(t *testing.T, mkt *fakeMarketplace, primary *syncPluginsPrimary) string {
	t.Helper()
	ctx := t.Context()
	catRepo := data.NewCatalogRepo(primary.dp.Db)
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
	guest := buildTaxOverridesAskGuest(t)
	mkt.publishWasmTaxVersionWithSettings(t, "listing-taxov", taxDePluginID, "1.0.0", guest,
		[]plugins.ManifestSetting{{
			Key:          "takeaway_rate_overrides",
			DefaultValue: map[string]any{},
			Scope:        "global",
		}})
	return taxID
}

// TestTakeawayOverride_RealChain_PluginInstalledAfterCatalog is ut-docs#1370's
// exact scenario: the catalog (tax code "Imported 19% (takeaway 7%)" +
// Cappuccino SKU 30005) exists BEFORE the German tax plugin is installed;
// the plugin is then installed through the real install-from-marketplace
// handler and the operator does NOTHING else — no settings-editor visit, no
// re-import. A fresh takeaway basket with one Cappuccino must show €0.25 tax
// (7% inside €3.80 gross), not the €0.61 (19%) the pilot till charged.
func TestTakeawayOverride_RealChain_PluginInstalledAfterCatalog(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	primary := newSyncPluginsPrimary(t, mkt)
	dp := primary.dp

	// (1) The catalog first, (2) THEN the plugin arrives, through the real
	// HTTP install handler.
	taxID := pinnedCatalogThenPublishTaxDe(t, mkt, primary)
	installTaxDeViaHTTP(t, primary, "listing-taxov")

	// The reconcile must have written the pinned rate as an ACTIVE override —
	// checked on the row itself first so a failure below can't be misread as
	// an ask-cache problem.
	if got := storedTakeawayOverrides(t, primary); got[taxID] != 700 {
		t.Fatalf("ut-docs#1370: install did not reconcile the catalog's pinned takeaway rate into takeaway_rate_overrides: got %v, want {%q: 700}", got, taxID)
	}

	// (3) Production engine wiring, and the issue's repro verbatim: fresh
	// basket, toggle Takeaway, add ONE Cappuccino.
	engine := pos.NewServiceWithResolver(pos.Config{TaxInclusive: true},
		ui.PriceResolverAdapter{Store: ui.NewButtonStore(dp.Db)})
	engine.SetTaxRateAsker(&pluginTaxRateAsker{db: dp.Db})
	dp.Engine = engine
	posMux := http.NewServeMux()
	registerPOSAPI(posMux, dp)

	if rec := posPostForm(posMux, "/api/pos/order-type", "order_type=takeaway"); rec.Code != http.StatusOK {
		t.Fatalf("set order type: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := posPostForm(posMux, "/api/pos/scan", "code=30005"); rec.Code != http.StatusOK {
		t.Fatalf("scan: %d (%s)", rec.Code, rec.Body.String())
	}
	b := engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].SKU != "30005" || b.Lines[0].TaxCodeID != taxID {
		t.Fatalf("expected exactly the Cappuccino line on tax code %s, got %+v", taxID, b.Lines)
	}
	if b.Total.Minor() != 380 {
		t.Fatalf("total = %d, want 380 (tax-inclusive gross)", b.Total.Minor())
	}
	if b.Tax.Minor() != 25 {
		t.Fatalf("VAT over-collection (ut-docs#1370): tax = %d minor units, want 25 (7%% takeaway rate); 61 means the 19%% dine-in rate was charged because the plugin's takeaway_rate_overrides was never reconciled from the pre-existing catalog", b.Tax.Minor())
	}
}

// TestTakeawayOverride_RealChain_CloudInstallReconciles pins the
// cloudInstallPluginVersion call site (cloudsync_wire.go) — the path a
// primary's directive / sync-pull install and a pinned-version upgrade go
// through, NOT the HTTP install button. Round 2 of ut-docs#1370's review
// proved that with only the HTTP-handler tests, reverting this one site's
// wiring left the whole package green; this test fails without it.
func TestTakeawayOverride_RealChain_CloudInstallReconciles(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	primary := newSyncPluginsPrimary(t, mkt)

	taxID := pinnedCatalogThenPublishTaxDe(t, mkt, primary)
	primary.install(t, "listing-taxov") // cloudInstallPlugin → cloudInstallPluginVersion

	if got := storedTakeawayOverrides(t, primary); got[taxID] != 700 {
		t.Fatalf("ut-docs#1370: cloud install (cloudsync_wire.go) did not reconcile the catalog's pinned takeaway rate into takeaway_rate_overrides: got %v, want {%q: 700}", got, taxID)
	}
}

// TestTakeawayOverride_ImportFromFileReconciles pins the offline-provisioning
// path (POST /api/plugins/import-from-file, handleImportFromFile): a
// sideloaded bundle of the German tax plugin is activated unconditionally by
// PersistManifest, so it must reconcile like every other activation. Round 2
// of ut-docs#1370's review found this operator-facing path unwired. No
// marketplace key is configured (the dev/offline default), so the unsigned
// asset-only bundle imports the way a real offline provisioning does.
func TestTakeawayOverride_ImportFromFileReconciles(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	takeawayBP := 700
	taxID, err := data.NewCatalogRepo(db).CreateTaxCode(t.Context(), "Imported 19% (takeaway 7%)", 1900, &takeawayBP)
	if err != nil {
		t.Fatalf("create tax code: %v", err)
	}

	bundle := writePluginBundle(t, plugins.Manifest{
		ID: taxDePluginID, Name: "Tax DE (sideloaded)", Version: "1.0.0",
		Runtime: "none", CanonicalType: "theme", DeviceArch: "any",
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, importRequest(t, bundle, "tax-de.tar.gz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("import-from-file: %d (%s)", rec.Code, rec.Body.String())
	}

	if got := storedTakeawayOverridesDB(t, db); got[taxID] != 700 {
		t.Fatalf("ut-docs#1370: import-from-file did not reconcile the catalog's pinned takeaway rate into takeaway_rate_overrides: got %v, want {%q: 700}", got, taxID)
	}
}

// TestTakeawayOverride_StoreInstallReconciles pins the Plugin Store's
// "download, then install" button (POST /api/plugins/store/install), the
// second operator-facing activation path round 2 of ut-docs#1370's review
// found unwired. The bundle is staged exactly where DownloadToStore leaves
// it, then installed through the real handler.
func TestTakeawayOverride_StoreInstallReconciles(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	m, pubHex := signedAssetOnlyManifest(t, taxDePluginID, "Tax DE (store)")
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: "http://127.0.0.1:0",
		PublicKey:   pubHex,
	})

	takeawayBP := 700
	taxID, err := data.NewCatalogRepo(d.Db).CreateTaxCode(t.Context(), "Imported 19% (takeaway 7%)", 1900, &takeawayBP)
	if err != nil {
		t.Fatalf("create tax code: %v", err)
	}

	stageSignedStoreBundle(t, m, "lst-taxde")
	rec := postForm(mux, "/api/plugins/store/install", url.Values{"listing_id": {"lst-taxde"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("store install: %d (%s)", rec.Code, rec.Body.String())
	}

	if got := storedTakeawayOverridesDB(t, d.Db); got[taxID] != 700 {
		t.Fatalf("ut-docs#1370: Plugin Store install did not reconcile the catalog's pinned takeaway rate into takeaway_rate_overrides: got %v, want {%q: 700}", got, taxID)
	}
}

// TestTakeawayOverride_RealChain_ReenableNeverClobbersMerchantOverride pins
// the ADD-ONLY half of the product decision. A merchant who has deliberately
// set a DIFFERENT override than the catalog's pinned rate (5% here, vs the
// tax code's pinned 7%) must keep it across a disable → re-enable cycle —
// the re-enable reconcile must fill missing entries only, never overwrite.
// The override is saved through the real typed settings editor and the
// flip goes through the real enable/disable handlers, so this proves the
// wired enable path, not the helper in isolation.
func TestTakeawayOverride_RealChain_ReenableNeverClobbersMerchantOverride(t *testing.T) {
	fx := newTakeawayRealChainFixture(t, false) // plugin installed, override absent
	dp := fx.dp.dp

	// The install (with the tax code created AFTER it in this fixture) left
	// the map empty — the precondition that makes the editor save below the
	// merchant's explicit choice, not a reconcile artefact.
	if got := storedTakeawayOverrides(t, fx.dp); len(got) != 0 {
		t.Fatalf("precondition: expected no overrides right after install with an empty catalog, got %v", got)
	}

	// Merchant sets 5% by hand through the REAL typed editor.
	settingsMux := http.NewServeMux()
	registerPluginSettings(settingsMux, dp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+taxDePluginID+"/settings",
		strings.NewReader("setting_takeaway_typed=1&takeaway_pct_"+fx.taxID+"=5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	settingsMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save takeaway override: %d (%s)", rec.Code, rec.Body.String())
	}
	if got := storedTakeawayOverrides(t, fx.dp); got[fx.taxID] != 500 {
		t.Fatalf("editor save did not land: got %v, want {%q: 500}", got, fx.taxID)
	}

	// A second pinned tax code that arrived while the plugin was configured
	// but was never reconciled (e.g. a later import). Its presence makes
	// this test load-bearing in BOTH directions: the re-enable must ADD
	// this one (proving the enable path really ran the reconcile — without
	// it the "kept 500" assertion below would pass trivially) while
	// KEEPING the merchant's 500 on the first.
	pastryBP := 700
	pastryID, err := data.NewCatalogRepo(dp.Db).CreateTaxCode(t.Context(), "Pastry 19% (takeaway 7%)", 1900, &pastryBP)
	if err != nil {
		t.Fatalf("create second tax code: %v", err)
	}

	// Disable, then re-enable — the re-enable is an activation and runs the
	// reconcile, which now sees a tax code pinned at 700 for a key that
	// already holds the merchant's 500, plus a brand-new pinned one.
	setTaxDeActiveViaHTTP(t, fx.dp, "disable")
	setTaxDeActiveViaHTTP(t, fx.dp, "enable")

	got := storedTakeawayOverrides(t, fx.dp)
	if got[fx.taxID] != 500 {
		t.Fatalf("re-enable clobbered the merchant's explicit override: got %v, want %q: 500 (add-only guarantee, ut-docs#1370)", got, fx.taxID)
	}
	if got[pastryID] != 700 {
		t.Fatalf("re-enable did not reconcile the not-yet-configured tax code: got %v, want %q: 700 — the enable path did not run the reconcile", got, pastryID)
	}
	if len(got) != 2 {
		t.Fatalf("stored overrides = %v, want exactly the two entries", got)
	}

	// And the till charges the merchant's 5%, not the catalog's 7%:
	// 5% inside €3.80 gross = 380 − round(380/1.05) = 380 − 362 = €0.18.
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
	if b.Tax.Minor() != 18 {
		t.Fatalf("tax = %d minor units, want 18 (merchant's 5%%); 25 means the catalog's 7%% overwrote the merchant's value, 61 means no override applied at all", b.Tax.Minor())
	}
}

// TestReconcileTaxDeTakeawayOverridesOnActivate_Unit exercises the helper
// directly (no HTTP, no wasm) for the edge cases the real-chain tests can't
// isolate cheaply: a catalog with nothing to reconcile must be a clean no-op
// (no row written, no generation bump — the bump is what forces every
// cached tax answer to be re-asked, so a spurious one is not free), a
// dine-in-only tax code (nil TakeawayRateBP) is skipped, and a pinned one is
// written exactly once — a second call is idempotent.
func TestReconcileTaxDeTakeawayOverridesOnActivate_Unit(t *testing.T) {
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	ctx := t.Context()
	// The plugin row the setting hangs off (FK), seeded through the real
	// PersistManifest — the same code path a real install goes through.
	seedInstalledPlugin(t, db, taxDePluginID, "1.0.0")
	pluginRepo := data.NewPluginRepo(db)
	catRepo := data.NewCatalogRepo(db)
	bus := plugins.SharedBus(db)

	assertNoRow := func(stage string) {
		t.Helper()
		if _, found, err := pluginRepo.GetPluginSetting(ctx, taxDePluginID, "takeaway_rate_overrides"); err != nil || found {
			t.Fatalf("%s: expected no takeaway_rate_overrides row (found=%v err=%v)", stage, found, err)
		}
	}

	// Empty catalog: no-op, no write, no bump.
	gen := bus.Generation()
	if added, failed := reconcileTaxDeTakeawayOverridesOnActivate(ctx, db); added != 0 || failed {
		t.Fatalf("empty catalog: added=%d failed=%v, want 0/false", added, failed)
	}
	assertNoRow("empty catalog")
	if bus.Generation() != gen {
		t.Fatalf("empty catalog: generation bumped %d → %d on a no-op", gen, bus.Generation())
	}

	// A dine-in-only tax code (no pinned takeaway rate) is not an override
	// candidate: still a no-op.
	if _, err := catRepo.CreateTaxCode(ctx, "Standard 19%", 1900, nil); err != nil {
		t.Fatalf("create dine-in-only tax code: %v", err)
	}
	gen = bus.Generation()
	if added, failed := reconcileTaxDeTakeawayOverridesOnActivate(ctx, db); added != 0 || failed {
		t.Fatalf("dine-in-only tax code: added=%d failed=%v, want 0/false", added, failed)
	}
	assertNoRow("dine-in-only tax code")
	if bus.Generation() != gen {
		t.Fatalf("dine-in-only tax code: generation bumped on a no-op")
	}

	// A pinned tax code is reconciled — exactly that one entry — and the
	// generation is bumped so pluginTaxRateAsker's per-generation cache
	// re-asks (ut-docs#1351).
	takeawayBP := 700
	taxID, err := catRepo.CreateTaxCode(ctx, "Imported 19% (takeaway 7%)", 1900, &takeawayBP)
	if err != nil {
		t.Fatalf("create pinned tax code: %v", err)
	}
	gen = bus.Generation()
	if added, failed := reconcileTaxDeTakeawayOverridesOnActivate(ctx, db); added != 1 || failed {
		t.Fatalf("pinned tax code: added=%d failed=%v, want 1/false", added, failed)
	}
	if bus.Generation() == gen {
		t.Fatalf("pinned tax code: generation not bumped after a write — cached tax answers would go stale")
	}
	raw, found, err := pluginRepo.GetPluginSetting(ctx, taxDePluginID, "takeaway_rate_overrides")
	if err != nil || !found {
		t.Fatalf("row missing after reconcile (found=%v err=%v)", found, err)
	}
	var got map[string]int
	if err := json.Unmarshal([]byte(unwrapSettingValue(raw)), &got); err != nil {
		t.Fatalf("stored value not a JSON map: %q (%v)", raw, err)
	}
	if len(got) != 1 || got[taxID] != 700 {
		t.Fatalf("stored overrides = %v, want exactly {%q: 700}", got, taxID)
	}

	// Idempotent: a second activation with nothing new adds nothing and
	// does not bump.
	gen = bus.Generation()
	if added, failed := reconcileTaxDeTakeawayOverridesOnActivate(ctx, db); added != 0 || failed {
		t.Fatalf("second activation: added=%d failed=%v, want 0/false", added, failed)
	}
	if bus.Generation() != gen {
		t.Fatalf("second activation: generation bumped with nothing added")
	}
}
