package pages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/bundled"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// Apple 2.5.2 / ADR-0194: through every page-layer path the App Store build
// refuses a code plugin, and the marketplace sees no request at all.

// codeGateEnv: a marketplace server that counts every hit, a deps whose local
// catalog lists a payment and a theme listing, and the given channel.
func codeGateEnv(t *testing.T, ch buildinfo.InstallChannel) (*http.ServeMux, *common.Deps, *atomic.Int32) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	t.Cleanup(buildinfo.OverrideChannelForTest(ch))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{
		EndpointURL: srv.URL, ClientID: "m", StoreID: "s", DeviceID: "d",
		PublicKey: strings.Repeat("ab", 32),
	})
	d.CatalogRepo = newCatalogRepoWithSnapshot(t, marketplace.CatalogSnapshot{
		Locale: "en-US", DeviceArch: marketplace.DeviceArch(),
		Plugins: []marketplace.PluginSummary{
			{ListingID: "lst-pay", ID: "lst-pay", Name: "Pay", Version: "1.0.0", CanonicalType: "payment"},
			{ListingID: "lst-theme", ID: "lst-theme", Name: "Theme", Version: "1.0.0", CanonicalType: "theme"},
		},
	})
	registerPluginAPI(mux, d)
	return mux, d, &hits
}

func storeErrKey(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	_, msg := storeRespOf(t, rec)
	return msg
}

func TestAppStore_StoreDownloadOfCodeListingRefused_NoNetwork(t *testing.T) {
	mux, _, hits := codeGateEnv(t, buildinfo.ChannelAppStore)
	for _, id := range []string{"lst-pay", "lst-unknown"} { // unknown fails closed too
		rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {id}, "accept_terms": {"on"}}, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("download %s = %d, want 403 (%s)", id, rec.Code, rec.Body.String())
		}
		if k := storeErrKey(t, rec); k != plugins.CodeInstallRefusedMsgID {
			t.Fatalf("download %s error = %q, want %q", id, k, plugins.CodeInstallRefusedMsgID)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("marketplace saw %d request(s), want 0", n)
	}
}

func TestAppStore_StoreDownloadOfThemeStillReachesMarketplace(t *testing.T) {
	mux, _, hits := codeGateEnv(t, buildinfo.ChannelAppStore)
	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-theme"}, "accept_terms": {"on"}}, nil)
	if k := storeErrKey(t, rec); k == plugins.CodeInstallRefusedMsgID {
		t.Fatalf("a theme was refused as code (%d)", rec.Code)
	}
	if hits.Load() == 0 {
		t.Fatal("a theme download never reached the marketplace")
	}
}

func TestSideloadPlayDirect_CodeListingStillReachesMarketplace(t *testing.T) {
	for _, ch := range []buildinfo.InstallChannel{buildinfo.ChannelSideload, buildinfo.ChannelPlay, buildinfo.ChannelDirect} {
		mux, _, hits := codeGateEnv(t, ch)
		rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-pay"}, "accept_terms": {"on"}}, nil)
		if k := storeErrKey(t, rec); k == plugins.CodeInstallRefusedMsgID {
			t.Fatalf("%s refused a marketplace code plugin", ch)
		}
		if hits.Load() == 0 {
			t.Fatalf("%s: the marketplace was never asked", ch)
		}
	}
}

