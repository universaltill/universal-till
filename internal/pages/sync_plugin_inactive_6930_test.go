package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6930: a listing the main has off (enabled:false) installs on the
// replica already switched off, through the real PersistManifest, so the
// active-owner exclusivity checks never refuse it and it never runs here.

type installCall6930 struct {
	listing            string
	inactive           bool
	activeAfterPersist bool
}

// stubPersistingSyncInstall swaps pluginSyncInstall for a func that runs the
// REAL plugins.PersistManifest with the flags the sync passes, so the
// exclusivity checks are actually exercised. It bypasses signature
// verification on purpose; T5 and T14 cover the real installer chain.
func stubPersistingSyncInstall(t *testing.T, manifests map[string]*plugins.Manifest) *[]installCall6930 {
	t.Helper()
	orig := pluginSyncInstall
	t.Cleanup(func() { pluginSyncInstall = orig })
	var calls []installCall6930
	pluginSyncInstall = func(ctx context.Context, d *common.Deps, listing, version string, inactive bool) (string, error) {
		src, ok := manifests[listing]
		if !ok {
			t.Fatalf("no manifest for listing %s", listing)
		}
		m := *src
		m.Version = version
		if err := plugins.PersistManifest(ctx, d.Db, &m, plugins.InstallOptions{TrustLevel: "trusted", InstallInactive: inactive, SkipActiveOwnerChecks: inactive}); err != nil {
			calls = append(calls, installCall6930{listing: listing, inactive: inactive})
			return "", err
		}
		on, err := data.NewPluginRepo(d.Db).PluginActive(ctx, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, installCall6930{listing: listing, inactive: inactive, activeAfterPersist: on})
		if err := plugins.NewInstallStatusStore(d.Db).Save(ctx, plugins.InstallStatusRecord{
			ListingID: listing, PluginID: m.ID, PluginName: m.Name, CurrentVersion: version, State: plugins.InstallStateActive,
		}); err != nil {
			t.Fatal(err)
		}
		// Like the real install's tail (cloudInstallPluginVersionChecked).
		if err := d.ReloadPlugins(ctx); err != nil {
			t.Fatal(err)
		}
		return "installed", nil
	}
	return &calls
}

func presetManifest6930(id, key string) *plugins.Manifest {
	return &plugins.Manifest{
		ID: id, Name: "Layout " + id, Version: "1.0.0", Runtime: "none", CanonicalType: "layout",
		Entries: []plugins.ManifestEntry{{
			Type: "layout", Key: "menu", Label: "Layout " + id,
			Config: map[string]any{"slot": "menu", "role": "preset", "amendments": []any{map[string]any{"key": key, "hide": true}}},
		}},
	}
}

func signerManifest6930(id string) *plugins.Manifest {
	return &plugins.Manifest{
		ID: id, Name: "Signer " + id, Version: "1.0.0", Entrypoint: "./main.wasm",
		Hooks: []plugins.ManifestHook{{Event: "fiscal.sign.ask", Action: "fiscal.sign"}},
	}
}

func row6930(listing, id string, enabled *bool) data.PluginSyncRow {
	return row6666(listing, id, enabled)
}

func assertNoOpenSyncProblems6930(t *testing.T, listings ...string) {
	t.Helper()
	for _, l := range listings {
		for _, p := range []string{pluginSyncInstallKeyPrefix, "plugin_sync.state:"} {
			if logging.HasOpenProblem(p + l) {
				t.Fatalf("open problem %s%s: %+v", p, l, logging.Recent())
			}
		}
	}
}

func TestConvergePluginSet_PassesInactiveOnlyForExplicitFalse(t *testing.T) {
	dp := newDeps6666(t, "6930-flag.db")
	mans := map[string]*plugins.Manifest{
		"LN": {ID: "com.t.n", Name: "n", Version: "1.0.0", Runtime: "none", CanonicalType: "page"},
		"LT": {ID: "com.t.t", Name: "t", Version: "1.0.0", Runtime: "none", CanonicalType: "page"},
		"LF": {ID: "com.t.f", Name: "f", Version: "1.0.0", Runtime: "none", CanonicalType: "page"},
	}
	calls := stubPersistingSyncInstall(t, mans)
	rows := []data.PluginSyncRow{
		row6930("LN", "com.t.n", nil),
		row6930("LT", "com.t.t", boolp6666(true)),
		row6930("LF", "com.t.f", boolp6666(false)),
	}
	if !convergePluginSet(t.Context(), dp, rows) {
		t.Fatal("not converged")
	}
	got := map[string]bool{}
	for _, c := range *calls {
		got[c.listing] = c.inactive
	}
	if len(got) != 3 || got["LN"] || got["LT"] || !got["LF"] {
		t.Fatalf("inactive flags = %v, want LN=false LT=false LF=true", got)
	}
}

