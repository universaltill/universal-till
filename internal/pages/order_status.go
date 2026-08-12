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
	"github.com/universaltill/universal-till/internal/pos"
)

// Order lifecycle status (ut-docs#526): one-tap new/preparing/ready/collected
// (+ terminal cancelled) on completed sales — the backbone the Kitchen
// Display (#516), pagers (#517) and customer tracking (#528/#527) will build
// on. Same shape as registerKitchenPrintAPI: operate on a receipt_no, no
// manager gate (marking an order ready is normal floor work), HTMX fragment
// response, journaled.
//
// The /orders page itself is a deliberately minimal placeholder surface —
// it proves the mechanism end to end; the real KDS/live-order view is #516's
// card, built on the same repo methods and broadcaster.

// orderStatusLabelKey maps a stored status (” = untracked) to its locale
// key. Keys exist in every file under web/locales/ (guard-i18n.sh).
func orderStatusLabelKey(status string) string {
	if status == "" {
		return "orders.status.none"
	}
	return "orders.status." + status
}

// writeOrderStatusFragment renders the current-state fragment the one-tap
// endpoint swaps into the status cell: status label + who/when. It renders
// the POST-write truth whether the write applied or was dropped as stale —
// a dropped write's response simply re-shows the unchanged current state.
func writeOrderStatusFragment(w http.ResponseWriter, locale, status, who, when string) {
	label := httpx.T(locale, orderStatusLabelKey(status))
	fmt.Fprintf(w, `<span class="order-status" data-status="%s">%s</span>`,
		template.HTMLEscapeString(status), template.HTMLEscapeString(label))
	if who != "" || when != "" {
		fmt.Fprintf(w, ` <span class="muted">%s · %s</span>`,
			template.HTMLEscapeString(who), template.HTMLEscapeString(when))
	}
}

func registerOrderStatus(mux *http.ServeMux, d *common.Deps) {
	// Minimal recent-orders page: list + one-tap buttons, loaded as a
	// fragment so a tap can swap just the row's status cell.
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		httpx.Render("ui/pages/orders.html", map[string]any{
			"title":     "Orders",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
		})(w, r)
	})

	mux.HandleFunc("GET /ui/orders", func(w http.ResponseWriter, r *http.Request) {
		entries, err := data.NewPOSRepo(d.Db).ListRecentOrders(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		type orderRow struct {
			ReceiptNo       string
			OrderType       string
			StatusKey       string
			Status          string
			StatusUpdatedAt string
			CreatedAt       string
			// ut-docs#517a: latest kitchen/receipt print attempt failed —
			// surfaced as an inline warning next to the status.
			KitchenPrintFailed bool
			ReceiptPrintFailed bool
		}
		rows := make([]orderRow, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, orderRow{
				ReceiptNo:          e.ReceiptNo,
				OrderType:          e.OrderType,
				StatusKey:          orderStatusLabelKey(e.Status),
				Status:             e.Status,
				StatusUpdatedAt:    e.StatusUpdatedAt,
				CreatedAt:          e.CreatedAt,
				KitchenPrintFailed: e.KitchenPrintFailedAt != "",
				ReceiptPrintFailed: e.ReceiptPrintFailedAt != "",
			})
		}
		httpx.RenderPartial("ui/partials/orders_list.html", map[string]any{"Orders": rows})(w, r)
	})

	// One-tap status change. Any operator may fire it (same reasoning as
	// the kitchen-ticket print: prep progress is floor work, not a manager
	// action). The conflict rule (pos.OrderStatusAllowed, decided per
	// ADR-0011's fixed-and-simple philosophy) makes a stale/backward tap a
	// silent 200 no-op showing the unchanged current state — never an error,
	// never a visible regression.
	mux.HandleFunc("POST /api/orders/{receipt_no}/status", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		receiptNo := strings.TrimSpace(r.PathValue("receipt_no"))
		next := strings.TrimSpace(r.Form.Get("status"))
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fail := func(status int, key string) {
			w.WriteHeader(status)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, key))
		}
		if !pos.ValidOrderStatus(next) {
			fail(http.StatusBadRequest, "orders.err.bad_status")
			return
		}
		actorID := getSessionUserID(r)
		now := time.Now().UTC().Format(time.RFC3339)
		repo := data.NewPOSRepo(d.Db)
		applied, found, err := repo.ApplyOrderStatus(r.Context(), receiptNo, next, actorID, now,
			func(current string) bool { return pos.OrderStatusAllowed(current, next) })
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !found {
			fail(http.StatusNotFound, "orders.err.not_found")
			return
		}
		if applied {
			// Journal-side effects only on an APPLIED write (ut-docs#526
			// item 4): audit-trail entry + broadcast to live subscribers.
			_ = repo.InsertAudit(r.Context(), nil, actorID, "sale", receiptNo, "order_status_changed",
				map[string]any{"status": next}, now, "")
			if d.OrderStatus != nil {
				d.OrderStatus.Publish(pos.OrderStatusChanged{ReceiptNo: receiptNo, Status: next, ActorID: actorID, At: now})
			}
		}
		// Render the post-write truth (applied or dropped alike) from the
		// journal's newest event — its actor/time is the current state's
		// who/when.
		ev, ok, err := repo.LatestOrderStatus(r.Context(), receiptNo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			// Sale exists but was never tracked (only possible when the
			// write was dropped): show the untracked state.
			writeOrderStatusFragment(w, locale, "", "", "")
			return
		}
		who := ev.ActorName
		if who == "" {
			who = ev.ActorID
		}
		writeOrderStatusFragment(w, locale, ev.Status, who, ev.CreatedAt)
	})
}
