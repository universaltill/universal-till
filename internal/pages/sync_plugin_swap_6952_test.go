package pages

import (
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6952: a main that swaps exclusive owners (X off, Y on) converges a
// replica holding X active and no Y in ONE tick: the follow pass disables X
// before Y's install, so PersistManifest's exclusive-owner refusal never fires
// and no plugin_sync.install problem is raised.

func runSwap6952(t *testing.T, name string, mans map[string]*plugins.Manifest) {
	dp := newDeps6666(t, "6952-"+name+".db")
	calls := stubPersistingSyncInstall(t, mans)
	// Replica starts with X installed and active, no Y.
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("LX", "com.t.x", boolp6666(true))}) {
		t.Fatalf("setup: X did not install: %+v", logging.Recent())
	}
	if !active6666(t, dp, "com.t.x") {
		t.Fatal("setup: X not active")
	}
	// Main swaps: X off, Y on.
	rows := []data.PluginSyncRow{row6930("LX", "com.t.x", boolp6666(false)), row6930("LY", "com.t.y", boolp6666(true))}
	if !convergePluginSet(t.Context(), dp, rows) {
		t.Fatalf("swap did not converge in one tick: %+v", logging.Recent())
	}
	if active6666(t, dp, "com.t.x") || !active6666(t, dp, "com.t.y") {
		t.Fatalf("x=%v y=%v, want x off, y on", active6666(t, dp, "com.t.x"), active6666(t, dp, "com.t.y"))
	}
	if st, ok := installStatusState(t, dp, "LY"); !ok || st != string(plugins.InstallStateActive) {
		t.Fatalf("Y install state = %q,%v", st, ok)
	}
	if logging.HasOpenProblem(pluginSyncInstallKeyPrefix + "LY") {
		t.Fatalf("plugin_sync.install problem raised: %+v", logging.Recent())
	}
	assertNoOpenSyncProblems6930(t, "LX", "LY")
	n := len(*calls)
	if !convergePluginSet(t.Context(), dp, rows) || len(*calls) != n {
		t.Fatalf("second tick: installs %d -> %d, want a no-op", n, len(*calls))
	}
}

func TestConvergePluginSet_SwapPresetOwnersInOneTick(t *testing.T) {
	runSwap6952(t, "preset", map[string]*plugins.Manifest{
		"LX": presetManifest6930("com.t.x", "/tables"),
		"LY": presetManifest6930("com.t.y", "/kitchen-stations"),
	})
}

func TestConvergePluginSet_SwapFiscalSignOwnersInOneTick(t *testing.T) {
	runSwap6952(t, "fiscal", map[string]*plugins.Manifest{
		"LX": signerManifest6930("com.t.x"),
		"LY": signerManifest6930("com.t.y"),
	})
}

// ut-docs#6952 review: a plugin_sync.state problem left open (planted here;
// in practice from an earlier tick) is closed by the after-installs follow
// pass once the install lands the plugin on, as the main has it.
func TestConvergePluginSet_ActiveInstallResolvesStateProblem(t *testing.T) {
	dp := newDeps6666(t, "6952-state.db")
	stubPersistingSyncInstall(t, map[string]*plugins.Manifest{
		"LY": presetManifest6930("com.t.y", "/kitchen-stations"),
	})
	warnPluginSyncOnce(pluginSyncStateKeyPrefix+"LY", "plugin sync: test-raised state problem for %s", "LY")
	if !logging.HasOpenProblem(pluginSyncStateKeyPrefix + "LY") {
		t.Fatal("setup: state problem not open")
	}
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("LY", "com.t.y", boolp6666(true))}) {
		t.Fatalf("install did not converge: %+v", logging.Recent())
	}
	if !active6666(t, dp, "com.t.y") {
		t.Fatal("Y not active after install")
	}
	if logging.HasOpenProblem(pluginSyncStateKeyPrefix + "LY") {
		t.Fatal("plugin_sync.state problem still open after Y installed on, as the main has it")
	}
}

// ut-docs#6952 edge: the follow pass now runs before the installs, so an
// enable is gated against the version installed HERE. A replica holding a
// revoked v1 switched off, whose main has upgraded to v2 and switched it on,
// must still converge in one tick: the v2 install lands it on (an install
// the main has on lands on), so the pass must not refuse an enable the
// install is about to make moot, nor raise a plugin_sync.state problem.
func TestConvergePluginSet_EnableWithUpgradeNotGatedOnOldVersion(t *testing.T) {
	dp := newDeps6666(t, "6952-upgrade-revoked.db")
	stubPersistingSyncInstall(t, map[string]*plugins.Manifest{
		"LX": {ID: "com.t.x", Name: "x", Version: "1.0.0", Runtime: "none", CanonicalType: "page"},
	})
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("LX", "com.t.x", boolp6666(true))}) {
		t.Fatalf("setup: X did not install: %+v", logging.Recent())
	}
	repo := data.NewPluginRepo(dp.Db)
	if err := repo.SetPluginActive(t.Context(), nil, "com.t.x", false); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReplaceRevokedReleases(t.Context(), []data.RevokedRelease{{ManifestID: "com.t.x", Version: "1.0.0", ListingID: "LX", Reason: "test-6952", Action: "disable"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	row := row6930("LX", "com.t.x", boolp6666(true))
	row.Version = "2.0.0"
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row}) {
		t.Fatalf("upgrade+enable did not converge in one tick: %+v", logging.Recent())
	}
	if v, _, _, _ := data.NewSyncPluginsRepo(dp.Db).InstalledPluginVersion(t.Context(), "com.t.x"); v != "2.0.0" || !active6666(t, dp, "com.t.x") {
		t.Fatalf("version=%s active=%v, want 2.0.0 on", v, active6666(t, dp, "com.t.x"))
	}
	assertNoOpenSyncProblems6930(t, "LX")
}