func TestConvergePluginSet_DisabledPresetInstallsInactive_BothOrders(t *testing.T) {
	runBothOrders6930(t, "preset", map[string]*plugins.Manifest{
		"LX": presetManifest6930("com.t.x", "/tables"),
		"LY": presetManifest6930("com.t.y", "/kitchen-stations"),
	})
}

func TestConvergePluginSet_DisabledFiscalSignInstallsInactive_BothOrders(t *testing.T) {
	runBothOrders6930(t, "fiscal", map[string]*plugins.Manifest{
		"LX": signerManifest6930("com.t.x"),
		"LY": signerManifest6930("com.t.y"),
	})
}

func runBothOrders6930(t *testing.T, name string, mans map[string]*plugins.Manifest) {
	for _, order := range []string{"X-first", "Y-first"} {
		t.Run(order, func(t *testing.T) {
			dp := newDeps6666(t, "6930-"+name+"-"+order+".db")
			calls := stubPersistingSyncInstall(t, mans)
			rows := []data.PluginSyncRow{
				row6930("LX", "com.t.x", boolp6666(true)),
				row6930("LY", "com.t.y", boolp6666(false)),
			}
			if order == "Y-first" {
				rows[0], rows[1] = rows[1], rows[0]
			}
			if !convergePluginSet(t.Context(), dp, rows) {
				t.Fatalf("not converged: %+v", logging.Recent())
			}
			if !active6666(t, dp, "com.t.x") || active6666(t, dp, "com.t.y") {
				t.Fatalf("x=%v y=%v, want x on, y off", active6666(t, dp, "com.t.x"), active6666(t, dp, "com.t.y"))
			}
			if st, ok := installStatusState(t, dp, "LY"); !ok || st != string(plugins.InstallStateActive) {
				t.Fatalf("Y install state = %q,%v", st, ok)
			}
			for _, c := range *calls {
				if c.listing == "LY" && c.activeAfterPersist {
					t.Fatal("Y was active right after persist (it would have been loaded)")
				}
			}
			assertNoOpenSyncProblems6930(t, "LX", "LY")
			n := len(*calls)
			if !convergePluginSet(t.Context(), dp, rows) || len(*calls) != n {
				t.Fatalf("second tick: installs %d -> %d, want a no-op", n, len(*calls))
			}
			if _, changed := followMarketplaceState(t.Context(), dp, rows); changed {
				t.Fatal("follow flipped something on the second pass")
			}
		})
	}
}

func TestConvergePluginSet_MissingEnabledStillRefusesSecondPreset(t *testing.T) {
	dp := newDeps6666(t, "6930-nil-refuse.db")
	mans := map[string]*plugins.Manifest{
		"LX": presetManifest6930("com.t.x", "/tables"),
		"LY": presetManifest6930("com.t.y", "/kitchen-stations"),
	}
	stubPersistingSyncInstall(t, mans)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("LX", "com.t.x", boolp6666(true))}) {
		t.Fatal("setup: X did not install")
	}
	rows := []data.PluginSyncRow{row6930("LX", "com.t.x", boolp6666(true)), row6930("LY", "com.t.y", nil)}
	if convergePluginSet(t.Context(), dp, rows) {
		t.Fatal("a second active preset (enabled absent) must not converge")
	}
	if _, ok := pluginInstalledVersion(t, dp, "com.t.y"); ok {
		t.Fatal("Y was installed")
	}
	if !logging.HasOpenProblem(pluginSyncInstallKeyPrefix + "LY") {
		t.Fatalf("install problem missing: %+v", logging.Recent())
	}
	if !heartbeatReportsProblem(t, dp, "preset") {
		t.Fatalf("refusal text (preset) not reported: %+v", collectProblems(t.Context(), dp))
	}
}

