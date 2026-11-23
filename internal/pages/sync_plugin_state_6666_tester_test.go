package pages

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6666 AC-1 (tester): following the main's switch must reach the
// running plugin manager in the same tick, not only the plugins row — a
// replica that flips is_active but never reloads keeps dispatching the
// disabled plugin until the next restart.
func TestConvergePluginSet_FollowedStateReloadsPluginManager(t *testing.T) {
	dp := newDeps6666(t, "6666-tester-reload.db")
	seed6666(t, dp, "L1", "com.test.tax-de", true, nil, false)
	stubPluginSyncInstall(t, nil)
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dp.Pm.InstalledIDs(), "com.test.tax-de") {
		t.Fatalf("precondition: manager does not hold the plugin: %v", dp.Pm.InstalledIDs())
	}

	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6666("L1", "com.test.tax-de", boolp6666(false))}) {
		t.Fatal("not converged")
	}
	if slices.Contains(dp.Pm.InstalledIDs(), "com.test.tax-de") {
		t.Fatal("the main disabled the plugin; the replica's plugin manager still loads it (no reload after the state follow)")
	}

	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row6666("L1", "com.test.tax-de", boolp6666(true))}) {
		t.Fatal("re-enable: not converged")
	}
	if !slices.Contains(dp.Pm.InstalledIDs(), "com.test.tax-de") {
		t.Fatal("the main re-enabled the plugin; the replica's plugin manager did not load it")
	}
}

// ut-docs#6666 AC-1/2/3 (tester), driven end to end on the two-till harness:
// a real primary (real mux, real /api/sync/plugins, real enable/disable
// handlers, real marketplace installs from a signed fake marketplace) and a
// real replica pulling over HTTP with syncPullTick. A second listing whose
// replica install keeps failing (version mismatch) must not stop the main's
// disable/re-enable of the first from arriving.
func TestSyncPullTick_6666_MainSwitchFollowsBehindFailingInstall(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{
		"listing-alpha": "com.test.sync-alpha",
		"listing-beta":  "com.test.sync-beta",
	})
	primary := newSyncPluginsPrimary(t, mkt)
	replica := newSyncPluginsReplica(t, mkt, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}
	const alpha = "com.test.sync-alpha"

	primary.install(t, "listing-alpha")
	replica.tick(t, client)
	if !active6666(t, replica.dp, alpha) {
		t.Fatal("precondition: alpha not installed+active on the replica")
	}

	// The main gains beta; the marketplace answers the replica's pinned
	// request with the wrong release, so beta's install fails every tick.
	primary.install(t, "listing-beta")
	mkt.publishMismatchedRelease(t, "listing-beta", "com.test.sync-beta", "1.0.0", "9.9.9")

	toggle := func(on bool) {
		t.Helper()
		h, verb := handleDisablePlugin(primary.dp), "disable"
		if on {
			h, verb = handleEnablePlugin(primary.dp), "enable"
		}
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+alpha+"/"+verb, nil)
		req.SetPathValue("id", alpha)
		rec := httptest.NewRecorder()
		withPaths(primary.dataDir, func() { h(rec, req) })
		if rec.Code != http.StatusOK {
			t.Fatalf("primary %s: %d %s", verb, rec.Code, rec.Body.String())
		}
	}
	toggle(false)

	for tick := 1; tick <= 2; tick++ {
		replica.tick(t, client)
		if active6666(t, replica.dp, alpha) {
			t.Fatalf("tick %d: the main disabled alpha; the replica kept it on behind beta's failing install", tick)
		}
		if _, ok := replica.dp.Pm.Installed[alpha]; ok {
			t.Fatalf("tick %d: alpha still loaded in the replica's plugin manager", tick)
		}
		if st, ok := installStatusState(t, replica.dp, "listing-alpha"); !ok || st != string(plugins.InstallStateActive) {
			t.Fatalf("tick %d: alpha install state %q,%v; disabled must stay installed", tick, st, ok)
		}
		if !replica.hasPluginFiles(alpha) {
			t.Fatalf("tick %d: alpha's files were removed", tick)
		}
		if st, _ := installStatusState(t, replica.dp, "listing-beta"); st != string(plugins.InstallStateFailed) {
			t.Fatalf("tick %d: beta state %q; the harness no longer makes beta fail", tick, st)
		}
	}

	toggle(true)
	replica.tick(t, client)
	if !active6666(t, replica.dp, alpha) {
		t.Fatal("the main re-enabled alpha; the replica did not follow (stale rows?)")
	}
	if _, ok := replica.dp.Pm.Installed[alpha]; !ok {
		t.Fatal("re-enabled alpha not loaded in the replica's plugin manager")
	}
}
