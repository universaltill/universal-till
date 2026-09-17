package pages

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
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
	posRepo := data.NewPOSRepo(d.Db)

	// auditButtonsElevated records a manager-PIN-approved shortcut-button
	// mutation with dual attribution (ut-docs#2312, mechanism ut-docs#557)
	// -- actorID is the APPROVER who actually performed it, blockedActorID
	// the originally-denied session operator (elev.ActorID), same shape as
	// users_page.go's own auditElevated. Scoped to the two routes below
	// (reorder/move) that call d.BtnStore directly inline; Add/Remove
	// delegate to ui.ButtonsHTTP, which writes its own response with no
	// success/failure signal back to this closure to audit against --
	// auditing those two is deferred (see registerButtonsAPI's own routes
	// below for the note) rather than restructuring ButtonsHTTP's return
	// shape for this card.
	auditButtonsElevated := func(r *http.Request, actorID, blockedActorID, targetID, action string, payload map[string]any) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAuditElevated(r.Context(), nil, actorID, blockedActorID, "shortcut_button", targetID, action, payload, now, "")
	}

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
		// ut-docs#2312: the Designer's reorder is a plain fetch(), not an
		// htmx request (see designer.html's persistOrder/utPostWithElevation
		// wiring) -- hxTarget is passed through to renderElevationPrompt for
		// contract-consistency with every other checkOrElevate call site,
		// but is never actually rendered/used by a non-htmx caller (that JS
		// helper only ever extracts #elevation-modal from the body).
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/reorder", "#buttons-add-error",
				httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_reorder"),
				[]elevationHiddenField{{Name: "codes", Value: strings.Join(codes, ",")}}, elev)
			return
		}
		if err := d.BtnStore.UpdateOrder(r.Context(), codes); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, buttonsErrorKey, "buttons", err)
			return
		}
		if elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, "-", "buttons_reorder", map[string]any{"codes": codes})
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
		// ut-docs#2312: the tile sheet renders Move/Remove/Edit for a
		// non-granted operator disabled-looking-but-clickable (see
		// renderTileSheet's Locked field below) precisely so a tap here
		// lands on this real elevation prompt rather than a dead button --
		// "show, don't hide" (#2285's UX decision) means discoverability,
		// not a silent no-op.
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/move", "#tile-sheet",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_move"), code),
				[]elevationHiddenField{{Name: "code", Value: code}, {Name: "dir", Value: strconv.Itoa(dir)}}, elev)
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
		if elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, code, "buttons_move", map[string]any{"dir": dir})
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
		// ut-docs#2312: parsed here (ahead of ui.ButtonsHTTP.Add's own,
		// idempotent ParseForm call below) so the elevation check has
		// label/code/itemId/imageUrl to mirror as hidden fields on the
		// dialog's retry -- checkOrElevate itself never touches the body.
		_ = r.ParseForm()
		label := r.Form.Get("label")
		code := r.Form.Get("code")
		itemID := r.Form.Get("itemId")
		imageURL := r.Form.Get("imageUrl")
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/add", "#buttons-grid-wrap",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_add"), label),
				[]elevationHiddenField{
					{Name: "label", Value: label},
					{Name: "code", Value: code},
					{Name: "itemId", Value: itemID},
					{Name: "imageUrl", Value: imageURL},
				}, elev)
			return
		}
		// ut-docs#2312: dual-attribution audit for the elevated case is
		// deferred here -- see registerButtonsAPI's own auditButtonsElevated
		// doc comment (ButtonsHTTP.Add writes its own response with no
		// success/failure signal back to this closure).
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
		// ut-docs#2312: parsed here (ahead of ui.ButtonsHTTP.Remove's own,
		// idempotent ParseForm call below) so the elevation check has the
		// code to mirror as a hidden field on the dialog's retry.
		//
		// This route is reached from TWO different surfaces with different
		// hx-target/hx-swap of their own -- the Designer's grid
		// (buttons_admin.html, "#buttons-grid-wrap"/innerHTML) and the
		// sell-screen tile sheet (tile_sheet.html, hx-swap="none" plus
		// data-closes-sheet, ut-docs#2285). "#buttons-grid-wrap" is used as
		// the elevation retry target unconditionally either way: on the
		// Designer it's exactly the original target; from the tile sheet
		// that id doesn't exist in the DOM at all, so the retry's own
		// response there is silently unswapped by htmx (same as any
		// unmatched hx-target) -- but the SALE SCREEN grid still updates
		// correctly regardless, because HX-Trigger: buttons-changed
		// (ButtonsHTTP.Remove's own header, unconditional on success) fires
		// independently of target resolution and is what buttons.html's
		// root actually listens for. The one accepted gap: the tile sheet
		// itself doesn't auto-close after an ELEVATED remove approval (the
		// dialog's retry form lives outside #tile-sheet, so app.js's
		// data-closes-sheet listener never sees it) -- a tap on the sheet's
		// own Close button clears it, same as any other stale panel.
		_ = r.ParseForm()
		code := r.Form.Get("code")
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/remove", "#buttons-grid-wrap",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_remove"), code),
				[]elevationHiddenField{{Name: "code", Value: code}}, elev)
			return
		}
		// ut-docs#2312: dual-attribution audit for the elevated case is
		// deferred here -- see registerButtonsAPI's own auditButtonsElevated
		// doc comment (ButtonsHTTP.Remove writes its own response with no
		// success/failure signal back to this closure).
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
	// ut-docs#2312: "show, don't hide" (#2285) -- a non-granted operator
	// still sees Move/Remove/Edit, just locked (TileSheetView.Locked's own
	// doc comment), rather than the sheet quietly omitting them.
	locked := !canPerform(d, r, "catalog_management")
	view, ok, err := d.BtnStore.BuildTileSheetView(code, isReplica, locked)
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
