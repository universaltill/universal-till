package pages

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerJournal(mux *http.ServeMux, d *common.Deps) {
	// Full journal page — receipts/sync history lives here, off the sale screen
	// (real tills keep the checkout screen for the transaction only).
	mux.HandleFunc("/journal", func(w http.ResponseWriter, r *http.Request) {
		_, invoicingOn := sellerConfig(r.Context(), d)
		httpx.Render("ui/pages/journal.html", map[string]any{
			"title":       "Journal",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"InvoicingOn": invoicingOn && canPerform(d, r, "reports"),
		})(w, r)
	})

	// Sale detail: lines, payments, totals, sync state for one receipt.
	mux.HandleFunc("/journal/{receipt}", func(w http.ResponseWriter, r *http.Request) {
		sale, found, err := data.NewPOSRepo(d.Db).GetSaleDetail(r.Context(), r.PathValue("receipt"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		// Cross-links for refunds: a sale lists its returns; a return
		// points back at its original receipt.
		repo := data.NewPOSRepo(d.Db)
		returns, _ := repo.ReturnReceiptsFor(r.Context(), sale.ID)
		original, _, _ := repo.OriginalReceiptFor(r.Context(), sale.ID)
		// Invoicing (G31): show the issue form / existing invoice link
		// only when the seller identity is configured.
		_, invoicingOn := sellerConfig(r.Context(), d)
		invKind := "invoice"
		if sale.SaleType == "return" {
			invKind = "credit_note"
		}
		inv, hasInvoice, _ := data.NewInvoiceRepo(d.Db).BySale(r.Context(), sale.ID, invKind)
		httpx.Render("ui/pages/journal_detail.html", map[string]any{
			"title":       "Receipt " + sale.ReceiptNo,
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"Sale":        sale,
			"Returns":     returns,
			"Original":    original,
			"InvoicingOn": invoicingOn,
			"HasInvoice":  hasInvoice,
			"Invoice":     inv,
		})(w, r)
	})

	mux.HandleFunc("/ui/journal", func(w http.ResponseWriter, r *http.Request) {
		repo := data.NewPOSRepo(d.Db)
		limit := 5
		if r.URL.Query().Get("limit") == "full" {
			limit = 100
		}

		// Enrolled tills drive the filter dropdown, the staleness lines, and
		// (S6, ut-docs#550 review) validate the "till" param below. A lookup
		// failure degrades to "no enrolled tills" rather than failing the
		// whole page (same defensive style as elsewhere here). The roster
		// itself DOES sync down to every replica (ut-docs#405 —
		// internal/data/sync_admin_repo.go's adminTables "tills" entry):
		// only bearer_hash/last_seen_at are redacted on a replica's copy. A
		// replica's own sales table, by contrast, never accumulates
		// siblings' sales — replicas push their own sales one-way up to the
		// primary and never receive siblings' sales back down, so a filter
		// asking for another till's sales can only ever come up empty there
		// (see the IsReplica/ExplicitCrossTillOnReplica handling below).
		tills, err := data.NewTillsRepo(d.Db).ListTills(r.Context())
		if err != nil {
			tills = nil
		}
		validTill := make(map[string]bool, len(tills))
		for _, t := range tills {
			validTill[t.ID] = true
		}

		// day (S5): validate strict YYYY-MM-DD before it ever reaches the
		// SQL date(?) comparison in ListSalesJournal — an invalid value is
		// treated as "no day filter" rather than silently matching nothing.
		day := r.URL.Query().Get("day")
		if day != "" {
			if _, err := time.Parse("2006-01-02", day); err != nil {
				day = ""
			}
		}

		// till: distinguish "operator loaded the bare page" from
		// "operator explicitly picked a filter" via Query().Has, not
		// Get() == "" — an empty query-string value ("This till" picked
		// from the dropdown) and an absent param (bare page load) are
		// otherwise indistinguishable (B3, ut-docs#550 review). Absent, and
		// (S6) present-but-unrecognized, both fall back to the pre-#550
		// default: every till's sales, no filter — matching ListRecentSales
		// (still used by the sale-screen mini-widget, internal/pages/pos_api.go)
		// and this page's own pre-#550 behavior.
		q := r.URL.Query()
		hasTillParam := q.Has("till")
		tillParam := q.Get("till")
		f := data.SalesJournalFilter{Limit: limit, Day: day}
		selectedTill := "all"
		switch {
		case !hasTillParam:
			f.AllTills = true
		case tillParam == "":
			// "This till" explicitly selected from the dropdown.
			selectedTill = ""
		case tillParam == "all":
			f.AllTills = true
		case validTill[tillParam]:
			f.TillID = tillParam
			selectedTill = tillParam
		default:
			// S6: an unrecognized till id would otherwise filter to a dead
			// id the <select> can't even display as selected, silently
			// desyncing the control from what's actually shown. Fall back
			// to all-tills instead.
			f.AllTills = true
		}

		entries, err := repo.ListSalesJournal(r.Context(), f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// B2: only warn when the operator explicitly asked for cross-till
		// data (tillParam present and not the "This till" sentinel) AND
		// this till is a replica — a replica's local sales table can never
		// satisfy that ask (see the comment above ListTills). The bare
		// default view already shows only local data on a replica, which is
		// honest by construction and needs no notice.
		isReplica := d.SyncPrimaryURL(r.Context()) != ""
		explicitCrossTillOnReplica := isReplica && hasTillParam && tillParam != ""

		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		journalView, err := ui.NewJournalView(funcs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = journalView.Render(w, ui.JournalViewData{
			Entries:                    entries,
			ShowFilters:                true,
			Tills:                      tills,
			SelectedTill:               selectedTill,
			Day:                        day,
			IsReplica:                  isReplica,
			ExplicitCrossTillOnReplica: explicitCrossTillOnReplica,
		})
	})
}
