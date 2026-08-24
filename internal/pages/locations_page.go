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

// registerLocations wires the stock-locations admin page (universaltill/ut-docs#49).
// Manager/admin only; a location with any inventory, stock movement, or
// register history can't be deactivated (StockLocationInUse guard).
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
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "stock_location", targetID, action, nil, now, "")
	}

	renderLocations := func(w http.ResponseWriter, r *http.Request, errKey string) {
		locs, err := posRepo.ListStockLocationsForAdmin(r.Context())
		if err != nil {
			http.Error(w, "failed to load locations", http.StatusInternalServerError)
			return
		}
		httpx.Render("ui/pages/locations.html", map[string]any{
			"title":     "Locations",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"locations": locs,
			"errKey":    errKey,
		})(w, r)
	}

	mux.HandleFunc("GET /locations", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderLocations(w, r, r.URL.Query().Get("err"))
	})

	mux.HandleFunc("POST /api/locations", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			http.Redirect(w, r, "/locations?err=locations.error.required", http.StatusSeeOther)
			return
		}
		id, err := posRepo.CreateStockLocation(r.Context(), name)
		if err != nil {
			http.Redirect(w, r, "/locations?err=locations.error.create", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "stock_location_create")
		http.Redirect(w, r, "/locations", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/locations/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			http.Redirect(w, r, "/locations?err=locations.error.required", http.StatusSeeOther)
			return
		}
		if err := posRepo.RenameStockLocation(r.Context(), id, name); err != nil {
			http.Redirect(w, r, "/locations?err=locations.error.rename", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "stock_location_rename")
		http.Redirect(w, r, "/locations", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/locations/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
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
				http.Redirect(w, r, "/locations?err=locations.error.in_use", http.StatusSeeOther)
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
				http.Redirect(w, r, "/locations?err=locations.error.last_location", http.StatusSeeOther)
				return
			}
		}
		if err := posRepo.SetStockLocationActive(r.Context(), id, activate); err != nil {
			http.Redirect(w, r, "/locations?err=locations.error.update", http.StatusSeeOther)
			return
		}
		action := "stock_location_deactivate"
		if activate {
			action = "stock_location_activate"
		}
		audit(r, actor.ID, id, action)
		http.Redirect(w, r, "/locations", http.StatusSeeOther)
	})
}
