package pages

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerDesignerCategoriesAPI (ut-docs#2174) wires the Quick Buttons
// Designer's own category-management routes: create, rename/recolour,
// deactivate/reactivate and reorder — the affordances the Designer's live
// replica of the sale-screen product panel (GET /ui/buttons?mode=edit,
// buttons.html's "designer-categories" section) posts to.
//
// Each route is a THIN wrapper over the CatalogRepo methods
// /api/categories/* (categories_page.go) already call —
// CreateCategoryWithColor / UpdateCategory / SetCategoryActive /
// SetCategorySortOrder — with no SQL of its own. It exists as a separate
// route family rather than reusing /api/categories/* for two deliberate
// reasons (BA+Architect, ut-docs#2174):
//
//  1. Gate. Everything under /designer gates on canPerform(d, r,
//     "catalog_management") — the same action designer_page.go and
//     /api/buttons/* gate on (ut-docs#2357/#2312) — where /categories gates
//     on "settings". One page = one permission model; a manager who can
//     reach the Designer must be able to use every control on it. A plain
//     403 (no elevation prompt): the page itself is unreachable without the
//     permission, so an elevation dialog here would only ever be reachable
//     by a hand-crafted request.
//  2. Response shape. /api/categories/* answers with an HX-Redirect back to
//     the full /categories page (record-dialog flow). The Designer never
//     navigates: on success every route here answers 204 with
//     `HX-Trigger: buttons-changed`, the same header /api/buttons/{add,
//     remove,reorder} set, so the ONE listener buttons.html's root already
//     has re-fetches the live preview in place (no new refresh pattern).
//     On failure it answers a non-2xx localized text/html fragment that
//     app.js's global htmx:beforeSwap force-swaps into the form's own
//     hx-target (the row's aria-live message), same mechanism
//     categories_page.go's renderCategoryDialogError relies on.
func registerDesignerCategoriesAPI(mux *http.ServeMux, d *common.Deps) {
	catRepo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// fail answers a refused mutation: a localized, escaped, text/html
	// fragment (never a raw Go/SQL error — ut-docs#316) so the client can
	// swap it inline. count is only read by the one %d key
	// (categories.error.deactivate_blocked).
	fail := func(w http.ResponseWriter, r *http.Request, status int, key string, count int) {
		msg := httpx.T(httpx.ResolveLocale(w, r), key)
		if key == "categories.error.deactivate_blocked" {
			msg = fmt.Sprintf(msg, count)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(msg) + `</div>`))
	}

	// ok answers a persisted mutation: nothing to swap, just the trigger the
	// live preview listens for.
	ok := func(w http.ResponseWriter) {
		w.Header().Set("HX-Trigger", "buttons-changed")
		w.WriteHeader(http.StatusNoContent)
	}

	// gate: catalog_management (see the file comment) plus the same
	// primary-till check every categories/buttons mutation makes — the
	// categories table syncs shop-wide from the primary (adminTables,
	// sync_admin_repo.go), so a write accepted on a satellite would vanish
	// on the next admin pull with no indication to the manager.
	gate := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "catalog_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		if d.SyncPrimaryURL(r.Context()) != "" {
			fail(w, r, http.StatusConflict, "categories.error.replica_use_primary", 0)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "category", targetID, action, nil, now, "")
	}

	// parseNameColor validates the two fields every create/update form
	// carries: a non-blank name and a colour from the SAME fixed palette
	// the item editor and /categories use (catalogtypes.ValidItemColor — an
	// allowlist, not a format check, because the value lands in a CSS custom
	// property on the sale screen's category tab). Empty colour = no colour.
	parseNameColor := func(r *http.Request) (name, color, errKey string) {
		_ = r.ParseForm()
		name = strings.TrimSpace(r.PostFormValue("name"))
		color = strings.TrimSpace(r.PostFormValue("color"))
		if name == "" {
			return name, color, "categories.error.name_required"
		}
		if !catalogtypes.ValidItemColor(color) {
			return name, color, "categories.error.color_invalid"
		}
		return name, color, ""
	}

	mux.HandleFunc("POST /api/designer/categories", func(w http.ResponseWriter, r *http.Request) {
		actor, allowed := gate(w, r)
		if !allowed {
			return
		}
		name, color, errKey := parseNameColor(r)
		if errKey != "" {
			fail(w, r, http.StatusBadRequest, errKey, 0)
			return
		}
		id, err := catRepo.CreateCategoryWithColor(r.Context(), name, color)
		if err != nil {
			if err == data.ErrCategoryNameRequired {
				fail(w, r, http.StatusBadRequest, "categories.error.name_required", 0)
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "categories.error.create", "designer_categories", err)
			return
		}
		audit(r, actor.ID, id, "category_create")
		ok(w)
	})

	// Reorder is registered BEFORE the {id} route only for readability —
	// Go 1.22's mux picks the more specific literal pattern regardless of
	// registration order (same pair categories_page.go registers). Body
	// shape mirrors /api/categories/reorder and /api/buttons/reorder:
	// repeated "ids" in display order, or one comma-joined field (what the
	// Designer's own page script posts via htmx.ajax).
	mux.HandleFunc("POST /api/designer/categories/reorder", func(w http.ResponseWriter, r *http.Request) {
		actor, allowed := gate(w, r)
		if !allowed {
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
		} else {
			_ = r.ParseForm()
		}
		ids := r.Form["ids"]
		if len(ids) == 1 && strings.Contains(ids[0], ",") {
			ids = strings.Split(ids[0], ",")
		}
		clean := ids[:0]
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				clean = append(clean, id)
			}
		}
		if len(clean) == 0 {
			fail(w, r, http.StatusBadRequest, "categories.error.update", 0)
			return
		}
		if err := catRepo.SetCategorySortOrder(r.Context(), clean); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "categories.error.update", "designer_categories_reorder", err)
			return
		}
		audit(r, actor.ID, strings.Join(clean, ","), "category_reorder")
		ok(w)
	})

	mux.HandleFunc("POST /api/designer/categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, allowed := gate(w, r)
		if !allowed {
			return
		}
		id := r.PathValue("id")
		name, color, errKey := parseNameColor(r)
		if errKey != "" {
			fail(w, r, http.StatusBadRequest, errKey, 0)
			return
		}
		if err := catRepo.UpdateCategory(r.Context(), id, name, color); err != nil {
			switch err {
			case data.ErrCategoryNameRequired:
				fail(w, r, http.StatusBadRequest, "categories.error.name_required", 0)
			case data.ErrCategoryNotFound:
				fail(w, r, http.StatusNotFound, "categories.error.not_found", 0)
			default:
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "categories.error.rename", "designer_categories", err)
			}
			return
		}
		audit(r, actor.ID, id, "category_update")
		ok(w)
	})

	mux.HandleFunc("POST /api/designer/categories/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, allowed := gate(w, r)
		if !allowed {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		enable := r.PostFormValue("active") == "1"
		if err := catRepo.SetCategoryActive(r.Context(), id, enable); err != nil {
			// The blocked-deactivate case is the card's explicit "don't
			// silently block" requirement: the operator sees the count of
			// active items still in the category, inline, and no row changes.
			// 409, not 400: the form was well-formed, the category's current
			// state is what refuses it.
			if hasItems, isBlocked := err.(*data.ErrCategoryHasItems); isBlocked {
				fail(w, r, http.StatusConflict, "categories.error.deactivate_blocked", hasItems.Count)
				return
			}
			if err == data.ErrCategoryNotFound {
				fail(w, r, http.StatusNotFound, "categories.error.not_found", 0)
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "categories.error.update", "designer_categories", err)
			return
		}
		action := "category_deactivate"
		if enable {
			action = "category_activate"
		}
		audit(r, actor.ID, id, action)
		ok(w)
	})
}
