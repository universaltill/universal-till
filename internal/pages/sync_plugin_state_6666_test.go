package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/bundled"
)

// ut-docs#6666: a main till's enable/disable of a marketplace plugin follows
// on replicas, whatever happens to other listings' installs.

func boolp6666(v bool) *bool { return &v }

// seed6666 installs a marketplace plugin locally (plugins row, install status
// 'active', optional exclusive hooks / preset layout entry).
func seed6666(t *testing.T, dp *common.Deps, listing, id string, active bool, hooks []string, preset bool) {
	t.Helper()
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT OR IGNORE INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES (?, '1.0.0', 'x', 'wasm', './plugin', 'https://marketplace.invalid/x', 'deadbeef', '2.5.0', '1.0', '2026-01-01T00:00:00Z')`, id); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	on := 0
	if active {
		on = 1
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?, ?, '1.0.0', './plugin', 'wasm', ?)`, id, id, on); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	for _, ev := range hooks {
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO plugin_hooks (id, plugin_id, event, action) VALUES (?, ?, ?, 'act')`, id+ev, id, ev); err != nil {
			t.Fatalf("seed hook: %v", err)
		}
	}
	if preset {
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO plugin_entries (id, plugin_id, type, key, label, config_json) VALUES (?, ?, 'layout', 'main', 'L', '{"role":"preset"}')`, id+"-e", id); err != nil {
			t.Fatalf("seed preset entry: %v", err)
		}
	}
	if listing != "" {
		if err := plugins.NewInstallStatusStore(dp.Db).Save(ctx, plugins.InstallStatusRecord{
			ListingID: listing, PluginID: id, PluginName: id, CurrentVersion: "1.0.0", State: plugins.InstallStateActive,
		}); err != nil {
			t.Fatalf("seed install status: %v", err)
		}
	}
}

func row6666(listing, id string, enabled *bool) data.PluginSyncRow {
	return data.PluginSyncRow{ListingID: listing, PluginID: id, PluginName: id, Version: "1.0.0", Enabled: enabled}
}

func active6666(t *testing.T, dp *common.Deps, id string) bool {
	t.Helper()
	v, err := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func newDeps6666(t *testing.T, name string) *common.Deps {
	t.Helper()
	dp := newMigratedSyncDeps(t, name)
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	stubRecompute(t)
	return dp
}

func TestConvergePluginSet_FollowsMainDisable(t *testing.T) {
	dp := newDeps6666(t, "6666-disable.db")
	seed6666(t, dp, "L1", "com.test.tax-de", true, nil, false)
	calls := stubPluginSyncInstall(t, nil)

	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6666("L1", "com.test.tax-de", boolp6666(false))}) {
		t.Fatal("converge reported not converged")
	}
	if active6666(t, dp, "com.test.tax-de") {
		t.Fatal("the main disabled the plugin; the replica kept it on")
	}
	if st, ok := installStatusState(t, dp, "L1"); !ok || st != string(plugins.InstallStateActive) {
		t.Fatalf("install state = %q,%v; a disabled plugin stays installed", st, ok)
	}
	if _, ok := pluginInstalledVersion(t, dp, "com.test.tax-de"); !ok {
		t.Fatal("the plugin row was removed")
	}
	if len(*calls) != 0 {
		t.Fatalf("install calls = %v, want none", *calls)
	}
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6666("L1", "com.test.tax-de", boolp6666(true))}) {
		t.Fatal("re-enable: not converged")
	}
	if !active6666(t, dp, "com.test.tax-de") {
		t.Fatal("the main re-enabled the plugin; the replica kept it off")
	}
}

func TestConvergePluginSet_MissingEnabledLeavesStateAlone(t *testing.T) {
	dp := newDeps6666(t, "6666-nil.db")
	seed6666(t, dp, "L1", "com.test.p", false, nil, false)
	stubPluginSyncInstall(t, nil)
	r := []data.PluginSyncRow{row6666("L1", "com.test.p", nil)}
	if !convergePluginSet(t.Context(), dp, r) || active6666(t, dp, "com.test.p") {
		t.Fatal("a row without the field must leave a locally-off plugin off")
	}
	if err := data.NewPluginRepo(dp.Db).SetPluginActive(t.Context(), nil, "com.test.p", true); err != nil {
		t.Fatal(err)
	}
	if !convergePluginSet(t.Context(), dp, r) || !active6666(t, dp, "com.test.p") {
		t.Fatal("a missing field must never mean off")
	}
}

