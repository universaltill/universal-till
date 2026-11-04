package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerChannelOrdersPage serves "Online orders" (ADR-0169 §9,
// ut-docs#4768): orders taken at a web shop or marketplace and recorded on
// this (main) till by a channel_order directive. The cloud owns the order
// status: the page shows what was recorded here, and staff on the main till
// queue accept/pick/pack/ship/cancel actions (channel_order_actions.go) that
// the sync tick uploads. A queued action is a chip, never a status change.
//
// These are not till sales: they are in no drawer, Z report or fiscal journal.
// Gated on the "reports" action like Reports (a cashier is sale-only,
// ut-docs#3079): hidden from the launcher AND refused here with 403.
func registerChannelOrdersPage(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewChannelOrdersRepo(d.Db)

	mux.HandleFunc("GET /channel-orders", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "reports") {
			return
		}
		orders, err := repo.List(r.Context(), 200)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "channel_orders.error.load_failed", err)
			return
		}
		httpx.Render("ui/pages/channel_orders.html", map[string]any{
			"title":     httpx.T(httpx.RequestLocale(r), "page.title.channel_orders"),
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Orders":    orders,
		})(w, r)
	})

	mux.HandleFunc("GET /channel-orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "reports") {
			return
		}
		o, ok, err := repo.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "channel_orders.error.load_failed", err)
			return
		}
		if !ok {
			httpx.RenderError(w, r, http.StatusNotFound, "channel_orders.error.not_found", nil)
			return
		}
		view, err := channelOrderDetailData(r.Context(), d, r, o, "", "", "", channelFormValues{})
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "channel_orders.error.load_failed", err)
			return
		}
		httpx.Render("ui/pages/channel_order_detail.html", view)(w, r)
	})

	registerChannelOrderActions(mux, d, repo)
}
