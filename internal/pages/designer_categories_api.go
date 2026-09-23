package pages

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerDesignerCategoriesAPI (ut-docs#2174) wires the thin category
// routes behind the Designer's live sale-screen replica: rename/recolour,
// create, activate/deactivate and reorder, each a one-call wrapper around
// the CatalogRepo method /categories (categories_page.go) already uses —
// no new SQL, no new validation rules (the same catalogtypes.ValidItemColor
// palette allowlist, the same ErrCategoryHasItems blocking-count guard).
//
// Why separate routes rather than reusing /api/categories/*: those answer
// with a redirect / HX-Redirect back to the /categories page on success
// (redirectCategories), which is exactly wrong for an inline popover on a
// replica that must update in place. These answer the way the replica's
// htmx forms need — 204 + HX-Trigger: buttons-changed on success, so the
// buttons.html root (hx-trigger="buttons-changed from:body") refetches
// itself from GET /ui/designer/buttons; a translated text/html fragment
// (record_dialog_msg.html, the same partial the /categories dialog's own
// refusals use) with a 4xx status on a refusal, which app.js's global
// htmx:beforeSwap force-swaps into the popover's own aria-live message
// region. /categories' own routes, gate and dialog are untouched.
//
// Gate: catalog_management via canPerform — the same permission /designer
// and /api/buttons/{add,remove,reorder} gate on — NOT /categories'
// "settings" gate: this is the Designer's surface, so it follows the
// Designer's permission. A plain 403 (no checkOrElevate PIN prompt): the
// Designer page itself is already unreachable without the permission, so
// there is no cashier here to elevate.
func registerDesignerCategoriesAPI(mux *http.ServeMux, d *common.Deps) {
	catRepo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	requireCatalogManagement := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "catalog_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// Same replica refusal as categories_page.go's requirePrimaryFetch
	// (categories sync primary-wins; a write on a satellite would vanish on
	// the next admin pull): a 409 with the categories page's own translated
	// key — surfaced in the popover the same way any other refusal is.
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			refuseDesignerCategory(w, r, http.StatusConflict, httpx.T(httpx.RequestLocale(r), "categories.error.replica_use_primary"))
			return false
		}
		return true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "category", targetID, action, nil, now, "")
	}

	// ok answers a successful mutation: nothing to swap, just the trigger
	// that makes the replica root re-render from the now-persisted truth.
	ok := func(w http.ResponseWriter) {
		w.Header().Set("HX-Trigger", "buttons-changed")
		w.WriteHeader(http.StatusNoContent)
	}

	refuseKey := func(w http.ResponseWriter, r *http.Request, key string) {
		refuseDesignerCategory(w, r, http.StatusBadRequest, httpx.T(httpx.RequestLocale(r), key))
	}

	// parseNameColor reads the popover form's two fields with the same
	// rules as categories_page.go's parseCategoryForm (minus the
	// modifier-group/kitchen-station pickers that dialog also carries and
	// this popover deliberately does not — those stay on /categories).
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
		actor, granted := requireCatalogManagement(w, r)
		if !granted || !requirePrimary(w, r) {
			return
		}
		name, color, errKey := parseNameColor(r)
		if errKey != "" {
			refuseKey(w, r, errKey)
			return
		}
		id, err := catRepo.CreateCategoryWithColor(r.Context(), name, color)
		if err != nil {
			key := "categories.error.create"
			if err == data.ErrCategoryNameRequired {
				key = "categories.error.name_required"
			}
			refuseKey(w, r, key)
			return
		}
		audit(r, actor.ID, id, "category_create")
		ok(w)
	})

	mux.HandleFunc("POST /api/designer/categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, granted := requireCatalogManagement(w, r)
		if !granted || !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		name, color, errKey := parseNameColor(r)
		if errKey != "" {
			refuseKey(w, r, errKey)
			return
		}
		if err := catRepo.UpdateCategory(r.Context(), id, name, color); err != nil {
			key := "categories.error.rename"
			switch err {
			case data.ErrCategoryNameRequired:
				key = "categories.error.name_required"
			case data.ErrCategoryNotFound:
				key = "categories.error.not_found"
			}
			refuseKey(w, r, key)
			return
		}
		audit(r, actor.ID, id, "category_update")
		ok(w)
	})

	mux.HandleFunc("POST /api/designer/categories/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, granted := requireCatalogManagement(w, r)
		if !granted || !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		enable := r.PostFormValue("active") == "1"
		if err := catRepo.SetCategoryActive(r.Context(), id, enable); err != nil {
			// The blocked case carries the active item count: interpolated
			// into /categories' own categories.error.deactivate_blocked
			// (%d) so the popover says WHY — "N active item(s) are still in
			// this category" — with no duplicate key minted for the
			// Designer. No row is changed.
			if hasItems, blocked := err.(*data.ErrCategoryHasItems); blocked {
				msg := fmt.Sprintf(httpx.T(httpx.RequestLocale(r), "categories.error.deactivate_blocked"), hasItems.Count)
				refuseDesignerCategory(w, r, http.StatusBadRequest, msg)
				return
			}
			key := "categories.error.update"
			if err == data.ErrCategoryNotFound {
				key = "categories.error.not_found"
			}
			refuseKey(w, r, key)
			return
		}
		action := "category_deactivate"
		if enable {
			action = "category_activate"
		}
		audit(r, actor.ID, id, action)
		ok(w)
	})

	// Mirrors POST /api/categories/reorder's body shape exactly: repeated
	// "ids" form values in display order (urlencoded or multipart — the
	// replica posts FormData), or one comma-joined field as a fallback.
	mux.HandleFunc("POST /api/designer/categories/reorder", func(w http.ResponseWriter, r *http.Request) {
		actor, granted := requireCatalogManagement(w, r)
		if !granted || !requirePrimary(w, r) {
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
		if len(ids) == 0 {
			http.Error(w, "ids required", http.StatusBadRequest)
			return
		}
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
		}
		if err := catRepo.SetCategorySortOrder(r.Context(), ids); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "categories.error.update", "designer_categories_reorder", err)
			return
		}
		audit(r, actor.ID, strings.Join(ids, ","), "category_reorder")
		ok(w)
	})
}

// refuseDesignerCategory answers a refused Designer category mutation with an
// already-translated message as a text/html fragment (record_dialog_msg.html
// — just the text, no wrapper, so the popover's hx-swap="innerHTML" into
// its own aria-live region keeps that node stable for assistive tech, the
// same reasoning categories_page.go's renderCategoryDialogError gives). A
// non-2xx status on purpose: app.js's global htmx:beforeSwap force-swaps a
// real, non-empty text/html body regardless of status and marks it not an
// error, so the message lands in the popover and nowhere else.
func refuseDesignerCategory(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	httpx.RenderPartial("ui/partials/record_dialog_msg.html", map[string]any{"msg": msg})(w, r)
}
