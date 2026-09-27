package pages

import (
	"context"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/builtinlayouts"
)

// shopTypeLayoutInterval is how often StartShopTypeLayoutReconcile checks
// that the builtin layout matches shop.type — the same 30 s cadence as the
// replica's admin pull, so a pulled shop_type lands within one more tick.
const shopTypeLayoutInterval = 30 * time.Second

// StartShopTypeLayoutReconcile keeps the builtin layout plugin in step with
// shop.type while the till runs (ut-docs#2793). Boot and the two local
// shop_type handlers already call builtinlayouts.Sync, but a shop_type that
// arrives any other way — an additional till's admin pull from the main
// till, or a cloud set_setting directive — changed only the settings row,
// so the layout stayed on the old value until a restart. Same Start* shape
// as StartSelfOrderSessionSweep: wg.Add before the goroutine, wg.Done on
// every exit path.
func StartShopTypeLayoutReconcile(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		var r shopTypeLayoutReconciler
		ticker := time.NewTicker(shopTypeLayoutInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.tick(ctx, d)
			}
		}
	}()
}

// shopTypeLayoutReconciler carries one loop's memory between ticks: the
// last failure, so a Sync error that repeats every tick (a conflicting
// marketplace layout plugin, a read-only plugins dir) reloads the plugins
// and nudges linked tills once — not every 30 s forever.
type shopTypeLayoutReconciler struct {
	lastFail string // "<shop_type>\x00<error>" of the previous failed tick
}

// tick is one pass of StartShopTypeLayoutReconcile. It never runs mid-sale:
// while any basket is open (cashier, kiosk or a table-QR guest —
// autoUpdateBusy) it does nothing and reports deferred, so the next tick
// retries; a plugin reload must not swap the menu or the plugin runtime
// under a sale or a payment in flight. Otherwise it syncs and reloads
// unless the sync was a genuine no-op — the same "reload on error too" rule
// as boot and the handlers (ut-docs#2006), applied once per distinct
// failure. Best-effort: a failure is logged, never surfaced to the till.
func (r *shopTypeLayoutReconciler) tick(ctx context.Context, d *common.Deps) (deferred bool) {
	if d.Engine != nil && autoUpdateBusy(d) {
		return true
	}
	shopType, _, err := d.Settings.Get(ctx, common.KeyShopType)
	if err != nil {
		logging.L().Warnf("shop_type layout: could not read shop_type: %v", err)
		return false
	}
	changed, syncErr := builtinlayouts.Sync(ctx, d.Db, shopType)
	reload := changed
	if syncErr != nil {
		fail := shopType + "\x00" + syncErr.Error()
		if fail != r.lastFail {
			logging.L().Warnf("shop_type layout: could not sync builtin layout for shop_type %q: %v", shopType, syncErr)
			reload = true
		}
		r.lastFail = fail
	} else {
		r.lastFail = ""
	}
	if reload {
		if err := d.ReloadPlugins(ctx); err != nil {
			logging.L().Warnf("shop_type layout: could not reload plugins after layout sync: %v", err)
		}
		if changed {
			logging.L().Infof("shop_type layout: builtin layout now matches shop_type %q", shopType)
		}
	}
	return false
}
