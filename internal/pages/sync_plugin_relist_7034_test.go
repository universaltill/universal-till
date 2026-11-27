package pages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7034: when the main re-lists plugin P under a new listing L2, the
// till's stale L1 record is re-keyed to L2 (rollback protection under the new
// listing, one record per plugin).

func relistSetup7034(t *testing.T, name string) (*common.Deps, *[]installCall6930, *[]string) {
	t.Helper()
	dp := newDeps6666(t, name)
	calls := stubPersistingSyncInstall(t, map[string]*plugins.Manifest{
		"L1": presetManifest6930("com.t.p", "/tables"),
		"L2": presetManifest6930("com.t.p", "/tables"),
	})
	var removes []string
	stubDBOnlyRemove7006(t, &removes)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L1", "com.t.p", boolp6666(true))}) {
		t.Fatalf("setup: %+v", logging.Recent())
	}
	if err := data.NewPluginRepo(dp.Db).UpsertPluginSetting(t.Context(), "com.t.p", "greeting", `"hello"`); err != nil {
		t.Fatal(err)
	}
	return dp, calls, &removes
}

func assertOnlyL2Record7034(t *testing.T, dp *common.Deps, version string) {
	t.Helper()
	store := plugins.NewInstallStatusStore(dp.Db)
	if _, ok, err := store.Get(t.Context(), "L1"); err != nil || ok {
		t.Fatalf("L1 record still present (ok=%v err=%v), want it re-keyed away", ok, err)
	}
	rec, ok, err := store.Get(t.Context(), "L2")
	if err != nil || !ok || rec.State != plugins.InstallStateActive || rec.PluginID != "com.t.p" || rec.CurrentVersion != version {
		t.Fatalf("L2 record = %+v,%v,%v, want Active com.t.p %s", rec, ok, err, version)
	}
	all, err := store.List(t.Context())
	if err != nil || len(all) != 1 {
		t.Fatalf("records = %+v (%v), want exactly L2", all, err)
	}
	for id, r := range all {
		if r.PluginID == "com.t.p" && id != "L2" {
			t.Fatalf("plugin mapped to %s, want only L2", id)
		}
	}
}

func TestConvergePluginSet_7034_RelistSameVersionRekeysRecord(t *testing.T) {
	dp, calls, removes := relistSetup7034(t, "7034-same.db")
	installsBefore := len(*calls)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L2", "com.t.p", boolp6666(true))}) {
		t.Fatalf("relist tick did not converge: %+v", logging.Recent())
	}
	if len(*removes) != 0 || len(*calls) != installsBefore {
		t.Fatalf("removes=%v installs=%d (before %d), want none", *removes, len(*calls), installsBefore)
	}
	assertOnlyL2Record7034(t, dp, "1.0.0")
	if v, ok, err := data.NewPluginRepo(dp.Db).GetPluginSetting(t.Context(), "com.t.p", "greeting"); err != nil || !ok || v != `"hello"` {
		t.Fatalf("setting = %q,%v,%v, want it kept", v, ok, err)
	}
	assertNoOpenSyncProblems6930(t, "L1", "L2")
}

func TestConvergePluginSet_7034_RelistNewVersionRekeysRecord(t *testing.T) {
	dp, _, removes := relistSetup7034(t, "7034-new.db")
	l2 := row6930("L2", "com.t.p", boolp6666(true))
	l2.Version = "1.1.0"
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{l2}) {
		t.Fatalf("upgrade tick did not converge: %+v", logging.Recent())
	}
	if len(*removes) != 0 {
		t.Fatalf("removes = %v, want none", *removes)
	}
	assertOnlyL2Record7034(t, dp, "1.1.0")
	assertNoOpenSyncProblems6930(t, "L1", "L2")
}

