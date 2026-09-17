package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerDesigner(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/designer", func(w http.ResponseWriter, r *http.Request) {
		// ut-docs#2357: the nav tile is already VisibleIf: "catalog_management"
		// (uislot.CoreAdmin), but a cashier typing the URL directly still got
		// the full page before this gate — matching tax_codes_page.go/
		// locations_page.go's own GET-handler gate pattern.
		if !canPerform(d, r, "catalog_management") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return
		}
		btns, _ := d.BtnStore.Load()
		data := map[string]any{
			"title":     httpx.T(httpx.RequestLocale(r), "page.title.quick_buttons"),
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Buttons":   ui.ToVM(btns),
		}
		httpx.Render("ui/pages/designer.html", data)(w, r)
	})
}