func TestAppStore_InstallFromMarketplaceRefused_NoNetwork(t *testing.T) {
	mux, _, hits := codeGateEnv(t, buildinfo.ChannelAppStore)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/install-from-marketplace",
		strings.NewReader(`{"listing_id":"lst-pay","accept_terms":true}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("install = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(rec.Body.String(), plugins.CodeInstallRefusedMsgID) {
		t.Fatalf("body = %s, want key %s", rec.Body.String(), plugins.CodeInstallRefusedMsgID)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("marketplace saw %d request(s), want 0", n)
	}
}

// The cloud-directive / replica / auto-install entry point and the update
// path (applyPluginUpdate): refused before any status write or request.
func TestAppStore_DirectiveAndUpdatePathsRefused_NoNetwork(t *testing.T) {
	_, d, hits := codeGateEnv(t, buildinfo.ChannelAppStore)
	if _, err := cloudInstallPluginVersion(t.Context(), d, "lst-pay", "1.0.0"); err == nil || !strings.Contains(err.Error(), "code plugins cannot be installed") {
		t.Fatalf("cloud directive install err = %v, want the code refusal", err)
	}
	if _, err := cloudInstallPlugin(t.Context(), d, "lst-pay"); err == nil {
		t.Fatal("unpinned cloud install of a code plugin was not refused")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("marketplace saw %d request(s), want 0", n)
	}
	recs, _ := plugins.NewInstallStatusStore(d.Db).List(t.Context())
	if len(recs) != 0 {
		t.Fatalf("a refused directive left install-status records: %v", recs)
	}
}

func TestAppStore_ImportFromFileRefusesCodePlugin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	manifest := `{"id":"com.test.imp","name":"I","version":"1.0.0","entrypoint":"./p.wasm","runtime":"wasm","canonical_type":"page","device_arch":"any"}`
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("trust_level", "untrusted")
	fw, _ := mw.CreateFormFile("plugin_file", "p.tar.gz")
	fw.Write(tarGzForTest(t, map[string]string{"manifest.json": manifest, "p.wasm": "x"}))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/import-from-file", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("import = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.test.imp'`).Scan(&n)
	if n != 0 {
		t.Fatal("a refused import left a plugin row")
	}
}

// A cashier can never switch a bundled plugin on or off: the check is
// server-side, and the stored flag does not move.
func TestBundledPlugin_CashierCannotToggle_ManagerCan(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedForPages(t, db)
	const id = "com.universaltill.payment-stripe"
	seedBundledRow(t, db, id)
	dp := newPluginAPIDeps(t, db, nil)
	dp.AuthSvc = auth.NewService(db)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	active := func() bool {
		var a int
		if err := db.QueryRow(`SELECT is_active FROM plugins WHERE id = ?`, id).Scan(&a); err != nil {
			t.Fatal(err)
		}
		return a == 1
	}
	do := func(path, role string) int {
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, path, nil), auth.User{ID: "u1", Role: role})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("/api/plugins/"+id+"/enable", "cashier"); c != http.StatusForbidden || active() {
		t.Fatalf("cashier enable = %d, active=%v; want 403 and still off", c, active())
	}
	if c := do("/api/plugins/"+id+"/enable", "manager"); c != http.StatusOK || !active() {
		t.Fatalf("manager enable = %d, active=%v; want 200 and on", c, active())
	}
	if c := do("/api/plugins/"+id+"/disable", "cashier"); c != http.StatusForbidden || !active() {
		t.Fatalf("cashier disable = %d, active=%v; want 403 and still on", c, active())
	}
	if c := do("/api/plugins/"+id+"/disable", "admin"); c != http.StatusOK || active() {
		t.Fatalf("admin disable = %d, active=%v; want 200 and off", c, active())
	}
}

// seedBundledRow persists a first-party payment plugin the way the bundled
// loader does (lock-verified, system trust) and leaves it DISABLED.
func seedBundledRow(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	m := &plugins.Manifest{ID: id, Name: "Card", Version: "1.5.0", Entrypoint: "./bin/plugin.wasm",
		Runtime: "wasm", CanonicalType: "payment", DeviceArch: "any", MinPOSVersion: "1.0.0"}
	if err := plugins.PersistManifest(t.Context(), db, m, plugins.InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
		t.Fatalf("persist bundled row: %v", err)
	}
	if err := data.NewPluginRepo(db).SetPluginActive(t.Context(), nil, id, false); err != nil {
		t.Fatal(err)
	}
}

