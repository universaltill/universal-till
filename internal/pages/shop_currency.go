package pages

import (
	"context"
	"net/http"
	"strconv"
	"sync"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0163 amendment 2026-10-16 (ut-docs#4539): a store born on my. has a
// currency the cloud set; the sync response's entitlement block carries it
// and cloudsync caches it (entitlement.KeyShopCurrency). After each sync the
// main till reconciles its own currency with it:
//
//   - no shop currency, or the same one: nothing to do (any open problem
//     clears);
//   - a till that follows a main till: nothing — the main till owns the
//     shop-wide store.currency and replicas follow it through the
//     primary-wins settings pull (ADR-0011 §5; ADR-0114 is only the
//     transport that kicks that pull);
//   - a code this build doesn't know: never adopted, reported;
//   - this shop value was already adopted once (entitlement.
//     KeyShopCurrencyAdopted): a manager has since chosen otherwise, which
//     is never overridden — reported;
//   - no completed or voided sale yet: adopt it, audited as "system" — but
//     never with a sale in progress (a basket with lines anywhere, or a
//     parked sale): that tick is skipped and the next one retries;
//   - otherwise: never change by itself (it would relabel every recorded
//     amount), report an open problem and show managers a status chip.
//
// Never a sale gate: selling carries on whatever this decides.

// problemKeyShopCurrency keys the open problem my. lists on this till
// (logging.WarnProblemf; ResolveProblems clears it).
const problemKeyShopCurrency = "cloud.shop_currency_mismatch"

// shopCurrencyWarned remembers which till/shop pair the open problem was
// logged for, so a repeat tick doesn't re-warn (the Problems ring holds 50
// lines) while a different pair still replaces the stale message.
var (
	shopCurrencyMu         sync.Mutex
	shopCurrencyWarnedPair string
	shopCurrencyDecimalsAt string
)

// reconcileShopCurrency is the cloudsync.Hooks.ShopCurrency hook. Best
// effort: it logs, it never fails the tick.
func reconcileShopCurrency(ctx context.Context, d *common.Deps) {
	shopCurrencyMu.Lock()
	defer shopCurrencyMu.Unlock()
	if d == nil || d.Settings == nil {
		return
	}
	shop, ok := entitlement.ShopCurrency(ctx, d.Settings)
	if !ok {
		resolveShopCurrencyProblem()
		return
	}
	if tillFollowsMain(ctx, d) {
		return
	}
	till := d.CurrentState().Currency
	if till == shop {
		noteShopCurrencyDecimals(ctx, d, shop)
		resolveShopCurrencyProblem()
		return
	}
	if !httpx.IsKnownCurrency(shop) {
		warnShopCurrencyProblem(till, shop,
			"[cloudsync] the shop's currency on my. is %s, which this till build doesn't know; it keeps selling in %s. Update the till, or change the shop's currency with support",
			shop, till)
		return
	}
	if adopted, _, err := d.Settings.Get(ctx, entitlement.KeyShopCurrencyAdopted); err == nil && entitlement.ShopCurrencyCode(adopted) == shop {
		// Adopted once already; the till's currency differs again, so a
		// manager chose it. Never flip it back.
		warnShopCurrencyProblem(till, shop,
			"[cloudsync] this till sells in %s but the shop's currency on my. is %s; change it in Settings or ask support",
			till, shop)
		return
	}
	hasSales, err := data.NewPOSRepo(d.Db).HasCompletedSales(ctx)
	if err != nil {
		logging.L().Warnf("[cloudsync] shop currency: cannot tell whether this till has sales, trying again next sync: %v", err)
		return
	}
	if hasSales {
		warnShopCurrencyProblem(till, shop,
			"[cloudsync] this till sells in %s but the shop's currency on my. is %s; change it in Settings or ask support",
			till, shop)
		return
	}
	if busy, err := shopCurrencySaleInProgress(ctx, d); err != nil || busy {
		if err != nil {
			logging.L().Warnf("[cloudsync] shop currency: cannot tell whether a sale is parked, trying again next sync: %v", err)
		} else {
			logging.L().Infof("[cloudsync] shop currency: a sale is in progress, adopting %s at a later sync", shop)
		}
		return
	}
	// ut-docs#157: the persist happens under the state write lock, and the
	// runtime changes only once it succeeded — a concurrent Settings save
	// can't interleave, and a failed write never becomes the runtime state.
	var persistErr error
	adopted := false
	st := d.UpdateState(func(s *common.RuntimeState) {
		if s.Currency != till {
			return // changed since read: decide again next sync
		}
		// settings-write:allow main-till only: a till that follows a main till returned above (ADR-0163 amendment)
		persistErr = d.Settings.SetMany(ctx, map[string]string{
			common.KeyCurrency:                 shop,
			common.KeyCurrencyConfirmed:        "true",
			entitlement.KeyShopCurrencyAdopted: shop,
		})
		if persistErr == nil {
			s.Currency = shop
			adopted = true
		}
	})
	if persistErr != nil {
		logging.L().Warnf("[cloudsync] shop currency: adopting %s failed, trying again next sync: %v", shop, persistErr)
		return
	}
	if !adopted {
		return
	}
	httpx.InitCurrency(st.Currency)
	auditCloudDirective(ctx, d, "settings", common.KeyCurrency, "currency_adopted_from_cloud",
		map[string]any{"from": till, "to": shop})
	logging.L().Infof("[cloudsync] adopted the shop's currency %s from my. (was %s; no sales yet)", shop, till)
	noteShopCurrencyDecimals(ctx, d, shop)
	resolveShopCurrencyProblem()
}

// shopCurrencySaleInProgress reports whether a sale is under way anywhere on
// this till: a cashier or kiosk basket with lines (by-hand entries
// included), a self-order session with items, or a parked (held) sale.
// Adopting then would reprice it mid-sale.
func shopCurrencySaleInProgress(ctx context.Context, d *common.Deps) (bool, error) {
	if d.Engine != nil && d.Engine.HasItemsOrByHand() {
		return true, nil
	}
	if d.KioskEngine != nil && d.KioskEngine.HasItemsOrByHand() {
		return true, nil
	}
	if d.SelfOrderSessions != nil && d.SelfOrderSessions.HasItems() {
		return true, nil
	}
	held, err := data.NewHeldSalesRepo(d.Db).List(ctx)
	if err != nil {
		return false, err
	}
	return len(held) > 0, nil
}

func warnShopCurrencyProblem(till, shop, format string, args ...any) {
	pair := till + ">" + shop
	if shopCurrencyWarnedPair == pair && logging.HasOpenProblem(problemKeyShopCurrency) {
		return
	}
	logging.ResolveProblems(problemKeyShopCurrency)
	logging.L().WarnProblemf(problemKeyShopCurrency, format, args...)
	shopCurrencyWarnedPair = pair
}

func resolveShopCurrencyProblem() {
	shopCurrencyWarnedPair = ""
	if logging.ResolveProblems(problemKeyShopCurrency) > 0 {
		logging.L().Infof("[cloudsync] the till's currency matches the shop's again")
	}
}

// noteShopCurrencyDecimals logs, once per code, when the cloud's decimals
// for the shop currency differ from this build's table. The till's table
// decides; this is only a breadcrumb.
func noteShopCurrencyDecimals(ctx context.Context, d *common.Deps, code string) {
	raw, ok, err := d.Settings.Get(ctx, entitlement.KeyShopCurrencyDecimals)
	if err != nil || !ok || raw == "" {
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil || !httpx.IsKnownCurrency(code) {
		return
	}
	local := httpx.CurrencyByCode(code).Decimals
	if n == local || shopCurrencyDecimalsAt == code+raw {
		return
	}
	shopCurrencyDecimalsAt = code + raw
	logging.L().Infof("[cloudsync] my. says %s has %d decimals, this till uses %d; keeping the till's", code, n, local)
}

// shopCurrencyChip is the data of web/ui/partials/shop_currency_chip.html.
type shopCurrencyChip struct {
	Till string
	Shop string
}

// registerShopCurrencyChip: GET /ui/shop-currency-chip, polled from the
// status bar on every page (base.html), same shape as
// /ui/disk-space-chip. Empty 200 unless the viewer may open Settings, the
// till does not follow a main till, and the cached shop currency differs
// from this till's; then a status chip —
// never a modal, selling carries on — linking to Settings → Currency.
func registerShopCurrencyChip(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/shop-currency-chip", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") || d.Settings == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		// A till that follows a main till never reconciles (the main till
		// owns store.currency), so it shows nothing either.
		if tillFollowsMain(r.Context(), d) {
			w.WriteHeader(http.StatusOK)
			return
		}
		shop, ok := entitlement.ShopCurrency(r.Context(), d.Settings)
		till := d.CurrentState().Currency
		if !ok || shop == till {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/shop_currency_chip.html", shopCurrencyChip{Till: till, Shop: shop})(w, r)
	})
}
