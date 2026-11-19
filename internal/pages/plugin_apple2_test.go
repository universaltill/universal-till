package pages

import (
	"bytes"
	"context"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/bundled"
)

const (
	bundledStripeID = "com.universaltill.payment-stripe"
	bundledTaxUKID  = "com.universaltill.tax-uk"
)

// seedBundledTaxRow persists the UK tax plugin the way the bundled loader does
// (lock-verified, system trust) and leaves it DISABLED, as a first boot does.
func seedBundledTaxRow(t *testing.T, db *sql.DB) {
	t.Helper()
	m := &plugins.Manifest{ID: bundledTaxUKID, Name: "UK VAT", Version: "1.3.1", Entrypoint: "./bin/plugin.wasm",
		Runtime: "wasm", CanonicalType: "tax", DeviceArch: "any", MinPOSVersion: "1.0.0",
		Entries: []plugins.ManifestEntry{{Type: "tax", Key: "eatin-standard-rate-uk", Label: "UK VAT"}}}
	if err := plugins.PersistManifest(t.Context(), db, m, plugins.InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
		t.Fatalf("persist bundled tax row: %v", err)
	}
	if err := data.NewPluginRepo(db).SetPluginActive(t.Context(), nil, bundledTaxUKID, false); err != nil {
		t.Fatal(err)
	}
}

func appStoreTaxEnv(t *testing.T) (*common.Deps, *fakeMarketplace) {
	t.Helper()
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	d := newPluginAPIDeps(t, db, nil)
	mkt := newFakeMarketplace(t, map[string]string{})
	publishGBTax(t, mkt)
	d.Cfg.Marketplace = mkt.config()
	return d, mkt
}

// ut-docs#6626: a UK till on the App Store build gets VAT from the bundled UK
// tax plugin: enabled by the tax setup path, with no marketplace use at all.
func TestAppStoreTax_GBTillGetsBundledVATEnabled_NoMarketplace(t *testing.T) {
	d, mkt := appStoreTaxEnv(t)
	seedBundledTaxRow(t, d.Db)
	resetTaxCatalogForTest(t)

	if err := resolveAndInstallBasePlugin(t.Context(), d, basePluginSpec{CanonicalType: "tax", Market: "GB"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if on, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), bundledTaxUKID); !on {
		t.Fatal("a UK till on the App Store build has no VAT plugin enabled")
	}
	if mkt.downloadTokenHits() != 0 || mkt.catalogHitsFor("tax") != 0 {
		t.Fatalf("the App Store tax path used the marketplace: token hits %d, catalogue hits %d", mkt.downloadTokenHits(), mkt.catalogHitsFor("tax"))
	}
	if st := loadTaxAutoInstallStatus(t.Context(), d); st != nil && st.State != taxStateInstalled {
		t.Fatalf("chip = %+v, want none or installed", st)
	}
}

func TestAppStoreTax_DeclinedStaysOff_OtherCountryDoneQuietly(t *testing.T) {
	d, mkt := appStoreTaxEnv(t)
	seedBundledTaxRow(t, d.Db)
	resetTaxCatalogForTest(t)
	// A country with no bundled tax plugin: nothing can be fetched on this
	// channel, so the spec is done at once, with no chip and no network.
	if err := resolveAndInstallBasePlugin(t.Context(), d, basePluginSpec{CanonicalType: "tax", Market: "DE"}); err != nil {
		t.Fatalf("DE resolve = %v, want done", err)
	}
	if st := loadTaxAutoInstallStatus(t.Context(), d); st != nil {
		t.Fatalf("a retry chip for a market the channel cannot serve: %+v", st)
	}
	if on, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), bundledTaxUKID); on {
		t.Fatal("the UK tax plugin was enabled for a German shop")
	}
	if mkt.catalogHitsFor("tax") != 0 || mkt.downloadTokenHits() != 0 {
		t.Fatal("marketplace used for a market the App Store build cannot install code for")
	}
	// An operator who declined keeps it off, whatever the spec says (ADR-0172 §5).
	if err := d.Settings.Set(t.Context(), common.KeyTaxAutoInstallDeclined, bundledTaxUKID); err != nil {
		t.Fatal(err)
	}
	if err := resolveAndInstallBasePlugin(t.Context(), d, basePluginSpec{CanonicalType: "tax", Market: "GB"}); err != nil {
		t.Fatal(err)
	}
	if on, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), bundledTaxUKID); on {
		t.Fatal("a declined UK tax plugin was switched on again")
	}
}

// The other channels keep the marketplace path (the bundled set is empty there).
func TestDirectChannelTax_UnchangedByBundledSet(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelDirect))
	if _, ok := bundledTaxForShop("GB"); ok {
		t.Fatal("a direct build takes the bundled tax path")
	}
}

// Finding 17: on a satellite the bundled switch answers with the plain reason
// (the main till decides), not the generic replica message.
func TestBundledSwitch_SatelliteSaysMainTillControlsIt(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedForPages(t, db)
	seedBundledRow(t, db, bundledStripeID)
	dp := newPluginAPIDeps(t, db, nil)
	dp.AuthSvc = auth.NewService(db)
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	for _, verb := range []string{"enable", "disable"} {
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/plugins/"+bundledStripeID+"/"+verb, nil), auth.User{ID: "u1", Role: "admin"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "plugins.manage.error.bundled_main_till_controls") {
			t.Fatalf("%s on a satellite = %d %s, want 409 bundled_main_till_controls", verb, rec.Code, rec.Body.String())
		}
	}
	if on, _ := data.NewPluginRepo(db).PluginActive(t.Context(), bundledStripeID); on {
		t.Fatal("the satellite switch moved")
	}
}

