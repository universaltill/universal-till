package pages

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// openOrderRow is one parked order as web/ui/pages/open_orders.html renders
// it: the persisted HeldSale plus the display-ready joins (table label,
// money-typed total, whole minutes since it was FIRST parked).
type openOrderRow struct {
	ID         string
	Label      string
	TableLabel string
	LineCount  int
	Total      money.Money
	// AgeMinutes is elapsedMinutes(created_at) -- since ut-docs#1918 a
	// re-park keeps the original created_at, so this is genuinely "how long
	// has this order been open", not "how long since it was last touched".
	AgeMinutes int
}

// registerOpenOrders wires the Open orders page (ut-docs#1918): every order
// currently parked on this till -- what the sale screen's "On hold" strip
// shows as chips, laid out as a full list with the details the strip has no
// room for (table, item count, total, how long it has been open). Read-only:
// resuming stays on the sale screen (the strip's chip), because a resume
// needs the live basket to be empty and lands the cashier there anyway.
//
// Modelled on registerTables' page half: repo reads at the pages layer,
// display-only joins done here (table id -> current label via
// posRepo.GetTable, the same lookup the resume handler uses, so a table
// renamed while the order was parked shows under its current name), one
// httpx.Render through the base layout. No manager gate: this is a cashier
// surface, same audience as the strip it mirrors (the auth middleware still
// requires an operator session, like every non-exempt page).
//
// Unlike /orders (order_status.go -- the kitchen-progress board for
// COMPLETED sales, displayed as "Order status"), this page is about sales
// that haven't been paid yet. The two are unrelated surfaces that happened
// to share the word "Orders"; ut-docs#1918 renamed the other's display
// string rather than its route or key namespace.
func registerOpenOrders(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewHeldSalesRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("GET /open-orders", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		items, err := repo.List(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "open_orders.error.load_failed", err)
			return
		}
		now := time.Now().UTC()
		// Memoised per distinct table id: several parked orders rarely share
		// a table (IsTableFree forbids it for a move), but a repeat lookup
		// costs nothing to skip. Falls back to the raw id, same as the strip.
		tableLabels := map[string]string{}
		rows := make([]openOrderRow, 0, len(items))
		for _, h := range items {
			row := openOrderRow{
				ID:         h.ID,
				Label:      h.Label,
				LineCount:  h.LineCount,
				Total:      money.FromMinor(h.TotalMinor),
				AgeMinutes: elapsedMinutes(h.CreatedAt, now),
			}
			if h.TableID != "" {
				label, seen := tableLabels[h.TableID]
				if !seen {
					label = h.TableID
					if t, found, err := posRepo.GetTable(ctx, h.TableID); err == nil && found {
						label = t.Label
					}
					tableLabels[h.TableID] = label
				}
				row.TableLabel = label
			}
			rows = append(rows, row)
		}
		httpx.Render("ui/pages/open_orders.html", map[string]any{
			"title":     "Open orders",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"orders":    rows,
		})(w, r)
	})
}
