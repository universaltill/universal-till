package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerShopNameNoticeUI wires GET /ui/shop-name-notice (ut-docs#3114):
// a till whose store.name is still a placeholder ("My Store", blank, the
// cloud's "Universal Till store" — config.IsPlaceholderStoreName) shows a
// manager a dismissible "Name your shop" banner on every page, linking to
// the Shop name card on /settings. Modelled 1:1 on GET /ui/pairing-notice
// (pending_pairings.go): base.html mounts the placeholder and polls it, and
// every "nothing to show" case — no settings permission (a cashier never
// sees it), an unreadable or real name — is 200 + empty body, never a 403
// or 500 on a background poll. Once a real name is saved the next poll
// returns empty and htmx's innerHTML swap removes the banner. Dismissal is
// per browser session, client-side (shop_name_notice.html).
func registerShopNameNoticeUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/shop-name-notice", func(w http.ResponseWriter, r *http.Request) {
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
