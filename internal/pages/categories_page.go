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
)

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
	// /api/categories/* mutation routes below, which return a redirect/
	// status the JS-free forms expect.
	requirePageManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// requirePrimary gates the plain-form mutations (create/rename/active) on
	// this till being the primary (ut-docs#1585 family): categories syncs
	// shop-wide as an admin table (adminTables, sync_admin_repo.go) via a
	// one-way primary-wins pull, so a write accepted on a satellite would
	// silently vanish on the very next admin pull -- refuse it up front
	// instead, same pattern as tables_page.go/locations_page.go's
	// requirePrimary.
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Redirect(w, r, "/categories?err=categories.error.replica_use_primary", http.StatusSeeOther)
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
		httpx.Render("ui/pages/categories.html", map[string]any{
			"title":      "Categories",
			"theme":      d.CurrentState().Theme,
			"menuItems":  d.MenuSnapshot(),
			"categories": rows,
			"errKey":     errKey,
			"errCount":   errCount,
		})(w, r)
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
			http.Redirect(w, r, "/categories?err=categories.error.name_required", http.StatusSeeOther)
			return
		}
		id, err := catRepo.CreateCategory(r.Context(), name)
		if err != nil {
			key := "categories.error.create"
			if err == data.ErrCategoryNameRequired {
				key = "categories.error.name_required"
			}
			http.Redirect(w, r, "/categories?err="+key, http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "category_create")
		http.Redirect(w, r, "/categories", http.StatusSeeOther)
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
			http.Redirect(w, r, "/categories?err=categories.error.name_required", http.StatusSeeOther)
			return
		}
		if err := catRepo.RenameCategory(r.Context(), id, name); err != nil {
			key := "categories.error.rename"
			if err == data.ErrCategoryNameRequired {
				key = "categories.error.name_required"
			}
			http.Redirect(w, r, "/categories?err="+key, http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "category_rename")
		http.Redirect(w, r, "/categories", http.StatusSeeOther)
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
			// The blocked-deactivate case carries the active item count as a
			// separate query param so the page can interpolate it into
			// categories.error.deactivate_blocked's %d placeholder (same
			// printf-in-template convention tables.html's
			// tables.status.open_minutes already uses) — the operator sees
			// WHY the deactivate was refused, not just "failed", and no row
			// is changed.
			if hasItems, ok := err.(*data.ErrCategoryHasItems); ok {
				http.Redirect(w, r, fmt.Sprintf("/categories?err=categories.error.deactivate_blocked&count=%d", hasItems.Count), http.StatusSeeOther)
				return
			}
			key := "categories.error.update"
			if err == data.ErrCategoryNotFound {
				key = "categories.error.not_found"
			}
			http.Redirect(w, r, "/categories?err="+key, http.StatusSeeOther)
			return
		}
		action := "category_deactivate"
		if enable {
			action = "category_activate"
		}
		audit(r, actor.ID, id, action)
		http.Redirect(w, r, "/categories", http.StatusSeeOther)
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
		_ = r.ParseForm()
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