func tarGzForTest(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A shop-initiated or scheduled update of an installed code plugin never
// leaves the till on the App Store channel (it moves with an app release).
func TestAppStore_ApplyPluginUpdateRefused_NoNetwork(t *testing.T) {
	_, d, hits := codeGateEnv(t, buildinfo.ChannelAppStore)
	const id = "com.universaltill.payment-stripe"
	seedBundledRow(t, d.Db, id)
	if err := plugins.NewInstallStatusStore(d.Db).Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "lst-pay", PluginID: id, State: plugins.InstallStateActive, CurrentVersion: "1.5.0",
	}); err != nil {
		t.Fatal(err)
	}
	if err := data.NewPluginRepo(d.Db).SetPluginActive(t.Context(), nil, id, true); err != nil {
		t.Fatal(err)
	}
	if err := d.Pm.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, _, err := applyPluginUpdate(t.Context(), d, id)
	if !errors.Is(err, plugins.ErrCodePluginRefused) {
		t.Fatalf("applyPluginUpdate err = %v, want ErrCodePluginRefused", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("marketplace saw %d request(s), want 0", n)
	}
}

// Settings > Plugins: the bundled set is flagged (system trust + in the lock)
// and listed first, no marketplace update is offered for it, a same-id plugin
// installed any other way is NOT flagged, and a bundled plugin cannot be
// uninstalled (the shop switches it off instead).
func TestPluginsPage_BuiltInGroupFlagsOnlyLockedSystemPlugins(t *testing.T) {
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	d := pluginsManagerTestDeps(t)
	const stripe = "com.universaltill.payment-stripe"
	const sumup = "com.universaltill.payment-sumup"
	seedTestPlugin(t, d.Db, "com.x.alpha", "Alpha", "1.0.0") // sorts before the built-ins by name
	seedBundledRow(t, d.Db, stripe)
	// Same id as a locked plugin but marketplace-trusted: not "built in".
	seedTestPlugin(t, d.Db, sumup, "Sum", "1.0.0")

	mux := http.NewServeMux()
	registerPluginsPage(mux, d)
	items := pluginsManagerJSON(t, mux)
	if !items[stripe].Bundled {
		t.Fatalf("locked system plugin not flagged built in: %+v", items[stripe])
	}
	if items["com.x.alpha"].Bundled || items[sumup].Bundled {
		t.Fatalf("a non-bundled plugin was flagged built in: %+v / %+v", items["com.x.alpha"], items[sumup])
	}
	if items[stripe].HasUpdate || items[stripe].VersionUnknown {
		t.Fatalf("a built-in plugin offers a marketplace update state: %+v", items[stripe])
	}
	// First in the payload (the page emits one heading for the group).
	req := httptest.NewRequest(http.MethodGet, "/plugins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if i, j := strings.Index(body, `"id":"`+stripe), strings.Index(body, `"id":"com.x.alpha"`); i < 0 || j < 0 || i > j {
		t.Fatalf("built-in plugin is not listed before the others (stripe at %d, alpha at %d)", i, j)
	}
	if !strings.Contains(body, "plugins-built-in-heading") {
		t.Fatal("page has no Built in heading")
	}
}

func TestBundledPlugin_UninstallRefusedAndKept(t *testing.T) {
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	isolatePluginsDir(t)
	t.Setenv("UT_AUTH", "off")
	db := openRealSchemaPagesDB(t)
	const id = "com.universaltill.payment-stripe"
	seedBundledRow(t, db, id)
	dp := newPluginAPIDeps(t, db, nil)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plugins/"+id+"/uninstall", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "plugins.manage.error.bundled_no_uninstall") {
		t.Fatalf("uninstall = %d %s, want 409 bundled_no_uninstall", rec.Code, rec.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = ?`, id).Scan(&n)
	if n != 1 {
		t.Fatal("the bundled plugin row was removed")
	}
}

// Keys and settings stay editable while the bundled plugin is switched off.
func TestBundledPlugin_SettingsPageWorksWhileDisabled(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	mux, dp := newPluginSettingsTestDeps(t)
	const id = "com.universaltill.payment-stripe"
	seedBundledRow(t, dp.Db, id)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/"+id+"/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("settings page of a disabled bundled plugin = %d, want 200", rec.Code)
	}
	var active int
	_ = dp.Db.QueryRow(`SELECT is_active FROM plugins WHERE id = ?`, id).Scan(&active)
	if active != 0 {
		t.Fatal("opening settings must not switch the plugin on")
	}
}
