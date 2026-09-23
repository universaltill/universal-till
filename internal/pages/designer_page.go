package pages

import (
	"net/http"
	"path/filepath"

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
		// ut-docs#2174: the page no longer renders a button list of its own.
		// designer.html hosts a placeholder that fetches GET
		// /ui/designer/buttons (below) on load — the live replica of the sale
		// screen's product panel, rendered by the very same buttons.html the
		// sale screen uses — plus buttons_admin.html's add-a-button search.
		data := map[string]any{
			"title":     httpx.T(httpx.RequestLocale(r), "page.title.quick_buttons"),
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
		}
		httpx.Render("ui/pages/designer.html", data)(w, r)
	})

	// ut-docs#2174: the Designer's live replica of the sale screen's product
	// panel. Same renderer triple and the same ui.ButtonsHTTP.List as GET
	// /ui/buttons (buttons_api.go), with Designer: true — see that field's
	// own doc comment for the exact differences (empty categories kept,
	// self-refresh from THIS route, edit affordances on the category strip).
	// Gated exactly like /designer itself; a bare LocalizedError rather than
	// a full RenderError page because this is an htmx fragment, not a page
	// (the same split categories_page.go's requireManager/requirePageManager
	// makes). Granted is therefore always true past the gate: the tile
	// badges never need the cashier lock affordance here.
	mux.HandleFunc("GET /ui/designer/buttons", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "catalog_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons.html"),
			funcs,
		)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "designer", err)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{
			Store:      *d.BtnStore,
			View:       renderer,
			HideAllTab: !d.CurrentState().ShowAllTabOnSellScreen,
			Granted:    true,
			Designer:   true,
		}
		btnHTTP.List(w, r)
	})

	registerDesignerCategoriesAPI(mux, d)
}
