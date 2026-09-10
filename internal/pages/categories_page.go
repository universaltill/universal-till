package pages

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/itemsnav"
)

// isHtmxDialogRequest reports whether r came from the record dialog's own
// hx-boosted forms (ut-docs#2020) rather than a plain browser submission.
// Local to this file rather than reused from httpx.IsFragmentSwap: that
// helper's own doc comment scopes it to GET-navigation fragment swaps
// (ut-docs#1950's /items rail); the check itself (HX-Request minus a
// history-restore, which a POST never carries anyway) is identical, but
// naming this separately keeps a POST-mutation reader from wondering why a
// GET-fragment-swap helper is here.
func isHtmxDialogRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("HX-Request"), "true")
}

// redirectCategories answers a categories mutation's SUCCESS with a
// navigation back to GET /categories.
//
// Before ut-docs#2020, every mutation form was a plain `<form
// method="post">` with no hx-post/hx-boost, so a bare 303 caused a real
// full-page browser navigation and htmx never touched it (an earlier draft
// of THIS card once added an "HX-Request"-conditional HX-Redirect here in
// anticipation of the forms one day going htmx; independent review at the
// time found that reasoning backwards for a plain form and it was
// reverted). The forms actually are hx-boosted now — a bare 303 to an
// hx-boosted submit is followed *transparently by the browser's fetch/XHR
// layer*, and its final response body (the whole /categories page) would
// land wherever the form's hx-target points, not as a real navigation.
// HX-Redirect is htmx's own signal for "do a real browser navigation to
// this URL" instead: it bypasses swap logic entirely, restoring the exact
// pre-#2020 UX. A non-htmx caller (should not exist once the templates are
// converted, but kept as a safety net — same shape as
// renderCategoryDialogError's fallback below) gets the plain redirect.
func redirectCategories(w http.ResponseWriter, r *http.Request, target string) {
	if isHtmxDialogRequest(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// renderCategoryDialogError answers a REFUSED categories mutation. Before
// ut-docs#2020 every refusal redirected to /categories?err=... same as a
// success, and by the time the page re-rendered the dialog was gone —
// closed by the redirect — and whatever the operator had typed with it.
//
// The dialog's forms now hx-boost (ut-docs/reference/
// list-and-dialog-pattern.md), targeting "#category-dialog-msg" — the
// dialog's own aria-live region — with hx-swap="innerHTML", so this
// renders just the translated TEXT that goes inside it
// (record_dialog_msg.html), never the region element itself (an
// outerHTML swap would replace that node, and a live region generally
// must stay the same node across a content change for assistive tech to
// announce it). The dialog and the form the operator is typing in are
// never mentioned in this response at all, so an innerHTML swap of the
// message region cannot touch either. Answered as a non-2xx status
// specifically because app.js's global htmx:beforeSwap handler
// force-swaps a non-2xx response when it is real, non-empty text/html —
// and marks it NOT an error, so the page-wide #pos-alert-style banner does
// not also fire for the same failure (list-and-dialog-pattern.md's "one
// error, one place" rule / this card's AC4). A failure this can't answer
// at all — no response reached the browser, or a plain-text 403/500 that
// beforeSwap does not force-swap — is record-dialog.js's
// dialogFailureFallback's job instead, via the SAME element.
//
// A non-htmx caller (should not exist once the templates are converted,
// but a page route must still answer *something* sane to a direct POST —
// e.g. a bookmarked/curl'd request, or a future regression in the
// template) falls back to the pre-#2020 redirect-with-query-string shape,
// unchanged: this is exactly what every existing Go-level test in this
// file (which posts with no HX-Request header) already exercises.
func renderCategoryDialogError(w http.ResponseWriter, r *http.Request, errKey string, errCount int) {
	if !isHtmxDialogRequest(r) {
		// &count= only when it's the one key that reads it (the %d
		// placeholder in categories.error.deactivate_blocked) — matches the
		// exact pre-#2020 query shape byte for byte, which the existing
		// Go-level tests in this file already pin.
		target := "/categories?err=" + errKey
		if errKey == "categories.error.deactivate_blocked" {
			target = fmt.Sprintf("%s&count=%d", target, errCount)
		}
		redirectCategories(w, r, target)
		return
	}
	msg := httpx.T(httpx.RequestLocale(r), errKey)
	if errKey == "categories.error.deactivate_blocked" {
		msg = fmt.Sprintf(msg, errCount)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	httpx.RenderPartial("ui/partials/record_dialog_msg.html", map[string]any{"msg": msg})(w, r)
}

// registerCategories wires the categories admin page (ut-docs#1898):
// create/rename/deactivate/reorder for the flat category list. Categories
// exist in the schema and are read in several places (the sale-screen tab
// bar, the catalog item-edit <select>, kitchen-station routing) but had no
// management screen anywhere — this is that screen. Manager/admin only,
// modelled most closely on tables_page.go (which has both gating variants
// this needs: a bare-body 403 for the API/fragment routes, a full RenderError
// page for the page route itself, ut-docs#1455). Flat list only — no
// nested/tree UI, no parent_id editing (out of scope, see the card's
// non-goals); move-up/move-down reorder buttons, not drag-and-drop
// (ut-docs#1221, same reasoning as buttons_admin.html's reorder grid).
// Categories are soft-disabled, never deleted from this UI (hard delete
// stays reachable only through the primary-side sync-prune path).
func registerCategories(mux *http.ServeMux, d *common.Deps) {
	catRepo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the "settings" action, same as
	// tables_page.go/kitchen_stations_page.go — no narrower catalog-admin
	// action exists yet, and adding one would need its own seed-data
	// migration (out of scope for this card).
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// requirePageManager is requireManager's gate for the /categories page
	// route itself, answered as a full RenderError page instead of a bare
	// LocalizedError body (ut-docs#1455: a page route must not fall back to
	// a bare, rail-less 403). requireManager stays as-is for the
	// /api/categories/* mutation routes below — a bare, plain-text 403
	// LocalizedError body, which the dialog's own forms being hx-boosted
	// now (ut-docs#2020) doesn't change: app.js's htmx:beforeSwap only
	// force-swaps a text/html body, so this one instead reaches
	// record-dialog.js's dialogFailureFallback and surfaces as the
	// generic "something went wrong" dialog message, not the specific
	// translated reason renderCategoryDialogError's own responses carry.
	requirePageManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// requirePrimary gates the dialog's own mutations (create/rename/active,
	// hx-boosted since ut-docs#2020 — not plain forms any more) on this
	// till being the primary (ut-docs#1585 family): categories syncs
	// shop-wide as an admin table (adminTables, sync_admin_repo.go) via a
	// one-way primary-wins pull, so a write accepted on a satellite would
	// silently vanish on the very next admin pull -- refuse it up front
	// instead, same pattern as tables_page.go/locations_page.go's
	// requirePrimary.
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			renderCategoryDialogError(w, r, "categories.error.replica_use_primary", 0)
			return false
		}
		return true
	}

	// requirePrimaryFetch is requirePrimary's counterpart for the reorder
	// route below, which is JS fetch-driven (categories.html's own script),
	// not a plain form post: a redirect there would just be followed
	// silently by fetch and read back as a 200 "success", hiding the refusal
	// from the reorder JS's res.ok check. Answers with a status + localized
	// body instead, same shape as buttons_api.go's own reorder endpoint gate
	// (409, so the client can tell "you're on a replica" apart from a
	// generic failure).
	requirePrimaryFetch := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			common.LocalizedError(w, r, http.StatusConflict, "categories.error.replica_use_primary")
			return false
		}
		return true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "category", targetID, action, nil, now, "")
	}

	renderCategories := func(w http.ResponseWriter, r *http.Request, errKey string, errCount int) {
		rows, err := catRepo.ListCategoriesForAdmin(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		categoriesData := map[string]any{
			"title":      "Categories",
			"theme":      d.CurrentState().Theme,
			"menuItems":  d.MenuSnapshot(),
			"categories": rows,
			"errKey":     errKey,
			"errCount":   errCount,
		}
		// ut-docs#1950: /categories is one of the /items rail's five section
		// destinations — an htmx request from that panel (NOT a stale history
		// restore, see httpx.IsFragmentSwap) gets just the "content" block
		// plus an out-of-band refresh of the rail so its is-current highlight
		// follows the click; a plain browser GET (deep link, or the redirect
		// a mutation falls back to) still gets the exact same full standalone
		// page as before this card.
		if httpx.IsFragmentSwap(r) {
			httpx.RenderContentFragment("ui/pages/categories.html", categoriesData)(w, r)
			itemsnav.WriteRailOOB(w, r, httpx.FuncsFor(httpx.RequestLocale(r)), "/categories")
			return
		}
		httpx.Render("ui/pages/categories.html", categoriesData)(w, r)
	}

	mux.HandleFunc("GET /categories", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requirePageManager(w, r); !ok {
			return
		}
		// count only matters alongside categories.error.deactivate_blocked
		// (the %d placeholder it interpolates); absent/unparseable elsewhere
		// just renders as 0, which the template never reads for any other key.
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		renderCategories(w, r, r.URL.Query().Get("err"), count)
	})

	mux.HandleFunc("POST /api/categories", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		_ = r.ParseForm()
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			renderCategoryDialogError(w, r, "categories.error.name_required", 0)
			return
		}
		id, err := catRepo.CreateCategory(r.Context(), name)
		if err != nil {
			key := "categories.error.create"
			if err == data.ErrCategoryNameRequired {
				key = "categories.error.name_required"
			}
			renderCategoryDialogError(w, r, key, 0)
			return
		}
		audit(r, actor.ID, id, "category_create")
		redirectCategories(w, r, "/categories")
	})

	mux.HandleFunc("POST /api/categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			renderCategoryDialogError(w, r, "categories.error.name_required", 0)
			return
		}
		if err := catRepo.RenameCategory(r.Context(), id, name); err != nil {
			key := "categories.error.rename"
			if err == data.ErrCategoryNameRequired {
				key = "categories.error.name_required"
			}
			renderCategoryDialogError(w, r, key, 0)
			return
		}
		audit(r, actor.ID, id, "category_rename")
		redirectCategories(w, r, "/categories")
	})

	mux.HandleFunc("POST /api/categories/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		enable := r.PostFormValue("active") == "1"
		if err := catRepo.SetCategoryActive(r.Context(), id, enable); err != nil {
			// The blocked-deactivate case carries the active item count so
			// renderCategoryDialogError can interpolate it into
			// categories.error.deactivate_blocked's %d placeholder (same
			// printf convention tables.html's tables.status.open_minutes
			// already uses) — the operator sees WHY the deactivate was
			// refused, not just "failed", and no row is changed.
			if hasItems, ok := err.(*data.ErrCategoryHasItems); ok {
				renderCategoryDialogError(w, r, "categories.error.deactivate_blocked", hasItems.Count)
				return
			}
			key := "categories.error.update"
			if err == data.ErrCategoryNotFound {
				key = "categories.error.not_found"
			}
			renderCategoryDialogError(w, r, key, 0)
			return
		}
		action := "category_deactivate"
		if enable {
			action = "category_activate"
		}
		audit(r, actor.ID, id, action)
		redirectCategories(w, r, "/categories")
	})

	// Reorder from move-up/move-down buttons (ut-docs#1221's pattern, see
	// categories.html's own script) — mirrors buttons_api.go's
	// POST /api/buttons/reorder body shape: repeated "ids" form values in
	// display order, or a single comma-joined field as a fallback.
	mux.HandleFunc("POST /api/categories/reorder", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimaryFetch(w, r) {
			return
		}
		// categories.html's reorder script posts FormData (multipart) —
		// ParseForm alone ignores multipart bodies, so every real-browser
		// reorder answered `400 ids required` while the urlencoded test
		// passed (ut-docs#2018). Same guard as buttons_api.go's reorder.
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
		} else {
			_ = r.ParseForm()
		}
		ids := r.Form["ids"]
		if len(ids) == 1 && strings.Contains(ids[0], ",") {
			ids = strings.Split(ids[0], ",")
		}
		if len(ids) == 0 {
			http.Error(w, "ids required", http.StatusBadRequest)
			return
		}
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
		}
		if err := catRepo.SetCategorySortOrder(r.Context(), ids); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "categories.error.update", "categories_reorder", err)
			return
		}
		audit(r, actor.ID, strings.Join(ids, ","), "category_reorder")
		w.WriteHeader(http.StatusNoContent)
	})
}
