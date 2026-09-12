package pages

import (
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// redirectLocations answers a locations mutation's SUCCESS with a
// navigation back to GET /locations. Mirrors categories_page.go's
// redirectCategories exactly (ut-docs#2124 adopting ut-docs#2010's pattern):
// the dialog's forms are hx-boosted, so a bare 303 would be followed by the
// boosted form's own fetch/XHR layer and land the whole /locations page's
// HTML wherever the form's hx-target points — HX-Redirect instead forces a
// real browser navigation, bypassing swap logic entirely. A non-htmx caller
// (a bookmarked/curl'd request, or a template regression) gets the plain
// redirect, matching every pre-#2124 Go-level test in this file.
func redirectLocations(w http.ResponseWriter, r *http.Request, target string) {
	if isHtmxDialogRequest(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// renderLocationsDialogError answers a REFUSED locations mutation. Mirrors
// categories_page.go's renderCategoryDialogError exactly, minus the %d
// count interpolation categories.error.deactivate_blocked needs — no
// locations.error.* key takes a placeholder. See that function's own doc
// comment for the full reasoning (why a non-2xx text/html body, why
// innerHTML-only into the dialog's own aria-live message region, why the
// non-htmx fallback preserves the pre-#2124 redirect-with-query-string
// shape byte for byte).
func renderLocationsDialogError(w http.ResponseWriter, r *http.Request, errKey string) {
	if !isHtmxDialogRequest(r) {
		redirectLocations(w, r, "/locations?err="+errKey)
		return
	}
	msg := httpx.T(httpx.RequestLocale(r), errKey)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	httpx.RenderPartial("ui/partials/record_dialog_msg.html", map[string]any{"msg": msg})(w, r)
}

// registerLocations wires the stock-locations admin page (universaltill/ut-docs#49).
// Manager/admin only; a location currently holding nonzero stock, or
// assigned to a currently-active register, can't be deactivated
// (StockLocationInUse guard) — past history alone no longer blocks it
// (universaltill/ut-docs#2066). Adopts the record_dialog/list_header
// pattern (ut-docs#2010) as of ut-docs#2124 — see redirectLocations/
// renderLocationsDialogError above.
func registerLocations(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the "stock_location_management" action
	// (ut-docs#903, migration 060) via canPerform, not a raw IsManager()
	// check on the session — matching every other admin page (#555's five
	// successor cards; see authz.go's own doc comment). Previously reused
	// the generic "settings" action (see ut-docs#901's fix, which migrated
	// this page's gate off a raw IsManager() check onto canPerform in the
	// first place); #903 gave stock-location/register administration its
	// own dedicated action instead, so a super_admin editing "settings" in
	// role_permissions (runtime-editable, permission_settings_page.go) no
	// longer moves this page's access in lockstep with every other
	// settings-gated surface. Seeded identically to "settings"
	// (manager/admin/super_admin granted) so no existing till's access
	// changes.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "stock_location_management") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "stock_location", targetID, action, nil, now, "")
	}

	// requirePrimary gates every mutation (create/rename/activate) on this
	// till being the primary (ut-docs#1590). stock_locations is now synced
	// shop-wide (sync_admin_repo.go's adminTables) via a one-way
	// primary-wins pull, so a write accepted on a satellite would silently
	// vanish (a new row deleted, an edit reverted) on the very next admin
	// pull -- refuse it up front instead, with a clear localized message,
	// same pattern as plugins_store_page.go's replica_use_primary gate.
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			renderLocationsDialogError(w, r, "locations.error.replica_use_primary")
			return false
		}
		return true
	}

	renderLocations := func(w http.ResponseWriter, r *http.Request, errKey string) {
		locs, err := posRepo.ListStockLocationsForAdmin(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		locationsData := map[string]any{
			"title":     "Locations",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"locations": locs,
			"errKey":    errKey,
		}
		// ut-docs#2116: /locations is one of the /admin tree's six
		// destinations -- an htmx request from that panel (NOT a stale
		// history restore, see httpx.IsFragmentSwap) gets just the
		// "content" block plus an out-of-band refresh of the tree so its
		// is-current highlight follows the click; a plain browser GET
		// (deep link, or the redirect a mutation falls back to) still gets
		// the exact same full standalone page as before this card.
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderContentFragment("ui/pages/locations.html", locationsData)(w, r)
			writeAdminTreeOOB(w, r, httpx.FuncsFor(httpx.RequestLocale(r)), "/locations", adminGroupsFor(visibleAdminEntries(d, r)))
			return
		}
		httpx.Render("ui/pages/locations.html", locationsData)(w, r)
	}

	mux.HandleFunc("GET /locations", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderLocations(w, r, httpx.QueryErrKey(r))
	})

	mux.HandleFunc("POST /api/locations", func(w http.ResponseWriter, r *http.Request) {
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
			renderLocationsDialogError(w, r, "locations.error.required")
			return
		}
		id, err := posRepo.CreateStockLocation(r.Context(), name)
		if err != nil {
			renderLocationsDialogError(w, r, "locations.error.create")
			return
		}
		audit(r, actor.ID, id, "stock_location_create")
		redirectLocations(w, r, "/locations")
	})

	mux.HandleFunc("POST /api/locations/{id}", func(w http.ResponseWriter, r *http.Request) {
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
			renderLocationsDialogError(w, r, "locations.error.required")
			return
		}
		if err := posRepo.RenameStockLocation(r.Context(), id, name); err != nil {
			renderLocationsDialogError(w, r, "locations.error.rename")
			return
		}
		audit(r, actor.ID, id, "stock_location_rename")
		redirectLocations(w, r, "/locations")
	})

	mux.HandleFunc("POST /api/locations/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		activate := r.PostFormValue("active") == "1"
		if !activate {
			inUse, err := posRepo.StockLocationInUse(r.Context(), id)
			if err != nil {
				http.Error(w, "failed to check location usage", http.StatusInternalServerError)
				return
			}
			if inUse {
				renderLocationsDialogError(w, r, "locations.error.in_use")
				return
			}
			// A shop must always have somewhere to receive/adjust/return stock —
			// mirrors the last-active-admin guard in users_page.go.
			activeCount, err := posRepo.CountActiveStockLocations(r.Context())
			if err != nil {
				http.Error(w, "failed to check active locations", http.StatusInternalServerError)
				return
			}
			if activeCount <= 1 {
				renderLocationsDialogError(w, r, "locations.error.last_location")
				return
			}
		}
		if err := posRepo.SetStockLocationActive(r.Context(), id, activate); err != nil {
			renderLocationsDialogError(w, r, "locations.error.update")
			return
		}
		action := "stock_location_deactivate"
		if activate {
			action = "stock_location_activate"
		}
		audit(r, actor.ID, id, action)
		redirectLocations(w, r, "/locations")
	})
}
