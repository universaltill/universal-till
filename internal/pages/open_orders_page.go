package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
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
// room for (table, item count, total, how long it has been open). ut-docs#2138:
// tapping a row resumes it from here too, not just from the sale screen's own
// popup (ut-docs#2137) -- a list that cannot open what it lists was a dead
// end for the one cashier who reached for it. Still enforces the empty-basket
// rule (resumeHeldSale, hold_api.go): refused, the order stays parked and
// listed, exactly as before.
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

	// listOpenOrders builds the display-ready rows both surfaces render: the
	// full /open-orders page and the sale screen's parked-orders popup
	// (ut-docs#2137). One reader, so the two can never disagree about what is
	// parked, what it is worth, or which table it is on.
	listOpenOrders := func(ctx context.Context) ([]openOrderRow, error) {
		items, err := repo.List(ctx)
		if err != nil {
			return nil, err
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
		return rows, nil
	}

	mux.HandleFunc("GET /open-orders", func(w http.ResponseWriter, r *http.Request) {
		rows, err := listOpenOrders(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "open_orders.error.load_failed", err)
			return
		}
		httpx.Render("ui/pages/open_orders.html", map[string]any{
			"title":     "Open orders",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"orders":    rows,
			// ut-docs#2138: set only by the resume route's redirect below, on
			// refusal -- the same ?err=<i18n key> + "login-error" banner
			// convention country_settings_page.go's renderPage already uses.
			"errKey": r.URL.Query().Get("err"),
		})(w, r)
	})

	// Resume a row tapped on THIS page (ut-docs#2138) -- distinct from the
	// sale-screen popup's POST /api/pos/resume (hold_api.go, ut-docs#2137):
	// that one is an htmx fragment swapped into #basket, which this page does
	// not have. resumeHeldSale (hold_api.go) carries the actual logic, shared
	// so the ut-docs#820 table re-resolution and ut-docs#1390 claim handling
	// exist in exactly one place. A plain redirect, not htmx, because this is
	// a full-page navigation: success lands the cashier on the sale screen
	// with the resumed basket; a refusal returns here with the existing
	// hold.error.busy message and the order still parked and listed.
	mux.HandleFunc("POST /open-orders/resume", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		switch resumeHeldSale(ctx, d, repo, posRepo, id) {
		case resumeBusy:
			http.Redirect(w, r, "/open-orders?err=hold.error.busy", http.StatusSeeOther)
		case resumeNotFound:
			http.Redirect(w, r, "/open-orders?err=hold.error.not_found", http.StatusSeeOther)
		case resumeFailed:
			http.Redirect(w, r, "/open-orders?err=hold.error.failed", http.StatusSeeOther)
		default:
			http.Redirect(w, r, "/", http.StatusSeeOther)
		}
	})

	// Parked-orders popup body (ut-docs#2137), opened from the button beside
	// Card on the sale screen. A fragment rather than part of the sale
	// screen's own render: it is fetched each time the popup opens, so it
	// cannot show an order that was paid or resumed since the page loaded.
	//
	// Why this exists at all: the strip that was supposed to be the way back
	// to a parked order is clipped off-screen on the pilot tablet
	// (ut-docs#2128), and /open-orders -- the other way -- was read-only and
	// pointed the cashier at that same strip. A parked order was
	// unreachable on that device.
	mux.HandleFunc("GET /ui/parked-orders", func(w http.ResponseWriter, r *http.Request) {
		rows, err := listOpenOrders(r.Context())
		if err != nil {
			// NEVER fall through to the empty-state body here (ut-docs#2137
			// review): "No open orders right now" is the one thing a cashier
			// with parked orders must not be told falsely -- it reads as
			// "your order is gone", and the recovery is to re-ring the whole
			// sale. A read failure has to look like a failure. 500 leaves the
			// popup body unswapped and lets app.js's htmx:responseError
			// handler raise the usual server banner, which is how every other
			// fragment on this screen reports the same thing.
			logging.L().Errorf("parked-orders popup: list held sales: %v", err)
			http.Error(w, "could not load parked orders", http.StatusInternalServerError)
			return
		}
		httpx.RenderPartial("ui/partials/parked_orders.html", map[string]any{
			"orders": rows,
		})(w, r)
	})
}