func TestSyncPullPlugins_CachedPre6666BundleInstallsActive(t *testing.T) {
	dp := newDeps6666(t, "6930-cached.db")
	mans := map[string]*plugins.Manifest{"LY": {ID: "com.t.y", Name: "y", Version: "1.0.0", Runtime: "none", CanonicalType: "page"}}
	calls := stubPersistingSyncInstall(t, mans)
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	// A pre-#6666 bundle: no "enabled" key at all.
	raw := `{"plugins":[{"listing_id":"LY","plugin_id":"com.t.y","plugin_name":"y","version":"1.0.0"}]}`
	var b data.PluginsBundle
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	if b.Plugins[0].Enabled != nil {
		t.Fatal("test bundle unexpectedly carries enabled")
	}
	if err := dp.Settings.Set(t.Context(), "sync.plugins_bundle", raw); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(t.Context(), "sync.plugins_version", b.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": pluginsBundleResponse{Version: b.Fingerprint(), Unchanged: true}, "error": nil})
	}))
	defer srv.Close()
	syncPullPlugins(t.Context(), dp, srv.Client(), srv.URL, "tok")
	if len(*calls) != 1 || (*calls)[0].inactive || !(*calls)[0].activeAfterPersist {
		t.Fatalf("calls = %+v, want one active install", *calls)
	}
}

func TestConvergePluginSet_UpdateWhileMainHasItOffNeverActive(t *testing.T) {
	dp := newDeps6666(t, "6930-update.db")
	mans := map[string]*plugins.Manifest{"LY": presetManifest6930("com.t.y", "/kitchen-stations")}
	calls := stubPersistingSyncInstall(t, mans)
	v1 := row6930("LY", "com.t.y", boolp6666(true))
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{v1}) || !active6666(t, dp, "com.t.y") {
		t.Fatal("setup: v1 not installed active")
	}
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dp.Pm.InstalledIDs(), "com.t.y") {
		t.Fatal("setup: v1 not loaded")
	}
	v2 := row6930("LY", "com.t.y", boolp6666(false))
	v2.Version = "2.0.0"
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{v2}) {
		t.Fatalf("not converged: %+v", logging.Recent())
	}
	if ver, _ := pluginInstalledVersion(t, dp, "com.t.y"); ver != "2.0.0" {
		t.Fatalf("version = %q, want 2.0.0", ver)
	}
	if active6666(t, dp, "com.t.y") {
		t.Fatal("updated plugin is active though the main has it off")
	}
	last := (*calls)[len(*calls)-1]
	if !last.inactive || last.activeAfterPersist {
		t.Fatalf("update call = %+v, want inactive and off right after persist", last)
	}
	if slices.Contains(dp.Pm.InstalledIDs(), "com.t.y") {
		t.Fatal("a plugin the main has off is still loaded here")
	}
}

func TestConvergePluginSet_LaterEnableGatedAfterInactiveInstall(t *testing.T) {
	setup := func(t *testing.T, name string) (*common.Deps, []data.PluginSyncRow) {
		dp := newDeps6666(t, name)
		stubPersistingSyncInstall(t, map[string]*plugins.Manifest{
			"LX": presetManifest6930("com.t.x", "/tables"),
			"LY": presetManifest6930("com.t.y", "/kitchen-stations"),
		})
		rows := []data.PluginSyncRow{row6930("LX", "com.t.x", boolp6666(true)), row6930("LY", "com.t.y", boolp6666(false))}
		if !convergePluginSet(t.Context(), dp, rows) {
			t.Fatalf("setup not converged: %+v", logging.Recent())
		}
		return dp, rows
	}
	t.Run("swap", func(t *testing.T) {
		dp, rows := setup(t, "6930-later-swap.db")
		rows[0].Enabled, rows[1].Enabled = boolp6666(false), boolp6666(true)
		if !convergePluginSet(t.Context(), dp, rows) {
			t.Fatalf("swap not converged: %+v", logging.Recent())
		}
		if active6666(t, dp, "com.t.x") || !active6666(t, dp, "com.t.y") {
			t.Fatal("want X off, Y on")
		}
	})
	t.Run("both-on-refused", func(t *testing.T) {
		dp, rows := setup(t, "6930-later-refuse.db")
		rows[1].Enabled = boolp6666(true)
		if convergePluginSet(t.Context(), dp, rows) {
			t.Fatal("two active presets must not converge")
		}
		if active6666(t, dp, "com.t.y") || !active6666(t, dp, "com.t.x") {
			t.Fatal("want Y to stay off, X on")
		}
		if !logging.HasOpenProblem("plugin_sync.state:LY") {
			t.Fatalf("state problem missing: %+v", logging.Recent())
		}
	})
}
