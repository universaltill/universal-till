package pages

import (
	"errors"
	"html"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#944 (ut-docs#924 increment 2 of 4): every raw-error http.Error in
// this file below routes through common.LogAndLocalizedError with this one
// key -- all six sites are generic internal/DB/template failures with no
// business-specific meaning to distinguish (unlike pos_api.go/refund_page.go,
// nothing here has an existing sibling key from a related file to reuse), so
// one new key follows the file's existing "designer." locale namespace
// (designer.add_button, designer.search_type_hint, ...) rather than adding
// six near-identical ones.
const buttonsErrorKey = "designer.error.server"

func registerButtonsAPI(mux *http.ServeMux, d *common.Deps) {
	// requirePrimary gates the reorder/add/remove routes below on this till
	// being the primary (same defect class as ut-docs#1689/#1667/#1590/
	// #1546): shortcut_buttons is synced shop-wide as an admin table
	// (adminTables, sync_admin_repo.go) via a one-way primary-wins pull, so
	// a write accepted on a satellite would silently vanish -- a reorder
	// reverted, an added/removed button undone -- on the very next admin
	// pull, with no indication to the manager who made the change. Refuse
	// it up front instead, same pattern as catalog/handlers.go's
	// requirePrimary: these routes return an HTMX fragment or a bare
	// status, not a full page, so the refusal is a plain localized error
	// response (409) rather than a redirect.
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			common.LocalizedError(w, r, http.StatusConflict, "designer.error.replica_use_primary")
			return false
		}
		return true
	}

	// UI fragment
	mux.HandleFunc("/ui/buttons", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons.html"),
			funcs,
		)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore, View: renderer}
		btnHTTP.List(w, r)
	})

	// Reorder from the Designer (move-up/move-down buttons, ut-docs#1221 --
	// formerly drag&drop): codes arrive in display order.
	mux.HandleFunc("POST /api/buttons/reorder", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r) {
			return
		}
		// The Designer posts FormData (multipart) — ParseForm alone ignores
		// multipart bodies, which silently dropped every reorder.
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
		} else {
			_ = r.ParseForm()
		}
		codes := r.Form["codes"]
		if len(codes) == 1 && strings.Contains(codes[0], ",") {
			codes = strings.Split(codes[0], ",")
		}
		if len(codes) == 0 {
			http.Error(w, "codes required", http.StatusBadRequest)
			return
		}
		for i := range codes {
			codes[i] = strings.TrimSpace(codes[i])
		}
		if err := d.BtnStore.UpdateOrder(r.Context(), codes); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		// ut-docs#2285: the Designer's own drag&drop/move-up/move-down
		// reorder changes the sale screen's button set too, same as
		// /api/buttons/move|remove|add below -- see buttons.html's root
		// comment on buttons-changed.
		w.Header().Set("HX-Trigger", "buttons-changed")
		w.WriteHeader(http.StatusNoContent)
	})

	// The sell-screen tile's long-press/right-click sheet (ut-docs#2285):
	// GET renders it for one tile; POST /api/buttons/move relocates that
	// tile next to its nearest same-category neighbour (ui.ButtonStore.Move)
	// and re-renders the SAME sheet so it stays open with the now-current
	// edge buttons disabled. Both share renderTileSheet below so the
	// code->button lookup (and the 404-vs-500 distinction) is written once.
	mux.HandleFunc("GET /ui/pos/tile-sheet", func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		renderer, err := tileSheetRenderer(w, r)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		renderTileSheet(w, r, d, renderer, code)
	})

	mux.HandleFunc("POST /api/buttons/move", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		code := strings.TrimSpace(r.Form.Get("code"))
		dir, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("dir")))
		if err != nil || (dir != 1 && dir != -1) {
			common.LocalizedError(w, r, http.StatusBadRequest, buttonsErrorKey)
			return
		}
		if _, err := d.BtnStore.Move(r.Context(), code, dir); err != nil {
			if errors.Is(err, ui.ErrButtonNotFound) {
				common.LocalizedError(w, r, http.StatusNotFound, buttonsErrorKey)
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		w.Header().Set("HX-Trigger", "buttons-changed")
		renderer, err := tileSheetRenderer(w, r)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		renderTileSheet(w, r, d, renderer, code)
	})

	// Admin add/remove
	mux.HandleFunc("/api/buttons/add", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r) {
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons_admin.html"),
			funcs,
		)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore, View: renderer}
		btnHTTP.Add(w, r)
	})

	mux.HandleFunc("/api/buttons/remove", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r) {
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons_admin.html"),
			funcs,
		)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore, View: renderer}
		btnHTTP.Remove(w, r)
	})

	// Item search for shortcuts (HTMX fragment)
	mux.HandleFunc("/api/buttons/search", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.FormValue("q")
		if q == "" {
			q = r.FormValue("search")
		}
		if len(strings.TrimSpace(q)) < 3 {
			locale := httpx.ResolveLocale(w, r)
			_, _ = w.Write([]byte(`<div class="results muted">` + html.EscapeString(httpx.T(locale, "designer.search_type_hint")) + `</div>`))
			return
		}
		offset := 0
		if off := r.URL.Query().Get("offset"); off != "" {
			if v, err := strconv.Atoi(off); err == nil && v >= 0 {
				offset = v
			}
		}
		results, err := d.BtnStore.SearchItems(r.Context(), q, offset, 10)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons_admin.html"),
			funcs,
		)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		_ = renderer.Render(w, "buttons_search_results", map[string]any{"Results": results})
	})
}

// tileSheetRenderer builds the "tile_sheet" partial's renderer, same
// (layout, page, partial) construction pattern every other route in this
// file uses — ui.NewRenderer's own per-tuple cache (see its doc comment)
// means this is cheap to call once per request rather than threading a
// renderer through registerButtonsAPI's closures.
func tileSheetRenderer(w http.ResponseWriter, r *http.Request) (*ui.Renderer, error) {
	funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
	return ui.NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "tile_sheet.html"),
		funcs,
	)
}

// renderTileSheet resolves code against the CURRENT button list and
// writes the "tile_sheet" fragment — shared by GET /ui/pos/tile-sheet and
// POST /api/buttons/move's own re-render, so the code->button lookup (and
// its 404 handling) is written exactly once. isReplica (ui.TileSheetView's
// own field) is resolved here, not inside internal/ui, since only this
// package has access to common.Deps.SyncPrimaryURL.
func renderTileSheet(w http.ResponseWriter, r *http.Request, d *common.Deps, renderer *ui.Renderer, code string) {
	isReplica := d.SyncPrimaryURL(r.Context()) != ""
	view, ok, err := d.BtnStore.BuildTileSheetView(code, isReplica)
	if err != nil {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
		return
	}
	if !ok {
		common.LocalizedError(w, r, http.StatusNotFound, buttonsErrorKey)
		return
	}
	_ = renderer.Render(w, "tile_sheet", view)
}