func TestConvergePluginSet_7034_RelistInstallFailureKeepsRecordsActive(t *testing.T) {
	dp, _, _ := relistSetup7034(t, "7034-fail.db")
	// Like the real path on a failed install of a listing with no prior
	// record: an error, nothing written.
	orig := pluginSyncInstall
	pluginSyncInstall = func(ctx context.Context, d *common.Deps, listing, version string, inactive bool, adopt []string) (string, error) {
		return "", errors.New("marketplace down")
	}
	t.Cleanup(func() { pluginSyncInstall = orig })

	l2 := row6930("L2", "com.t.p", boolp6666(true))
	l2.Version = "1.1.0"
	if convergePluginSet(t.Context(), dp, []data.PluginSyncRow{l2}) {
		t.Fatal("tick converged despite the failed install")
	}
	store := plugins.NewInstallStatusStore(dp.Db)
	rec, ok, err := store.Get(t.Context(), "L2")
	if err != nil || !ok || rec.State != plugins.InstallStateActive || rec.PluginID != "com.t.p" || rec.CurrentVersion != "1.0.0" {
		t.Fatalf("L2 record = %+v,%v,%v, want pre-adopted Active com.t.p 1.0.0", rec, ok, err)
	}
	if _, ok, _ := store.Get(t.Context(), "L1"); !ok {
		t.Fatal("L1 record gone although P has not converged under L2")
	}
	for _, p := range collectProblems(t.Context(), dp) {
		if msg, _ := p["msg"].(string); strings.HasPrefix(msg, "plugin install failed") {
			t.Fatalf("heartbeat reports %q", msg)
		}
	}
	if !logging.HasOpenProblem(pluginSyncInstallKeyPrefix + "L2") {
		t.Fatalf("keyed problem for L2 not open: %+v", logging.Recent())
	}
	if !active6666(t, dp, "com.t.p") {
		t.Fatal("P not active")
	}
	if v, ok, err := data.NewPluginRepo(dp.Db).GetPluginSetting(t.Context(), "com.t.p", "greeting"); err != nil || !ok || v != `"hello"` {
		t.Fatalf("setting = %q,%v,%v, want it kept", v, ok, err)
	}
	// A later tick with a working installer converges to only L2.
	pluginSyncInstall = orig
	stubPersistingSyncInstall(t, map[string]*plugins.Manifest{"L2": presetManifest6930("com.t.p", "/tables")})
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{l2}) {
		t.Fatalf("recovery tick did not converge: %+v", logging.Recent())
	}
	assertOnlyL2Record7034(t, dp, "1.1.0")
	assertNoOpenSyncProblems6930(t, "L1", "L2")
}

func TestConvergePluginSet_7034_RekeyedThenDroppedIsPrunedOnce(t *testing.T) {
	dp, _, removes := relistSetup7034(t, "7034-drop.db")
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L2", "com.t.p", boolp6666(true))}) {
		t.Fatalf("relist tick: %+v", logging.Recent())
	}
	if !convergePluginSet(t.Context(), dp, nil) {
		t.Fatalf("drop tick did not converge: %+v", logging.Recent())
	}
	if len(*removes) != 1 || (*removes)[0] != "remove:com.t.p" {
		t.Fatalf("removes = %v, want exactly one of com.t.p", *removes)
	}
	if all, err := plugins.NewInstallStatusStore(dp.Db).List(t.Context()); err != nil || len(all) != 0 {
		t.Fatalf("records = %+v (%v), want none", all, err)
	}
}

