package pages

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
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

// stubDBOnlyRemove7006 swaps pluginSyncRemove for a DB-only uninstall (no
// RemoveAll under the real paths.Plugins()) that logs "remove:<id>".
func stubDBOnlyRemove7006(t *testing.T, log *[]string) {
	t.Helper()
	orig := pluginSyncRemove
	t.Cleanup(func() { pluginSyncRemove = orig })
	pluginSyncRemove = func(ctx context.Context, d *common.Deps, id string) (string, error) {
		*log = append(*log, "remove:"+id)
		if err := plugins.UninstallPlugin(ctx, d.Db, id); err != nil {
			return "", err
		}
		if err := plugins.NewInstallStatusStore(d.Db).ClearForPlugin(ctx, id); err != nil {
			return "", err
		}
		return "uninstalled " + id, d.ReloadPlugins(ctx)
	}
}

// ut-docs#7006: a main that swapped exclusive owners by UNINSTALLING X (no LX
// row at all) and installing Y converges in one tick: X is pruned before Y's
// active install, so PersistManifest's refusal never fires.
func runSwapByUninstall7006(t *testing.T, name string, mans map[string]*plugins.Manifest) {
	dp := newDeps6666(t, "7006-"+name+".db")
	calls := stubPersistingSyncInstall(t, mans)
	var removes []string
	stubDBOnlyRemove7006(t, &removes)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("LX", "com.t.x", boolp6666(true))}) || !active6666(t, dp, "com.t.x") {
		t.Fatalf("setup: X not installed active: %+v", logging.Recent())
	}
	setupInstalls := len(*calls)
	rows := []data.PluginSyncRow{row6930("LY", "com.t.y", boolp6666(true))}
	if !convergePluginSet(t.Context(), dp, rows) {
		t.Fatalf("swap by uninstall did not converge in one tick: %+v", logging.Recent())
	}
	if len(removes) != 1 || removes[0] != "remove:com.t.x" {
		t.Fatalf("removes = %v, want [remove:com.t.x]", removes)
	}
	if _, ok := installStatusState(t, dp, "LX"); ok {
		t.Fatal("LX install record still present")
	}
	if !active6666(t, dp, "com.t.y") {
		t.Fatal("Y not active")
	}
	if st, ok := installStatusState(t, dp, "LY"); !ok || st != string(plugins.InstallStateActive) {
		t.Fatalf("Y install state = %q,%v", st, ok)
	}
	swap := (*calls)[setupInstalls:]
	if len(swap) != 1 || swap[0].listing != "LY" || !swap[0].activeAfterPersist {
		t.Fatalf("swap installs = %+v, want one active LY install", swap)
	}
	if logging.HasOpenProblem(pluginSyncInstallKeyPrefix+"LY") || logging.HasOpenProblem(pluginSyncUninstallKeyPrefix+"LX") {
		t.Fatalf("problem left open: %+v", logging.Recent())
	}
	assertNoOpenSyncProblems6930(t, "LX", "LY")
	n, r := len(*calls), len(removes)
	if !convergePluginSet(t.Context(), dp, rows) || len(*calls) != n || len(removes) != r {
		t.Fatalf("second tick not a no-op: installs %d->%d removes %d->%d", n, len(*calls), r, len(removes))
	}
}

func TestConvergePluginSet_SwapPresetOwnersByUninstallInOneTick(t *testing.T) {
	runSwapByUninstall7006(t, "preset", map[string]*plugins.Manifest{
		"LX": presetManifest6930("com.t.x", "/tables"),
		"LY": presetManifest6930("com.t.y", "/kitchen-stations"),
	})
}

func TestConvergePluginSet_SwapFiscalSignOwnersByUninstallInOneTick(t *testing.T) {
	runSwapByUninstall7006(t, "fiscal", map[string]*plugins.Manifest{
		"LX": signerManifest6930("com.t.x"),
		"LY": signerManifest6930("com.t.y"),
	})
}

