package pages

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerIndex(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/help" {
			renderHelpPage(w, r, d)
			return
		}
		// The "/" pattern catches every otherwise-unrouted path. Plugin page
		// entries may register arbitrary routes (e.g. /faq), so dispatch those
		// here; anything else unknown is a 404 rather than silently showing
		// the home page.
		if r.URL.Path != "/" {
			if entry, ok := findPageEntry(r, d); ok {
				renderPluginPage(w, r, d, entry)
				return
			}
			http.NotFound(w, r)
			return
		}
		methods := collectPaymentMethods(r.Context(), d.Db)
		defaultMethod := "cash"
		if len(methods) > 0 {
			defaultMethod = methods[0]
		}
		// Full method rows (label + plugin provenance) drive the Pay tab.
		payMethods, _ := data.NewPOSRepo(d.Db).ListActivePaymentMethods(r.Context())
		data := map[string]any{
			"title":                "Universal Till",
			"theme":                d.State.Theme,
			"menuItems":            d.Menu,
			"currency":             d.State.Currency,
			"paymentMethods":       methods,
			"paymentMethodDefault": defaultMethod,
			"payMethods":           payMethods,
		}
		httpx.Render("ui/pages/index.html", data)(w, r)
	})
}

func collectPaymentMethods(ctx context.Context, db *sql.DB) []string {
	defaults := []string{"cash", "card", "voucher", "account"}
	if db == nil {
		return defaults
	}
	methods, err := data.NewPOSRepo(db).ListPaymentMethodIDs(ctx)
	if err != nil {
		return defaults
	}
	if len(methods) == 0 {
		return defaults
	}
	return methods
}
