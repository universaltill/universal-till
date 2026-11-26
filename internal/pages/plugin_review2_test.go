package pages

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/bundled"
)

func review2BundledEnv(t *testing.T) *common.Deps {
	t.Helper()
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedBundledRow(t, db, bundledStripeID)
	return newPluginAPIDeps(t, db, nil)
}

func review2StripeState(t *testing.T, d *common.Deps) (found, active bool, key string) {
	t.Helper()
	repo := data.NewPluginRepo(d.Db)
	_, found, err := repo.PluginTrustLevel(t.Context(), bundledStripeID)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		active, _ = repo.PluginActive(t.Context(), bundledStripeID)
		key, _, _ = repo.GetPluginSetting(t.Context(), bundledStripeID, "secret_key")
	}
	return found, active, key
}

// Review 2 (ut-docs#6565): the uninstall API refuses a bundled plugin because
// an uninstall deletes the shop's keys and the next boot only reinstalls it,
// disabled. The cloud "remove plugin" directive ended in the same uninstall
// with no such check: a bundled plugin is switched off, never uninstalled, by
// every path.
func TestCloudRemovePlugin_BundledIsSwitchedOffNotUninstalled(t *testing.T) {
	d := review2BundledEnv(t)
	repo := data.NewPluginRepo(d.Db)
	if err := repo.UpsertPluginSetting(t.Context(), bundledStripeID, "secret_key", `"sk_test_keepme"`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetPluginActive(t.Context(), nil, bundledStripeID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudRemovePlugin(t.Context(), d, bundledStripeID); err != nil {
		t.Fatalf("cloudRemovePlugin: %v", err)
	}
	found, active, key := review2StripeState(t, d)
	if !found {
		t.Fatal("a cloud remove directive uninstalled a built-in plugin (row and the shop's keys deleted)")
	}
	if active {
		t.Fatal("a cloud remove directive left the built-in plugin switched on")
	}
	if !strings.Contains(key, "sk_test_keepme") {
		t.Fatalf("the shop's plugin setting was lost: %q", key)
	}
}

// Review 2: a satellite that joined a main till holding a marketplace copy of
// a bundled plugin carries that listing's install record (join snapshot). When
// the main till drops the plugin, the satellite's prune loop must switch the
// bundled plugin off, not uninstall it.
func TestConvergePluginSet_PruneNeverUninstallsBundledPlugin(t *testing.T) {
	d := review2BundledEnv(t)
	repo := data.NewPluginRepo(d.Db)
	if err := repo.UpsertPluginSetting(t.Context(), bundledStripeID, "secret_key", `"sk_test_keepme"`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetPluginActive(t.Context(), nil, bundledStripeID, true); err != nil {
		t.Fatal(err)
	}
	if err := plugins.NewInstallStatusStore(d.Db).Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "lst-stripe", PluginID: bundledStripeID, PluginName: "Stripe", State: plugins.InstallStateActive,
	}); err != nil {
		t.Fatal(err)
	}
	orig := pluginSyncInstall
	pluginSyncInstall = func(context.Context, *common.Deps, string, string, bool, []string) (string, error) { return "", nil }
	t.Cleanup(func() { pluginSyncInstall = orig })

	// The main till no longer has the plugin: absent from its set.
	convergePluginSet(t.Context(), d, nil)
	found, active, key := review2StripeState(t, d)
	if !found {
		t.Fatal("the prune loop uninstalled a built-in plugin (row and the shop's keys deleted until the next boot)")
	}
	if active {
		t.Fatal("the main till dropped the plugin but the satellite kept it on")
	}
	if !strings.Contains(key, "sk_test_keepme") {
		t.Fatalf("the shop's plugin setting was lost: %q", key)
	}
	// And it converges: the stale record does not make every tick retry.
	if !convergePluginSet(t.Context(), d, nil) {
		t.Fatal("not converged on the second pass")
	}
}

// Review 2 (L3 residual): "Built in" keys on trust_level = system. A manager
// could move a bundled plugin to another trust level through the trust API;
// until the next boot it then left the Built in group, could be uninstalled
// (keys deleted) and dropped out of the set the main till serves to its
// satellites (which then switched it off).
func TestPluginTrustAPI_RefusesBundledPlugin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	d := review2BundledEnv(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, d)
	form := url.Values{"plugin_id": {bundledStripeID}, "trust_level": {"untrusted"}}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/trust", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("trust change of a built-in plugin = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	trust, _, _ := data.NewPluginRepo(d.Db).PluginTrustLevel(t.Context(), bundledStripeID)
	if trust != "system" {
		t.Fatalf("trust level = %q, want system", trust)
	}
}

// Review 2 (L3): TestImportFromFile_RejectsSystemTrustLevel passes with the
// trust_level check removed, because its unsigned bundle is answered 400 for
// another reason. This one tells the two apart: the refusal is the
// trust-level one, and a known level is not refused by it.
func TestImportFromFile_TrustLevelCheckIsWhatRefuses(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelDirect))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)
	manifest := `{"id":"com.universaltill.payment-stripe","name":"I","version":"9.0.0","entrypoint":"./p.wasm","runtime":"wasm","canonical_type":"payment","device_arch":"any"}`
	post := func(lvl string) *httptest.ResponseRecorder {
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
		return rec
	}
	for _, lvl := range []string{"system", "bogus", "revoked", "trusted"} {
		rec := post(lvl)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid trust_level") {
			t.Fatalf("import with trust_level=%s = %d %q, want 400 invalid trust_level", lvl, rec.Code, rec.Body.String())
		}
	}
	if rec := post("verified"); strings.Contains(rec.Body.String(), "invalid trust_level") {
		t.Fatalf("a known trust level was refused as invalid: %d %q", rec.Code, rec.Body.String())
	}
}

// Review 2 (finding 17): a satellite whose embedded reinstall fails is not
// reported converged, so the pull keeps retrying instead of settling on a
// dead bundled row.
func TestConvergePluginSet_FailedEmbeddedReinstallIsNotConverged(t *testing.T) {
	d := review2BundledEnv(t)
	other := &plugins.Manifest{ID: bundledStripeID, Name: "Card", Version: otherBundledVersion(t, bundledStripeID), Entrypoint: "./bin/plugin.wasm",
		Runtime: "wasm", CanonicalType: "payment", DeviceArch: "any", MinPOSVersion: "1.0.0"}
	if err := plugins.PersistManifest(t.Context(), d.Db, other, plugins.InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
		t.Fatal(err)
	}
	orig := bundledSyncFn
	bundledSyncFn = func(context.Context, *sql.DB) (bool, error) {
		return false, errors.New("embedded bytes do not match the lock")
	}
	t.Cleanup(func() { bundledSyncFn = orig })
	if convergePluginSet(t.Context(), d, nil) {
		t.Fatal("converge reported converged although the embedded bundled plugin could not be reinstalled")
	}
}