func TestConvergePluginSet_StateFollowIndependentOfFailingInstall(t *testing.T) {
	dp := newDeps6666(t, "6666-indep.db")
	seed6666(t, dp, "L1", "com.test.tax-de", true, nil, false)
	stubPluginSyncInstall(t, errors.New("persist plugin manifest: boom"))
	rows := []data.PluginSyncRow{
		row6666("L2", "com.test.other", boolp6666(true)), // not installed: its install fails first
		row6666("L1", "com.test.tax-de", boolp6666(false)),
	}
	for i := 0; i < 2; i++ {
		if convergePluginSet(t.Context(), dp, rows) {
			t.Fatalf("tick %d: converged despite a failing install", i)
		}
		if active6666(t, dp, "com.test.tax-de") {
			t.Fatalf("tick %d: tax-de still on behind a failing install", i)
		}
	}
	if !heartbeatReportsProblem(t, dp, "failed (will retry)") {
		t.Fatalf("install problem missing: %+v", collectProblems(t.Context(), dp))
	}
}

func TestSyncPullPlugins_FreshRowsWhileInstallFails(t *testing.T) {
	dp := newDeps6666(t, "6666-fresh.db")
	seed6666(t, dp, "L1", "com.test.tax-de", true, nil, false)
	stubPluginSyncInstall(t, errors.New("persist plugin manifest: boom"))
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	var l1 atomic.Value
	l1.Store(false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := data.PluginsBundle{Plugins: []data.PluginSyncRow{
			row6666("L1", "com.test.tax-de", boolp6666(l1.Load().(bool))),
			row6666("L2", "com.test.other", boolp6666(true)),
		}}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": pluginsBundleResponse{Version: b.Fingerprint(), Bundle: b}, "error": nil})
	}))
	defer srv.Close()
	ver := func() string { v, _, _ := dp.Settings.Get(t.Context(), "sync.plugins_version"); return v }

	syncPullPlugins(t.Context(), dp, srv.Client(), srv.URL, "tok")
	if active6666(t, dp, "com.test.tax-de") || ver() != "" {
		t.Fatalf("tick 1: active=%v version=%q, want off and not advanced", active6666(t, dp, "com.test.tax-de"), ver())
	}
	l1.Store(true)
	syncPullPlugins(t.Context(), dp, srv.Client(), srv.URL, "tok")
	if !active6666(t, dp, "com.test.tax-de") {
		t.Fatal("tick 2: the main's re-enable did not arrive (stale rows)")
	}
	if raw, _, _ := dp.Settings.Get(t.Context(), "sync.plugins_bundle"); raw != "" || ver() != "" {
		t.Fatalf("the fingerprint/cache advanced despite a failing install (%q, %q)", ver(), raw)
	}
}

// ut-docs#6930: a fresh install of a plugin the main has off lands off in the
// persist transaction (never active, never loaded), so the follow has nothing
// to flip.
func TestConvergePluginSet_FreshInstallFollowsDisabled(t *testing.T) {
	dp := newDeps6666(t, "6666-fresh-install.db")
	orig := pluginSyncInstall
	t.Cleanup(func() { pluginSyncInstall = orig })
	var sawInactive bool
	pluginSyncInstall = func(ctx context.Context, d *common.Deps, listing, version string, inactive bool) (string, error) {
		sawInactive = inactive
		seed6666(t, d, listing, "com.test.new", !inactive, nil, false) // a real install lands off when inactive
		return "", nil
	}
	rows := []data.PluginSyncRow{row6666("L1", "com.test.new", boolp6666(false))}
	if !convergePluginSet(t.Context(), dp, rows) {
		t.Fatal("not converged")
	}
	if !sawInactive {
		t.Fatal("the install was not asked to land inactive")
	}
	if active6666(t, dp, "com.test.new") {
		t.Fatal("a fresh install of a plugin the main has off must end off")
	}
	if _, changed := followMarketplaceState(t.Context(), dp, rows); changed {
		t.Fatal("the follow flipped a plugin that already landed off")
	}
}

func TestConvergePluginSet_EnableRefusedByFiscalExclusivity(t *testing.T) {
	dp := newDeps6666(t, "6666-fiscal-refuse.db")
	stubPluginSyncInstall(t, nil)
	seed6666(t, dp, "", "com.test.tse-a", true, []string{"fiscal.sign.ask"}, false) // stale local, not listed
	seed6666(t, dp, "LB", "com.test.tse-b", false, []string{"fiscal.sign.ask"}, false)
	rows := []data.PluginSyncRow{row6666("LB", "com.test.tse-b", boolp6666(true))}

	if convergePluginSet(t.Context(), dp, rows) {
		t.Fatal("a refused enable must not report converged")
	}
	if active6666(t, dp, "com.test.tse-b") {
		t.Fatal("two fiscal-sign providers are now active")
	}
	if !logging.HasOpenProblem("plugin_sync.state:LB") {
		t.Fatalf("keyed state problem missing: %+v", logging.Recent())
	}
	convergePluginSet(t.Context(), dp, nil) // the main dropped LB
	if logging.HasOpenProblem("plugin_sync.state:LB") {
		t.Fatal("a dropped listing's state problem stays open")
	}
}

