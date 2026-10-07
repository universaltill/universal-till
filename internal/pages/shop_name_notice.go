package pages

import (
	"net/http"
	"net/url"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerShopNameNoticeUI wires GET /ui/shop-name-notice (ut-docs#3114):
// a till whose store.name is still a placeholder ("My Store", blank, the
// cloud's "Universal Till store" — config.IsPlaceholderStoreName) shows a
// manager a dismissible "Name your shop" banner on every page EXCEPT the
// sale screen, linking to the Shop name card on /settings. Modelled 1:1 on
// GET /ui/pairing-notice (pending_pairings.go): base.html mounts the
// placeholder and polls it, and every "nothing to show" case — no settings
// permission (a cashier never sees it), an unreadable or real name, or the
// sale screen itself — is 200 + empty body, never a 403 or 500 on a
// background poll. Once a real name is saved the next poll returns empty
// and htmx's innerHTML swap removes the banner. Dismissal is per browser
// session, client-side (shop_name_notice.html).
//
// The sale-screen exclusion (review finding, real e2e regression): unlike
// pairing/join notices — which are only ever present for the rare till
// mid-pairing — a legacy till's placeholder name is the common case, so
// this banner is persistently present on every poll. Mounted on "/" like
// every other page, its ~110px pushed the basket/tile-grid/totals-row
// layouts (phone-sell-3059, portrait-tablet-sale-3050, tablet-tier-
// totals-416, phone-width-layout-413, sale-screen-213) out of the exact
// viewport height they budget for, breaking "no page scroll" / "one-row"
// / "totals row visible" assertions across phone and tablet sizes. The
// offline-first rule ("never block the sale flow") means the sale screen
// gets none of this banner, not just a non-modal version of it — the
// nudge still reaches a manager on every other page they use.
func registerShopNameNoticeUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/shop-name-notice", func(w http.ResponseWriter, r *http.Request) {
		if onSaleScreen(r) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if !canPerform(d, r, "settings") {
			w.WriteHeader(http.StatusOK)
			return
		}
		v, ok, err := d.Settings.Get(r.Context(), common.KeyStoreName)
		if err != nil {
			logging.L().Errorf("shop-name notice: read store.name: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Unset (!ok) shows nothing: the shop name then comes from the
		// boot-time UT_STORE_NAME (kioskShopName, self_order_page.go), and
		// migration 001 seeds "My Store" anyway, so a legacy till is ok.
		if !ok || !config.IsPlaceholderStoreName(v) {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/shop_name_notice.html", map[string]any{})(w, r)
	})
}

// onSaleScreen reports whether this background poll was mounted from the
// sale screen ("/") — Referer, or htmx's HX-Current-URL when a referrer
// policy strips it (same two-header fallback as internal/auth's own
// refererPath; not reused directly, different package). Both are
// browser-supplied, which is enough here: worst case a manager doesn't see
// the nudge on one page for one poll, never the reverse.
func onSaleScreen(r *http.Request) bool {
	for _, h := range []string{"HX-Current-URL", "Referer"} {
		v := r.Header.Get(h)
		if v == "" {
			continue
		}
		if u, err := url.Parse(v); err == nil {
			return u.Path == "/"
		}
	}
	return false
}