// Real installer: a relist whose pinned install is answered with another
// version must roll back under the new listing, not uninstall P.
func TestSyncPullTick_7034_RelistVersionMismatchRollsBackKeepingSettings(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"L1": "com.test.p", "L2": "com.test.p"})
	primary := newSyncPluginsPrimary(t, mkt)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)

	var ok bool
	withPaths(replica.dataDir, func() {
		ok = convergePluginSet(t.Context(), replica.dp, []data.PluginSyncRow{{ListingID: "L1", PluginID: "com.test.p", PluginName: "p", Version: "1.0.0"}})
	})
	if !ok {
		t.Fatalf("setup: %+v", logging.Recent())
	}
	mkt.publishVersion(t, "L2", "com.test.p", "1.1.0")
	mkt.publishMismatchedRelease(t, "L2", "com.test.p", "1.1.0", "9.9.9")
	withPaths(replica.dataDir, func() {
		ok = convergePluginSet(t.Context(), replica.dp, []data.PluginSyncRow{{ListingID: "L2", PluginID: "com.test.p", PluginName: "p", Version: "1.1.0"}})
	})
	if ok {
		t.Fatal("tick reported converged despite the mismatch")
	}
	if v, found := pluginInstalledVersion(t, replica.dp, "com.test.p"); !found || v != "1.0.0" {
		t.Fatalf("installed = (%q,%v), want 1.0.0 kept", v, found)
	}
	if !replica.hasPluginFiles("com.test.p") {
		t.Fatal("plugin files were removed")
	}
	if _, err := os.Stat(filepath.Join(replica.dataDir, "plugins", "com.test.p", "versions", "1.1.0")); !os.IsNotExist(err) {
		t.Fatalf("versions/1.1.0 exists after the mismatch (err=%v)", err)
	}
	if r1, found, err := plugins.NewInstallStatusStore(replica.dp.Db).Get(t.Context(), "L1"); err != nil || !found || r1.State != plugins.InstallStateActive {
		t.Fatalf("L1 record = %+v,%v,%v, want kept Active", r1, found, err)
	}
	// Settings survival is asserted in the stub-based tests: the fake
	// marketplace manifest declares no settings, so a real in-place upgrade
	// attempt reconciles an undeclared key away (pre-existing, same listing too).
	rec, found, err := plugins.NewInstallStatusStore(replica.dp.Db).Get(t.Context(), "L2")
	if err != nil || !found || rec.State != plugins.InstallStateActive || rec.PluginID != "com.test.p" ||
		rec.CurrentVersion != "1.0.0" || rec.MessageKey != "plugins.install.error.version_mismatch" {
		t.Fatalf("L2 record = %+v,%v,%v", rec, found, err)
	}
}

func TestSyncPullTick_7034_RelistPlainFailureNoHeartbeatInstallFailed(t *testing.T) {
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"L1": "com.test.p", "L2": "com.test.p"})
	primary := newSyncPluginsPrimary(t, mkt)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)

	var ok bool
	withPaths(replica.dataDir, func() {
		ok = convergePluginSet(t.Context(), replica.dp, []data.PluginSyncRow{{ListingID: "L1", PluginID: "com.test.p", PluginName: "p", Version: "1.0.0"}})
	})
	if !ok {
		t.Fatalf("setup: %+v", logging.Recent())
	}
	mkt.unpublish("L2")
	withPaths(replica.dataDir, func() {
		ok = convergePluginSet(t.Context(), replica.dp, []data.PluginSyncRow{{ListingID: "L2", PluginID: "com.test.p", PluginName: "p", Version: "1.1.0"}})
	})
	if ok {
		t.Fatal("tick converged although L2 is not served")
	}
	if v, found := pluginInstalledVersion(t, replica.dp, "com.test.p"); !found || v != "1.0.0" {
		t.Fatalf("installed = (%q,%v), want 1.0.0 kept", v, found)
	}
	if state, found := installStatusState(t, replica.dp, "L2"); !found || state != string(plugins.InstallStateActive) {
		t.Fatalf("L2 state = %q,%v, want active", state, found)
	}
	for _, p := range collectProblems(t.Context(), replica.dp) {
		if msg, _ := p["msg"].(string); strings.Contains(msg, "plugin install failed") {
			t.Fatalf("heartbeat reports %q", msg)
		}
	}
}

