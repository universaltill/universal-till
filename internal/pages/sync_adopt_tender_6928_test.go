package pages

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6928: the replica sync forwards the main's owned-tender list to the
// install, and the install adopts the ownerless synced row.

func strsp6928(s ...string) *[]string { return &s }

func TestConvergePluginSet_PassesOwnedPaymentKeys(t *testing.T) {
	dp := newDeps6666(t, "6928-pass.db")
	orig := pluginSyncInstall
	t.Cleanup(func() { pluginSyncInstall = orig })
	got := map[string][]string{}
	pluginSyncInstall = func(_ context.Context, d *common.Deps, listing, _ string, _ bool, adopt []string) (string, error) {
		got[listing] = adopt
		seed6666(t, d, listing, "com.t."+listing, true, nil, false)
		return "", nil
	}
	rows := []data.PluginSyncRow{
		{ListingID: "LA", PluginID: "com.t.LA", PluginName: "a", Version: "1.0.0", OwnedPaymentMethodIDs: strsp6928("sumup")},
		{ListingID: "LB", PluginID: "com.t.LB", PluginName: "b", Version: "1.0.0", OwnedPaymentMethodIDs: strsp6928()},
		{ListingID: "LC", PluginID: "com.t.LC", PluginName: "c", Version: "1.0.0"},
	}
	if !convergePluginSet(t.Context(), dp, rows) {
		t.Fatal("not converged")
	}
	if !reflect.DeepEqual(got["LA"], []string{"sumup"}) {
		t.Fatalf("LA adopt = %v", got["LA"])
	}
	if len(got["LB"]) != 0 || len(got["LC"]) != 0 {
		t.Fatalf("empty/absent hints must pass nothing: LB=%v LC=%v", got["LB"], got["LC"])
	}
}

func sumupManifest6928() map[string]*plugins.Manifest {
	return map[string]*plugins.Manifest{"LS": {
		ID: "com.universaltill.payment-sumup", Name: "SumUp", Version: "1.0.0", Entrypoint: "./main.wasm",
		Entries: []plugins.ManifestEntry{{Type: "payment", Key: "sumup", Label: "SumUp"}},
	}}
}

func seedOwnerlessSumup6928(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES ('sumup', 'SumUp', 'card', 1, 100)`); err != nil {
		t.Fatal(err)
	}
}

func sumupState6928(t *testing.T, dp *common.Deps) (owner string, active int) {
	t.Helper()
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COALESCE(plugin_id,''), is_active FROM payment_methods WHERE id='sumup'`).Scan(&owner, &active); err != nil {
		t.Fatal(err)
	}
	return
}

func TestPluginSync_AdoptsThenFollowsDisable(t *testing.T) {
	dp := newDeps6666(t, "6928-e2e.db")
	seedOwnerlessSumup6928(t, dp)
	stubPersistingSyncInstall(t, sumupManifest6928())
	const pid = "com.universaltill.payment-sumup"

	row := data.PluginSyncRow{ListingID: "LS", PluginID: pid, PluginName: "SumUp", Version: "1.0.0",
		Enabled: boolp6666(true), OwnedPaymentMethodIDs: strsp6928("sumup")}
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row}) {
		t.Fatalf("not converged: %+v", logging.Recent())
	}
	if owner, active := sumupState6928(t, dp); owner != pid || active != 1 {
		t.Fatalf("after adopt: owner=%q active=%d, want %q/1", owner, active, pid)
	}
	if logging.HasOpenProblem(pluginSyncInstallKeyPrefix + "LS") {
		t.Fatalf("open install problem: %+v", logging.Recent())
	}
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "collides with an existing built-in") {
			t.Fatalf("collision logged: %s", p.Msg)
		}
	}

	// The main disables it; the replica follows. No admin-bundle apply runs,
	// so the ownership-aware deactivate step alone must retire the tender.
	row.Enabled = boolp6666(false)
	if !convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row}) {
		t.Fatalf("disable tick not converged: %+v", logging.Recent())
	}
	if err := data.NewPluginRepo(dp.Db).SyncPluginPaymentMethods(t.Context()); err != nil {
		t.Fatal(err)
	}
	if owner, active := sumupState6928(t, dp); owner != pid || active != 0 {
		t.Fatalf("after disable: owner=%q active=%d, want %q/0", owner, active, pid)
	}
}

func TestPluginSync_NoHintStillRefuses(t *testing.T) {
	dp := newDeps6666(t, "6928-nohint.db")
	seedOwnerlessSumup6928(t, dp)
	stubPersistingSyncInstall(t, sumupManifest6928())
	for name, hint := range map[string]*[]string{"absent": nil, "empty": strsp6928()} {
		row := data.PluginSyncRow{ListingID: "LS", PluginID: "com.universaltill.payment-sumup", PluginName: "SumUp",
			Version: "1.0.0", OwnedPaymentMethodIDs: hint}
		if convergePluginSet(t.Context(), dp, []data.PluginSyncRow{row}) {
			t.Fatalf("%s hint: converged, want refusal", name)
		}
		if owner, _ := sumupState6928(t, dp); owner != "" {
			t.Fatalf("%s hint: owner = %q, want NULL", name, owner)
		}
	}
}
