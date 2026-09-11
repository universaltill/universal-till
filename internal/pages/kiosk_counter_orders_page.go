package pages

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// counterOrderRow is one row on the staff-facing "pay at counter" board
// (ut-docs#582) — the subset of data.KioskCounterOrder the template
// actually renders, Items already joined to one display string. AgeMinutes
// is how long the order has been waiting (elapsedMinutes, same convention
// as the tables floor plan's OpenMinutes — tables_page.go /
// tables.status.open_minutes) — this board's whole point is letting staff
// spot a stale order at a glance, which an absolute wall-clock timestamp
// doesn't give them.
type counterOrderRow struct {
	ID         string
	DisplayNo  string
	OrderType  string
	Items      string
	AgeMinutes int
}

// counterOrderItemsSummary joins a counter order's lines into one
// "Name × Qty" list for the board — this staff-facing surface has no
// per-line cells the way orders_list.html does, since a counter order
// carries no price to put in one.
func counterOrderItemsSummary(lines []data.KioskCounterOrderLine) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, fmt.Sprintf("%s × %s", l.Name, l.Qty))
	}
	return strings.Join(parts, ", ")
}

func counterOrderRowsFor(orders []data.KioskCounterOrder) []counterOrderRow {
	now := time.Now()
	rows := make([]counterOrderRow, 0, len(orders))
	for _, o := range orders {
		rows = append(rows, counterOrderRow{
			ID:         o.ID,
			DisplayNo:  o.DisplayNo,
			OrderType:  o.OrderType,
			Items:      counterOrderItemsSummary(o.Lines),
			AgeMinutes: elapsedMinutes(o.CreatedAt, now),
		})
	}
	return rows
}

// registerKioskCounterOrdersPage wires the staff-facing "pay at counter"
// board (ut-docs#582): a NORMAL authenticated route — unlike
// self_order_shop.go's /self-order and /api/self-order/* (auth-exempt,
// anonymous customer surface), this is an operator page, gated by the
// ordinary auth middleware like /orders. Same 15s self-re-arming poll
// pattern as /orders (order_status.go / orders_list.html) — see
// kiosk_counter_orders_list.html's own root div for the exact shape.
func registerKioskCounterOrdersPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /kiosk-counter-orders", func(w http.ResponseWriter, r *http.Request) {
		httpx.Render("ui/pages/kiosk_counter_orders.html", map[string]any{
			"title":     "Pay at counter",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
		})(w, r)
	})

	mux.HandleFunc("GET /ui/kiosk-counter-orders", func(w http.ResponseWriter, r *http.Request) {
		orders, err := data.NewKioskCounterOrdersRepo(d.Db).ListOpen(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "orders.err.server", "kiosk_counter_orders", err)
			return
		}
		httpx.RenderPartial("ui/partials/kiosk_counter_orders_list.html", map[string]any{
			"Orders": counterOrderRowsFor(orders),
		})(w, r)
	})

	// Any operator may fire it — same "prep/collection progress is floor
	// work, not a manager action" reasoning as the manual kitchen-ticket
	// print endpoint (registerKitchenPrintAPI, kitchen_print.go).
	mux.HandleFunc("POST /api/kiosk-counter-orders/{id}/collect", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := data.NewKioskCounterOrdersRepo(d.Db).MarkCollected(r.Context(), id); err != nil {
			http.Error(w, "failed to mark collected", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Same htmx out-of-band "delete" shape as writeOrderStatusFragment's
		// own terminal-status row removal (order_status.go) — the row
		// leaves the board immediately instead of waiting for the next 15s
		// poll (which won't return it anyway, now that MarkCollected has
		// moved it out of ListOpen).
		fmt.Fprintf(w, `<div id="counter-order-row-%s" hx-swap-oob="delete"></div>`, template.HTMLEscapeString(id))
	})
}
