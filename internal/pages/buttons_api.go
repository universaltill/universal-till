package pages

import (
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
	// users_page.go's own auditElevated. Used by all three routes below
	// (add/remove/reorder) -- ut-docs#2358 gave ui.ButtonsHTTP.Add/Remove a
	// bool return so add/remove could call this symmetrically with
	// reorder/move, which already called it inline (they write straight to
	// d.BtnStore, so they never needed the extra signal Add/Remove do).
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
		// ut-docs#2361: the jiggle-mode edit/remove badges need to know
		// up front whether THIS session already holds catalog_management
		// — the same permission /api/buttons/{add,remove,reorder} and
		// /catalog itself gate on (ut-docs#2312) — so buttons.html can
		// show a lock affordance before a cashier drags/taps into the
		// real elevation prompt, instead of only discovering it needs a
		// PIN after acting.
		granted := canPerform(d, r, "catalog_management")
		btnHTTP := &ui.ButtonsHTTP{
			Store:      *d.BtnStore,
			View:       renderer,
			HideAllTab: !d.CurrentState().ShowAllTabOnSellScreen,
			Granted:    granted,
		}
		btnHTTP.List(w, r)
	})

	// Sell-screen live search (ut-docs#2294): every active catalog item
	// matching q, rendered as the same tile component a quick-button/All-tab
	// tile already uses -- distinct from /api/buttons/search above, which
	// is the Designer's own "add as a shortcut" search.
	mux.HandleFunc("/ui/buttons/search", func(w http.ResponseWriter, r *http.Request) {
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
		btnHTTP.Search(w, r)
	})

	// Reorder from the Designer (move-up/move-down buttons, ut-docs#1221 --
	// formerly drag&drop) AND from the sell screen's own jiggle edit mode
	// (ut-docs#2339, app.js's utTileJiggle -- which replaced the
	// ut-docs#2285 long-press sheet and its POST /api/buttons/move route):
	// codes arrive in display order, the FULL global list, exactly once per
	// edit session (on Done), never per drag step.
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
		// ut-docs#2285: the Designer's own move-up/move-down reorder changes
		// the sale screen's button set too, same as /api/buttons/remove|add
		// below -- see buttons.html's root comment on buttons-changed. (The
		// sell screen's jiggle-mode Done, ut-docs#2339, posts here as well;
		// its own DOM already shows the new order, so that refetch is a
		// harmless re-render from the now-persisted truth.)
		w.Header().Set("HX-Trigger", "buttons-changed")
		w.WriteHeader(http.StatusNoContent)
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
		// ut-docs#2358: ButtonsHTTP.Add now reports success/failure back to
		// this closure, so the dual-attribution audit that reorder/move
		// already write can fire symmetrically here too -- only on an
		// actual persisted add, and only for the elevated (PIN-override)
		// case, same as reorder's own auditButtonsElevated call above.
		if ok := btnHTTP.Add(w, r); ok && elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, itemID, "buttons_add", map[string]any{"label": label, "code": code, "item_id": itemID})
		}
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
		// (buttons_admin.html, "#buttons-grid-wrap"/innerHTML) and the sell
		// screen's jiggle-mode remove badge (buttons.html's
		// .tile-badge-remove, hx-swap="none", ut-docs#2339). "#buttons-grid-wrap"
		// is used as the elevation retry target unconditionally either way:
		// on the Designer it's exactly the original target; from the jiggle
		// badge that id doesn't exist in the DOM at all, so the retry's own
		// response there is silently unswapped by htmx (same as any
		// unmatched hx-target) -- but the SALE SCREEN grid still updates
		// correctly regardless, because HX-Trigger: buttons-changed
		// (ButtonsHTTP.Remove's own header, unconditional on success) fires
		// independently of target resolution and is what buttons.html's
		// root actually listens for, AND the elevation dialog itself renders
		// regardless of hx-swap="none" -- it's OOB-swapped into the shared
		// #elevation-modal placeholder (elevation_prompt.html), a swap htmx
		// processes independently of the triggering element's own hx-swap.
		_ = r.ParseForm()
		code := r.Form.Get("code")
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/remove", "#buttons-grid-wrap",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_remove"), code),
				[]elevationHiddenField{{Name: "code", Value: code}}, elev)
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
		// ut-docs#2358: same rationale as /api/buttons/add above -- audit
		// only on an actual persisted removal, elevated case only.
		if ok := btnHTTP.Remove(w, r); ok && elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, code, "buttons_remove", map[string]any{"code": code})
		}
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