func TestConvergePluginSet_DisablesBeforeEnables(t *testing.T) {
	for name, preset := range map[string]bool{"fiscal": false, "preset": true} {
		t.Run(name, func(t *testing.T) {
			dp := newDeps6666(t, "6666-order-"+name+".db")
			stubPluginSyncInstall(t, nil)
			var hooks []string
			if !preset {
				hooks = []string{"fiscal.sign.ask"}
			}
			seed6666(t, dp, "LA", "com.test.a", true, hooks, preset)
			seed6666(t, dp, "LB", "com.test.b", false, hooks, preset)
			rows := []data.PluginSyncRow{ // B first: ordering must not depend on slice order
				row6666("LB", "com.test.b", boolp6666(true)),
				row6666("LA", "com.test.a", boolp6666(false)),
			}
			if !convergePluginSet(t.Context(), dp, rows) {
				t.Fatal("swap did not converge in one tick")
			}
			if active6666(t, dp, "com.test.a") || !active6666(t, dp, "com.test.b") {
				t.Fatalf("a=%v b=%v, want a off and b on", active6666(t, dp, "com.test.a"), active6666(t, dp, "com.test.b"))
			}
		})
	}
}

func TestConvergePluginSet_DisabledListingNotPruned(t *testing.T) {
	dp := newDeps6666(t, "6666-noprune.db")
	seed6666(t, dp, "L1", "com.test.p", true, nil, false)
	stubPluginSyncInstall(t, nil)
	removes := 0
	orig := pluginSyncRemove
	t.Cleanup(func() { pluginSyncRemove = orig })
	pluginSyncRemove = func(context.Context, *common.Deps, string) (string, error) { removes++; return "", nil }
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6666("L1", "com.test.p", boolp6666(false))}) {
		t.Fatal("not converged")
	}
	if removes != 0 {
		t.Fatalf("a disabled listing was uninstalled (%d)", removes)
	}
	if _, ok := pluginInstalledVersion(t, dp, "com.test.p"); !ok {
		t.Fatal("record gone")
	}
}

func TestFollowBundledState_MarketplaceRowDisabledIsOff(t *testing.T) {
	t.Cleanup(bundled.OverrideAvailableForTest(true))
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	seedBundledRow(t, db, bundledStripeID)
	dp := newPluginAPIDeps(t, db, nil)
	repo := data.NewPluginRepo(db)
	if err := repo.SetPluginActive(t.Context(), nil, bundledStripeID, true); err != nil {
		t.Fatal(err)
	}
	row := data.PluginSyncRow{ListingID: "lst-stripe", PluginID: bundledStripeID, PluginName: "Stripe", Version: "1.6.0", Enabled: boolp6666(false)}
	if ok, _ := followBundledState(t.Context(), dp, []data.PluginSyncRow{row}); !ok {
		t.Fatal("not ok")
	}
	if on, _ := repo.PluginActive(t.Context(), bundledStripeID); on {
		t.Fatal("the main holds a DISABLED marketplace copy; the bundled plugin must follow it off")
	}
}

// A failed local switch (the DB refuses the plugins UPDATE; injected with a
// trigger since SetPluginActive has no seam) raises the keyed problem and
// clears once the switch lands.
func TestConvergePluginSet_FailedSwitchRaisesKeyedProblem(t *testing.T) {
	dp := newDeps6666(t, "6666-failed-switch.db")
	stubPluginSyncInstall(t, nil)
	seed6666(t, dp, "LD", "com.test.fail-off", true, nil, false)
	rows := []data.PluginSyncRow{row6666("LD", "com.test.fail-off", boolp6666(false))}
	if _, err := dp.Db.ExecContext(t.Context(), `CREATE TRIGGER t6666_block BEFORE UPDATE ON plugins BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if convergePluginSet(t.Context(), dp, rows) {
		t.Fatal("a failed switch must not report converged")
	}
	if !logging.HasOpenProblem("plugin_sync.state:LD") {
		t.Fatalf("keyed state problem missing: %+v", logging.Recent())
	}
	if _, err := dp.Db.ExecContext(t.Context(), `DROP TRIGGER t6666_block`); err != nil {
		t.Fatal(err)
	}
	convergePluginSet(t.Context(), dp, rows)
	if active6666(t, dp, "com.test.fail-off") || logging.HasOpenProblem("plugin_sync.state:LD") {
		t.Fatal("retry must switch off and clear the problem")
	}
}