// Finding 17: the main till's choice reaches the satellite. A bundled plugin
// the main has ON is switched on here, one it has OFF (absent from the main's
// active set) is switched off, the marketplace is never asked for a bundled
// plugin (the version may differ; only the locked bytes run), and converge
// reports converged only once the local state matches.
func TestConvergePluginSet_BundledStateFollowsMainTill(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedBundledRow(t, db, bundledStripeID)
	dp := newPluginAPIDeps(t, db, nil)
	repo := data.NewPluginRepo(db)
	var installs int
	orig := pluginSyncInstall
	pluginSyncInstall = func(context.Context, *common.Deps, string, string) (string, error) { installs++; return "", nil }
	t.Cleanup(func() { pluginSyncInstall = orig })
	on := func() bool { v, _ := repo.PluginActive(t.Context(), bundledStripeID); return v }

	// Main till: stripe ON (a marketplace copy at a newer version, or this very
	// bundled plugin reported with no listing).
	for _, row := range []data.PluginSyncRow{
		{ListingID: "lst-stripe", PluginID: bundledStripeID, PluginName: "Stripe", Version: "1.6.0"},
		{ListingID: "", PluginID: bundledStripeID, PluginName: "Stripe", Version: "1.5.0"},
	} {
		if err := repo.SetPluginActive(t.Context(), nil, bundledStripeID, false); err != nil {
			t.Fatal(err)
		}
		if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row}) {
			t.Fatalf("row %+v: converge reported not converged", row)
		}
		if !on() {
			t.Fatalf("row %+v: converge reported converged while the local bundled plugin is OFF", row)
		}
	}
	// Main till: stripe OFF -> absent from its active set.
	if !convergePluginSet(t.Context(), dp, nil) {
		t.Fatal("converge with the plugin off on the main: not converged")
	}
	if on() {
		t.Fatal("the main till switched stripe off but the satellite kept it ON")
	}
	if installs != 0 {
		t.Fatalf("the marketplace was asked to install a bundled plugin %d times", installs)
	}
}

func TestIsBundledPlugin_NeedsTheEmbeddedSet(t *testing.T) {
	t.Cleanup(bundled.OverrideAvailableForTest(false))
	if isBundledPlugin(bundledStripeID, "system") {
		t.Fatal("a build that embeds nothing shows a system-trust row as Built in (forged import)")
	}
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	if !isBundledPlugin(bundledStripeID, "system") || isBundledPlugin(bundledStripeID, "trusted") {
		t.Fatal("isBundledPlugin is wrong on a build with the set")
	}
}

// L3: a manager cannot import a plugin with trust_level=system (a fake
// "Built in" row on channels that allow imports).
func TestImportFromFile_RejectsSystemTrustLevel(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelDirect))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)
	manifest := `{"id":"com.universaltill.payment-stripe","name":"I","version":"9.0.0","entrypoint":"./p.wasm","runtime":"wasm","canonical_type":"payment","device_arch":"any"}`
	for _, lvl := range []string{"system", "bogus"} {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("trust_level", lvl)
		fw, _ := mw.CreateFormFile("plugin_file", "p.tar.gz")
		_, _ = fw.Write(tarGzForTest(t, map[string]string{"manifest.json": manifest, "p.wasm": "x"}))
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/import-from-file", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("import with trust_level=%s = %d, want 400 (%s)", lvl, rec.Code, rec.Body.String())
		}
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.universaltill.payment-stripe'`).Scan(&n)
	if n != 0 {
		t.Fatal("a rejected import left a row")
	}
}

// Finding 17/18: a bundled row that arrived with a join snapshot at another
// version (rows without bytes) is put back on the embedded plugin at once
// (not at the next boot), and a row already at the locked version is left
// alone (no re-hash of the embed every tick).
func TestConvergePluginSet_BundledRowAtOtherVersionTriggersEmbeddedInstall(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	dp := newPluginAPIDeps(t, db, nil)
	var syncs int
	orig := bundledSyncFn
	bundledSyncFn = func(context.Context, *sql.DB) (bool, error) { syncs++; return true, nil }
	t.Cleanup(func() { bundledSyncFn = orig })

	seedBundledRow(t, db, bundledStripeID) // 1.5.0 = the locked version
	convergePluginSet(t.Context(), dp, nil)
	if syncs != 0 {
		t.Fatalf("a row at the locked version triggered %d embedded installs", syncs)
	}
	other := &plugins.Manifest{ID: bundledStripeID, Name: "Card", Version: "1.9.0", Entrypoint: "./bin/plugin.wasm",
		Runtime: "wasm", CanonicalType: "payment", DeviceArch: "any", MinPOSVersion: "1.0.0"}
	if err := plugins.PersistManifest(t.Context(), db, other, plugins.InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
		t.Fatal(err)
	}
	convergePluginSet(t.Context(), dp, nil)
	if syncs != 1 {
		t.Fatalf("a row at another version triggered %d embedded installs, want 1", syncs)
	}
}
