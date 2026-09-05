package pages

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Kitchen Display System, HDMI-local slice (universaltill/ut-docs#544): a
// per-station live order board — GET /kitchen-display/{station_id} — for a
// second screen driven by the machine already running the till (a second
// HDMI output, a browser window dragged onto it). No network hop, no
// pairing: it is just another session-authed page on this till, opened from
// the "View display" link on /kitchen-stations.
//
// It is the /orders board (order_status.go, ut-docs#526/#517/ADR-0079)
// scoped to one station, and deliberately builds on ALL of its mechanism
// rather than a second one:
//
//   - the page shell mirrors orders.html: load the fragment once, then the
//     fragment's own root re-arms the 15s poll (offline-first fallback) and
//     listens for the "orders-push" nudge the page's EventSource fires on
//     every event of the SAME /api/orders/stream;
//   - the fragment renders the SAME ui/partials/orders_list.html with the
//     SAME row shape (orderRowsFor) — one-tap buttons post to the EXISTING
//     POST /api/orders/{receipt_no}/status, no new write endpoint;
//   - the only new query is data.POSRepo.ListRecentOrdersForStation, which
//     restates ResolveKitchenStations' item-over-category precedence in SQL
//     (its doc comment carries the argument).
//
// Same auth posture as /orders: session-authed by the middleware, no manager
// gate — reading a kitchen screen and tapping "Ready" is floor work.
//
// Known, deliberate v1 limitations (this slice, not bugs to fix here):
//
//   - LOCAL ORDERS ONLY. The fragment reads this till's own sales, with no
//     primary/replica cross-till proxy (unlike /ui/orders'
//     fetchOrdersFromPrimary, ut-docs#1350). A screen on a replica shows
//     what that till's own /orders would show when the primary is
//     unreachable — parity with the offline view, not a regression; the
//     proxy needs a station-scoped sync endpoint on the primary and is a
//     follow-up. The LAN-paired remote KDS device (its own pairing, auth
//     and liveness) is a separate card altogether.
//   - STATUS IS PER ORDER, NOT PER LINE. There is no per-line status model
//     anywhere (ut-docs#526), so an order whose lines split across two
//     display stations appears on both screens, and advancing it on either
//     clears it from both. Matches /orders' granularity everywhere else.

// kitchenDisplayStation resolves a route's station id to a station that has
// a screen to show: exists, enabled, and display-capable ('display' or
// 'both'). ok=false for anything else — a printer-only or deactivated
// station has no screen, so the URL is a 404, not an empty board (the admin
// page only ever links to stations this returns ok for).
func kitchenDisplayStation(r *http.Request, repo *data.POSRepo) (data.KitchenStation, bool, error) {
	s, ok, err := repo.GetKitchenStation(r.Context(), r.PathValue("station_id"))
	if err != nil {
		return data.KitchenStation{}, false, err
	}
	if !ok || !s.Enabled || !s.ShowsOnDisplay() {
		return data.KitchenStation{}, false, nil
	}
	return s, true, nil
}

func registerKitchenDisplay(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("GET /kitchen-display/{station_id}", func(w http.ResponseWriter, r *http.Request) {
		station, ok, err := kitchenDisplayStation(r, posRepo)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "orders.err.server", err)
			return
		}
		if !ok {
			// Through the layout, never bare text: a wrong/stale link on a
			// pinned kiosk screen must still leave a way back (ut-docs#1455).
			httpx.RenderError(w, r, http.StatusNotFound, "kitchendisplay.error.not_found",
				errors.New("kitchen display: station "+r.PathValue("station_id")+" missing, disabled or not display-capable"))
			return
		}
		httpx.Render("ui/pages/kitchen_display.html", map[string]any{
			"title":       "Kitchen display",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"stationName": station.Name,
			"fragmentURL": kitchenDisplayFragmentURL(station.ID),
		})(w, r)
	})

	mux.HandleFunc("GET /ui/kitchen-display/{station_id}", func(w http.ResponseWriter, r *http.Request) {
		station, ok, err := kitchenDisplayStation(r, posRepo)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "orders.err.server", "kitchen-display", err)
			return
		}
		if !ok {
			common.LocalizedError(w, r, http.StatusNotFound, "kitchendisplay.error.not_found")
			return
		}
		// Local orders only — see the file comment for why there is no
		// fetchOrdersFromPrimary here. fromPrimary=false: every row is this
		// till's own sale, so every receipt link resolves.
		entries, err := posRepo.ListRecentOrdersForStation(r.Context(), station.ID, 50)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "orders.err.server", "kitchen-display", err)
			return
		}
		rows := orderRowsFor(r.Context(), posRepo, entries, false)
		httpx.RenderPartial("ui/partials/orders_list.html", map[string]any{
			"Orders":      rows,
			"FragmentURL": kitchenDisplayFragmentURL(station.ID),
			"EmptyKey":    "kitchendisplay.empty",
		})(w, r)
	})
}

// kitchenDisplayFragmentURL is the one place the fragment URL is spelled,
// shared by the page shell (initial load) and the fragment root (re-arm).
// Station ids are uuids today; PathEscape keeps it correct regardless.
func kitchenDisplayFragmentURL(stationID string) string {
	return "/ui/kitchen-display/" + url.PathEscape(stationID)
}
