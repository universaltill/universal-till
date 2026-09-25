package pages

import (
	"context"
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

// buttonsElevationItemName resolves itemID to the catalog item's own NAME
// for the hide/unhide/delete-item elevation prompts (ut-docs#2541 review
// finding 3): a manager approving a PIN prompt needs to see what they're
// actually approving, not a bare UUID. Falls back to itemID itself when the
// lookup fails (an unknown/stale id) or errors, same "never blank/fail the
// whole prompt over an optional nicety" shape every other best-effort
// lookup in this file already has.
func buttonsElevationItemName(ctx context.Context, d *common.Deps, itemID string) string {
	item, ok, err := data.NewCatalogRepo(d.Db).GetItem(ctx, itemID)
	if err != nil || !ok || strings.TrimSpace(item.Name) == "" {
		return itemID
	}
	return item.Name
}

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
		// ut-docs#2174: ?mode=edit is the Quick Buttons Designer's live
		// replica of this very fragment (designer.html's placeholder sends
		// it via hx-vals, so its hx-get stays exactly "/ui/buttons" — the
		// literal app.js's utTileJiggle unsaved-drag guard matches). It
		// renders category-management controls, so it's gated on the same
		// catalog_management the Designer page itself is (designer_page.go,
		// ut-docs#2357): a cashier fetching it by hand gets a plain 403, the
		// sale screen's own render is untouched. Edit mode always renders
		// the quick-button strip (ui.ButtonsHTTP.List), which has no All
		// tab since ut-docs#2613.
		editMode := r.URL.Query().Get("mode") == "edit"
		if editMode && !granted {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		// ut-docs#2499: the clamped live browsing mode — ClampBrowsingMode
		// here rather than trusting RuntimeState verbatim, because a bare
		// Deps (tests, helpers) carries the zero value "" and the sell
		// screen must still render the real default, not a fourth shape.
		btnHTTP := &ui.ButtonsHTTP{
			Store:        *d.BtnStore,
			View:         renderer,
			BrowsingMode: common.ClampBrowsingMode(d.CurrentState().BrowsingMode),
			Granted:      granted,
			EditMode:     editMode,
		}
		btnHTTP.List(w, r)
	})

	// ut-docs#2499 (absorbing ut-docs#2372): the category-tiles mode's popup
	// body — EVERY active item in one category (its subtree included), quick
	// buttons first in their Designer order then the rest A–Z, plus the
	// popup's own search box. Rendered on open (an htmx GET from the tile),
	// never a second always-present copy of the tiles in the DOM — #2372's
	// own strict-mode-locator requirement, and the reason the ut-docs#2283
	// clone-the-panel approach was retired with the Categories tab itself.
	mux.HandleFunc("/ui/buttons/category", func(w http.ResponseWriter, r *http.Request) {
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
		btnHTTP.CategoryItems(w, r)
	})

	// Sell screen All-grid "load more" (ut-docs#2319; since ut-docs#2613
	// only the all_filter_chips mode has an All grid): the next page of
	// ButtonStore.LoadAllActive beyond the first AllTabPageSize items GET
	// /ui/buttons itself already inlined — see ui.ButtonsHTTP.AllMore's own
	// doc comment. Mirrors /api/buttons/search's offset-query-param shape
	// below, applied to the sale screen's own grid instead of the
	// Designer's shortcut search.
	mux.HandleFunc("/ui/buttons/all/more", func(w http.ResponseWriter, r *http.Request) {
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
		btnHTTP.AllMore(w, r)
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
			// ut-docs#2174: the retry target is the Designer's own
			// #buttons-add-error region (where its search-result button
			// lands this route's response too) — the retired flat admin
			// grid's #buttons-grid-wrap wrapper no longer exists.
			renderElevationPrompt(w, r, "/api/buttons/add", "#buttons-add-error",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_add"), label),
				[]elevationHiddenField{
					{Name: "label", Value: label},
					{Name: "code", Value: code},
					{Name: "itemId", Value: itemID},
					{Name: "imageUrl", Value: imageURL},
				}, elev)
			return
		}
		// ut-docs#2174: no renderer — Add answers an empty 200 + HX-Trigger
		// now (see ui.ButtonsHTTP.Add), the Designer's live replica
		// re-fetches itself off that header.
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore}
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
		// code/itemId to mirror as hidden fields on the dialog's retry.
		//
		// ut-docs#2541: this route now HIDES the item (ui.ButtonStore.Remove)
		// rather than just deleting its shortcut_buttons row -- every active
		// item is a quick button by default now, so a plain row delete would
		// let the tile silently reappear. Kept at this same path (not
		// retired in favour of /api/buttons/hide below) because
		// buttons_admin.html's legacy search flow and any other caller that
		// only has a code, not an itemId, still needs a route that resolves
		// one from the other.
		//
		// This route is reached from the Designer's live replica of the
		// sale screen (GET /ui/buttons?mode=edit, ut-docs#2174) -- the sale
		// screen's own jiggle-mode trash badge posts to
		// /api/buttons/delete-item instead (ut-docs#2541). The elevation
		// retry target below is "#buttons-add-error", which exists on the
		// Designer; managers (the only operators who reach it) never see
		// the prompt at all.
		_ = r.ParseForm()
		code := r.Form.Get("code")
		itemID := r.Form.Get("itemId")
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/remove", "#buttons-add-error",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_remove"), code),
				[]elevationHiddenField{{Name: "code", Value: code}, {Name: "itemId", Value: itemID}}, elev)
			return
		}
		// ut-docs#2174: no renderer -- same as /api/buttons/add above.
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore}
		// ut-docs#2358: same rationale as /api/buttons/add above -- audit
		// only on an actual persisted hide, elevated case only.
		target := itemID
		if target == "" {
			target = code
		}
		if ok := btnHTTP.Remove(w, r); ok && elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, target, "buttons_remove", map[string]any{"code": code, "item_id": itemID})
		}
	})

	// Hide an item from the sell screen (ut-docs#2541) -- same
	// gating/elevation/audit pattern as /api/buttons/remove above, itemId
	// only (no code to resolve from).
	mux.HandleFunc("/api/buttons/hide", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r) {
			return
		}
		_ = r.ParseForm()
		itemID := r.Form.Get("itemId")
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/hide", "#buttons-add-error",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_hide"), buttonsElevationItemName(r.Context(), d, itemID)),
				[]elevationHiddenField{{Name: "itemId", Value: itemID}}, elev)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore}
		if ok := btnHTTP.Hide(w, r); ok && elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, itemID, "buttons_hide", map[string]any{"item_id": itemID})
		}
	})

	// Unhide an item, restoring it to the sell screen as an implicit quick
	// button (ut-docs#2541) -- same pattern as hide above. Also reachable
	// from the Designer's own search->add flow (ui.ButtonStore.Add clears
	// the flag), so this route is mainly the "Hidden from sell screen"
	// section's own Unhide button.
	mux.HandleFunc("/api/buttons/unhide", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r) {
			return
		}
		_ = r.ParseForm()
		itemID := r.Form.Get("itemId")
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/unhide", "#buttons-add-error",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_unhide"), buttonsElevationItemName(r.Context(), d, itemID)),
				[]elevationHiddenField{{Name: "itemId", Value: itemID}}, elev)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore}
		if ok := btnHTTP.Unhide(w, r); ok && elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, itemID, "buttons_unhide", map[string]any{"item_id": itemID})
		}
	})

	// Unhide every hidden item at once -- the Designer's "Show all N on the
	// sell screen" in its "Hidden from sell screen" section (ut-docs#2614).
	// Same gating/elevation/audit pattern as unhide above; no form input
	// (so no hidden fields to mirror on the elevation retry). POST only:
	// with nothing to validate, a GET (link prefetch, an <img src>) would
	// otherwise be a one-request shop-wide change.
	mux.HandleFunc("/api/buttons/unhide-all", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/unhide-all", "#buttons-add-error",
				httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_unhide_all"), nil, elev)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore}
		if n, ok := btnHTTP.UnhideAll(w, r); ok && elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, "all", "buttons_unhide_all", map[string]any{"count": n})
		}
	})

	// Delete the item itself from the jiggle-mode trash badge (ut-docs#2541)
	// -- same gating/elevation/audit pattern as remove/hide above, plus the
	// same underlying deactivate the catalog page's own
	// /api/catalog/item/deactivate uses (ui.ButtonStore.DeleteItem ->
	// pos.DeactivateItem).
	mux.HandleFunc("/api/buttons/delete-item", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r) {
			return
		}
		_ = r.ParseForm()
		itemID := r.Form.Get("itemId")
		elev := checkOrElevate(d, r, "catalog_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/buttons/delete-item", "#buttons-add-error",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.buttons_delete_item"), buttonsElevationItemName(r.Context(), d, itemID)),
				[]elevationHiddenField{{Name: "itemId", Value: itemID}}, elev)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.BtnStore}
		if ok := btnHTTP.DeleteItem(w, r); ok && elev.Outcome == elevated {
			auditButtonsElevated(r, elev.ApproverID, elev.ActorID, itemID, "buttons_delete_item", map[string]any{"item_id": itemID})
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