func failSaveFor7034(t *testing.T, dp *common.Deps, listing string) {
	t.Helper()
	if _, err := dp.Db.Exec(`CREATE TRIGGER fail_save_7034 BEFORE INSERT ON plugin_install_status
		WHEN NEW.listing_id = '` + listing + `' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
}

// M1: an installer that leaves another plugin's Active record under L2 must
// not make the sync drop P's L1 record.
func TestConvergePluginSet_7034_InstallLeavingOtherPluginUnderL2KeepsL1(t *testing.T) {
	dp, _, _ := relistSetup7034(t, "7034-m1.db")
	orig := pluginSyncInstall
	pluginSyncInstall = func(ctx context.Context, d *common.Deps, listing, version string, inactive bool, adopt []string) (string, error) {
		return "installed", plugins.NewInstallStatusStore(d.Db).Save(ctx, plugins.InstallStatusRecord{
			ListingID: listing, PluginID: "com.t.q", PluginName: "q", CurrentVersion: version, State: plugins.InstallStateActive,
		})
	}
	t.Cleanup(func() { pluginSyncInstall = orig })
	l2 := row6930("L2", "com.t.p", boolp6666(true))
	l2.Version = "1.1.0"
	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{l2})
	if _, ok, _ := plugins.NewInstallStatusStore(dp.Db).Get(t.Context(), "L1"); !ok {
		t.Fatal("L1 record dropped although L2 is not P's")
	}
}

// M2: an Active record of another plugin under L2 is never overwritten.
func TestConvergePluginSet_7034_OtherPluginsL2RecordNotOverwritten(t *testing.T) {
	dp, calls, _ := relistSetup7034(t, "7034-m2.db")
	store := plugins.NewInstallStatusStore(dp.Db)
	if err := store.Save(t.Context(), plugins.InstallStatusRecord{ListingID: "L2", PluginID: "com.t.q", PluginName: "q", CurrentVersion: "3.0.0", State: plugins.InstallStateActive}); err != nil {
		t.Fatal(err)
	}
	_ = calls
	orig := pluginSyncInstall
	pluginSyncInstall = func(ctx context.Context, d *common.Deps, listing, version string, inactive bool, adopt []string) (string, error) {
		return "", errors.New("down")
	}
	t.Cleanup(func() { pluginSyncInstall = orig })
	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L2", "com.t.p", boolp6666(true))})
	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{func() data.PluginSyncRow {
		r := row6930("L2", "com.t.p", boolp6666(true))
		r.Version = "1.1.0"
		return r
	}()})
	rec, ok, _ := store.Get(t.Context(), "L2")
	if !ok || rec.PluginID != "com.t.q" || rec.CurrentVersion != "3.0.0" {
		t.Fatalf("L2 = %+v, want Q untouched", rec)
	}
	if _, ok, _ := store.Get(t.Context(), "L1"); !ok {
		t.Fatal("L1 dropped")
	}
}

// Rekey Save failure keeps L1.
func TestConvergePluginSet_7034_RekeySaveFailureKeepsL1(t *testing.T) {
	dp, _, _ := relistSetup7034(t, "7034-rk.db")
	failSaveFor7034(t, dp, "L2")
	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6930("L2", "com.t.p", boolp6666(true))})
	store := plugins.NewInstallStatusStore(dp.Db)
	if _, ok, _ := store.Get(t.Context(), "L1"); !ok {
		t.Fatal("L1 dropped although the re-key save failed")
	}
	if _, ok, _ := store.Get(t.Context(), "L2"); ok {
		t.Fatal("L2 exists despite the failed save")
	}
}

// L4: a failed pre-adopt save skips the install this tick.
func TestConvergePluginSet_7034_PreAdoptFailureSkipsInstall(t *testing.T) {
	dp, calls, _ := relistSetup7034(t, "7034-l4.db")
	failSaveFor7034(t, dp, "L2")
	before := len(*calls)
	l2 := row6930("L2", "com.t.p", boolp6666(true))
	l2.Version = "1.1.0"
	if convergePluginSet(t.Context(), dp, []data.PluginSyncRow{l2}) {
		t.Fatal("converged")
	}
	if len(*calls) != before {
		t.Fatalf("install attempted despite the failed pre-adopt")
	}
	if _, ok, _ := plugins.NewInstallStatusStore(dp.Db).Get(t.Context(), "L1"); !ok {
		t.Fatal("L1 dropped")
	}
}

// L2: a cloud-hosted refusal removes this tick's pre-adopted copy.
func TestConvergePluginSet_7034_CloudHostedRemovesPreAdopted(t *testing.T) {
	dp, _, _ := relistSetup7034(t, "7034-l2.db")
	orig := pluginSyncInstall
	pluginSyncInstall = func(ctx context.Context, d *common.Deps, listing, version string, inactive bool, adopt []string) (string, error) {
		return "", plugins.ErrCloudHostedPlugin
	}
	t.Cleanup(func() { pluginSyncInstall = orig })
	l2 := row6930("L2", "com.t.p", boolp6666(true))
	l2.Version = "1.1.0"
	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{l2})
	store := plugins.NewInstallStatusStore(dp.Db)
	if _, ok, _ := store.Get(t.Context(), "L2"); ok {
		t.Fatal("pre-adopted L2 copy left behind")
	}
	if _, ok, _ := store.Get(t.Context(), "L1"); !ok {
		t.Fatal("L1 dropped")
	}
}
