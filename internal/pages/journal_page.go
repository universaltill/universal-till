package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerJournal(mux *http.ServeMux, d *common.Deps) {
	// Full journal page — receipts/sync history lives here, off the sale screen
	// (real tills keep the checkout screen for the transaction only).
	mux.HandleFunc("/journal", func(w http.ResponseWriter, r *http.Request) {
		httpx.Render("ui/pages/journal.html", map[string]any{
			"title":     "Journal",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
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
		httpx.Render("ui/pages/journal_detail.html", map[string]any{
			"title":     "Receipt " + sale.ReceiptNo,
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
			"Sale":      sale,
			"Returns":   returns,
			"Original":  original,
		})(w, r)
	})

	mux.HandleFunc("/ui/journal", func(w http.ResponseWriter, r *http.Request) {
		repo := data.NewPOSRepo(d.Db)
		limit := 5
		if r.URL.Query().Get("limit") == "full" {
			limit = 100
		}
		entries, err := repo.ListRecentSales(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		journalView, err := ui.NewJournalView(funcs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = journalView.Render(w, ui.JournalViewData{Entries: entries})
	})
}