// ut-docs#7006 review: the main had X on and Y installed but off (a replica
// installs Y off, ut-docs#6930); it then uninstalls X and switches Y on. The
// prune must run before the state-follow pass, or the first pass tries Y on
// while X is still active, raises a plugin_sync.state problem and needs a
// second tick.
func runSwapByUninstallYInstalledOff7006(t *testing.T, name string, mans map[string]*plugins.Manifest) {
	dp := newDeps6666(t, "7006-off-"+name+".db")
	calls := stubPersistingSyncInstall(t, mans)
	var removes []string
	stubDBOnlyRemove7006(t, &removes)
	setup := []data.PluginSyncRow{row6930("LX", "com.t.x", boolp6666(true)), row6930("LY", "com.t.y", boolp6666(false))}
	if !convergePluginSet(t.Context(), dp, setup) || !active6666(t, dp, "com.t.x") || active6666(t, dp, "com.t.y") {
		t.Fatalf("setup: want X active, Y installed off: %+v", logging.Recent())
	}
	if _, ok := installStatusState(t, dp, "LY"); !ok {
		t.Fatal("setup: Y not installed")
	}
	rows := []data.PluginSyncRow{row6930("LY", "com.t.y", boolp6666(true))}
	if !convergePluginSet(t.Context(), dp, rows) {
		t.Fatalf("swap did not converge in one tick: %+v", logging.Recent())
	}
	if len(removes) != 1 || removes[0] != "remove:com.t.x" {
		t.Fatalf("removes = %v, want [remove:com.t.x]", removes)
	}
	if _, ok := installStatusState(t, dp, "LX"); ok {
		t.Fatal("LX install record still present")
	}
	if !active6666(t, dp, "com.t.y") {
		t.Fatal("Y not active")
	}
	assertNoOpenSyncProblems6930(t, "LX", "LY")
	if logging.HasOpenProblem(pluginSyncStateKeyPrefix+"LY") || logging.HasOpenProblem(pluginSyncInstallKeyPrefix+"LY") || logging.HasOpenProblem(pluginSyncUninstallKeyPrefix+"LX") {
		t.Fatalf("problem left open: %+v", logging.Recent())
	}
	n, r := len(*calls), len(removes)
	if !convergePluginSet(t.Context(), dp, rows) || len(*calls) != n || len(removes) != r {
		t.Fatalf("second tick not a no-op: installs %d->%d removes %d->%d", n, len(*calls), r, len(removes))
	}
}

func TestConvergePluginSet_SwapPresetByUninstallYInstalledOffInOneTick(t *testing.T) {
	runSwapByUninstallYInstalledOff7006(t, "preset", map[string]*plugins.Manifest{
		"LX": presetManifest6930("com.t.x", "/tables"),
		"LY": presetManifest6930("com.t.y", "/kitchen-stations"),
	})
}

func TestConvergePluginSet_SwapFiscalSignByUninstallYInstalledOffInOneTick(t *testing.T) {
	runSwapByUninstallYInstalledOff7006(t, "fiscal", map[string]*plugins.Manifest{
		"LX": signerManifest6930("com.t.x"),
		"LY": signerManifest6930("com.t.y"),
	})
}

// ut-docs#7006: a malformed row (ListingID set, PluginID empty) is not
// in-primary, so the record for that listing is still pruned.
func TestConvergePluginSet_MalformedRowStillNotInPrimary(t *testing.T) {
	dp := newDeps6666(t, "7006-malformed.db")
	stubPersistingSyncInstall(t, map[string]*plugins.Manifest{"LM": presetManifest6930("com.t.m", "/tables")})
	var removes []string
	stubDBOnlyRemove7006(t, &removes)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("LM", "com.t.m", boolp6666(true))}) {
		t.Fatalf("setup: %+v", logging.Recent())
	}
	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{{ListingID: "LM"}})
	if len(removes) != 1 || removes[0] != "remove:com.t.m" {
		t.Fatalf("removes = %v, want LM pruned", removes)
	}
}
