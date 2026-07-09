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
			"theme":     d.State.Theme,
			"menuItems": d.Menu,
		})(w, r)
	})

	mux.HandleFunc("/ui/journal", func(w http.ResponseWriter, r *http.Request) {
		repo := data.NewPOSRepo(d.Db)
		entries, err := repo.ListRecentSales(r.Context(), 5)
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
