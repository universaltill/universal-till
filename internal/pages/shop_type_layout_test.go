package pages

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins/builtinlayouts"
	"github.com/universaltill/universal-till/internal/pos"
)

// salonHidesTables reports whether the salon layout is live in the
// replica's in-memory menu amendments (it hides /tables), i.e. whether the
// plugin reload actually happened — not just the DB row.
func salonHidesTables(d *common.Deps) bool {
	for _, a := range d.MenuAmendmentsSnapshot() {
		if a.PluginID == builtinlayouts.SalonPluginID && a.Key == "/tables" && a.Hide {
			return true
		}
	}
	return false
}

func salonInstalled(t *testing.T, d *common.Deps) bool {
	t.Helper()
	_, found, err := data.NewPluginRepo(d.Db).GetInstalledPluginVersion(t.Context(), builtinlayouts.SalonPluginID)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// TestShopTypeLayout_ReplicaFollowsPulledShopTypeWithoutRestart
// (ut-docs#2793): shop_type changed on the main till reaches an additional
// till through the admin pull, and its builtin layout follows without a
// restart — but never while a sale is open on that till.
func TestShopTypeLayout_ReplicaFollowsPulledShopTypeWithoutRestart(t *testing.T) {
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })
	var r shopTypeLayoutReconciler
	primary := newPullTestPrimary(t)
	ctx := t.Context()
	if err := primary.dp.Settings.Set(ctx, common.KeyShopType, "service"); err != nil {
		t.Fatal(err)
	}

	replica := newPullTestReplica(t, primary.server.URL)
	replica.Engine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	replica.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	client := &http.Client{Timeout: 5 * time.Second}

	syncPullTick(ctx, replica, client, func(ctx2 context.Context) {})
	if got, _, _ := replica.Settings.Get(ctx, common.KeyShopType); got != "service" {
		t.Fatalf("precondition: the pull must carry shop_type to the replica, got %q", got)
	}

	// A sale is open: the reconcile must wait, touching nothing.
	replica.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "sku-1", Name: "Coffee", Qty: 1, PriceCents: 250}, 1, nil)
	if deferred := r.tick(ctx, replica); !deferred {
		t.Fatal("reconcile must defer while the cashier basket has items")
	}
	if salonInstalled(t, replica) || salonHidesTables(replica) {
		t.Fatal("the layout must not change mid-sale")
	}

	// Kiosk basket counts too (ut-docs#449).
	replica.Engine.Reset()
	replica.KioskEngine.AddLineWithModifiers(pos.BasketLine{SKU: "sku-1", Name: "Coffee", Qty: 1, PriceCents: 250}, 1, nil)
	if deferred := r.tick(ctx, replica); !deferred {
		t.Fatal("reconcile must defer while the kiosk basket has items")
	}

	// Sale over: the next tick installs and reloads.
	replica.KioskEngine.Reset()
	if deferred := r.tick(ctx, replica); deferred {
		t.Fatal("reconcile must run once no basket is open")
	}
	if !salonInstalled(t, replica) {
		t.Fatal("salon layout must be installed after the pulled shop_type=service")
	}
	if !salonHidesTables(replica) {
		t.Fatal("salon layout must be live in the menu without a restart (plugins reloaded)")
	}

	// And back: the main till moves to retail, the replica drops the layout.
	if err := primary.dp.Settings.Set(ctx, common.KeyShopType, "retail"); err != nil {
		t.Fatal(err)
	}
	syncPullTick(ctx, replica, client, func(ctx2 context.Context) {})
	r.tick(ctx, replica)
	if salonInstalled(t, replica) || salonHidesTables(replica) {
		t.Fatal("salon layout must be removed after the pulled shop_type=retail")
	}
}
