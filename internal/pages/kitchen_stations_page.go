package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// stationCheck is one station checkbox: checked when the row (category or
// item) is currently routed to it.
type stationCheck struct {
	ID      string
	Name    string
	Checked bool
}

// registerKitchenStations wires the kitchen-stations admin page
// (universaltill/ut-docs#516): CRUD for prep stations (each with its own
// printer), a category × station routing matrix (the primary mechanism —
// "nobody configures 229 items by hand"), and per-item overrides found via
// search. Manager/admin only, modelled on registerLocations. Stations are
// soft-disabled, never deleted. This slice only creates 'printer' stations;
// the 'display' destination is the KDS follow-up (ut-docs#544).
func registerKitchenStations(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	catRepo := data.NewCatalogRepo(d.Db)

	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		u, ok := auth.FromContext(r.Context())
		if !ok || !u.IsManager() {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		return u, true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "kitchen_station", targetID, action, nil, now, "")
	}

	checksFor := func(stations []data.KitchenStation, routed []string) []stationCheck {
		set := map[string]bool{}
		for _, id := range routed {
			set[id] = true
		}
		out := make([]stationCheck, 0, len(stations))
		for _, s := range stations {
			out = append(out, stationCheck{ID: s.ID, Name: s.Name, Checked: set[s.ID]})
		}
		return out
	}

	renderPage := func(w http.ResponseWriter, r *http.Request, errKey string) {
		ctx := r.Context()
		stations, err := posRepo.ListKitchenStations(ctx)
		if err != nil {
			http.Error(w, "failed to load kitchen stations", http.StatusInternalServerError)
			return
		}
		categories, err := catRepo.ListCategories(ctx)
		if err != nil {
			http.Error(w, "failed to load categories", http.StatusInternalServerError)
			return
		}
		catRoutes, err := posRepo.AllCategoryStationRoutes(ctx)
		if err != nil {
			http.Error(w, "failed to load category routing", http.StatusInternalServerError)
			return
		}
		overrides, err := posRepo.ListItemStationOverrides(ctx)
		if err != nil {
			http.Error(w, "failed to load item overrides", http.StatusInternalServerError)
			return
		}

		type categoryRow struct {
			ID     string
			Name   string
			Checks []stationCheck
		}
		catRows := make([]categoryRow, 0, len(categories))
		for _, c := range categories {
			catRows = append(catRows, categoryRow{ID: c.ID, Name: c.Name, Checks: checksFor(stations, catRoutes[c.ID])})
		}

		type itemRow struct {
			ItemID string
			Name   string
			SKU    string
			Checks []stationCheck
		}
		overrideRows := make([]itemRow, 0, len(overrides))
		for _, o := range overrides {
			overrideRows = append(overrideRows, itemRow{ItemID: o.ItemID, Name: o.Name, SKU: o.SKU, Checks: checksFor(stations, o.StationIDs)})
		}

		// Item search (GET ?q=…): server-rendered results with station
		// checkboxes so a new override is one form-post away. Items already
		// overridden are edited in the overrides list above instead.
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		var searchRows []itemRow
		if q != "" {
			overridden := map[string]bool{}
			for _, o := range overrides {
				overridden[o.ItemID] = true
			}
			found, err := posRepo.SearchActiveItems(ctx, q, 0, 20)
			if err != nil {
				http.Error(w, "failed to search items", http.StatusInternalServerError)
				return
			}
			for _, it := range found {
				if overridden[it.ID] {
					continue
				}
				searchRows = append(searchRows, itemRow{ItemID: it.ID, Name: it.Name, SKU: it.SKU, Checks: checksFor(stations, nil)})
			}
		}

		httpx.Render("ui/pages/kitchen_stations.html", map[string]any{
			"title":         "Kitchen stations",
			"theme":         d.CurrentState().Theme,
			"menuItems":     d.MenuSnapshot(),
			"stations":      stations,
			"categories":    catRows,
			"overrides":     overrideRows,
			"searchQuery":   q,
			"searchResults": searchRows,
			"errKey":        errKey,
		})(w, r)
	}

	mux.HandleFunc("GET /kitchen-stations", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderPage(w, r, r.URL.Query().Get("err"))
	})

	mux.HandleFunc("POST /api/kitchen-stations", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		name := strings.TrimSpace(r.PostFormValue("name"))
		address := strings.TrimSpace(r.PostFormValue("printer_address"))
		if name == "" {
			http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.required", http.StatusSeeOther)
			return
		}
		// A station with no address would silently swallow every line routed
		// to it (code review, ut-docs#516): TransportForAddress("") returns a
		// nil transport with no error, which reads as "unconfigured" further
		// down the pipeline rather than failing loud here at creation time.
		if address == "" {
			http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.address_required", http.StatusSeeOther)
			return
		}
		id, err := posRepo.CreateKitchenStation(r.Context(), name, address)
		if err != nil {
			http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.create", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "kitchen_station_create")
		http.Redirect(w, r, "/kitchen-stations", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/kitchen-stations/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		name := strings.TrimSpace(r.PostFormValue("name"))
		address := strings.TrimSpace(r.PostFormValue("printer_address"))
		if name == "" {
			http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.required", http.StatusSeeOther)
			return
		}
		if address == "" {
			http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.address_required", http.StatusSeeOther)
			return
		}
		if err := posRepo.UpdateKitchenStation(r.Context(), id, name, address); err != nil {
			key := "kitchenstations.error.update"
			if strings.Contains(err.Error(), "not found") {
				key = "kitchenstations.error.not_found"
			}
			http.Redirect(w, r, "/kitchen-stations?err="+key, http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "kitchen_station_update")
		http.Redirect(w, r, "/kitchen-stations", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/kitchen-stations/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		enable := r.PostFormValue("active") == "1"
		if err := posRepo.SetKitchenStationEnabled(r.Context(), id, enable); err != nil {
			key := "kitchenstations.error.update"
			if strings.Contains(err.Error(), "not found") {
				key = "kitchenstations.error.not_found"
			}
			http.Redirect(w, r, "/kitchen-stations?err="+key, http.StatusSeeOther)
			return
		}
		action := "kitchen_station_deactivate"
		if enable {
			action = "kitchen_station_activate"
		}
		audit(r, actor.ID, id, action)
		http.Redirect(w, r, "/kitchen-stations", http.StatusSeeOther)
	})

	// Replace-all routing writes: the form posts every ticked station_id;
	// none ticked (or the explicit remove button) clears the rule.
	setRoutes := func(w http.ResponseWriter, r *http.Request, set func(context.Context, string, []string) error, targetID, action, actorID string) {
		_ = r.ParseForm()
		stationIDs := r.PostForm["station_id"]
		if err := set(r.Context(), targetID, stationIDs); err != nil {
			http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.routes", http.StatusSeeOther)
			return
		}
		audit(r, actorID, targetID, action)
		http.Redirect(w, r, "/kitchen-stations", http.StatusSeeOther)
	}

	// Nested under routes/ (not /api/kitchen-stations/categories/{id}) —
	// a 4-segment literal here is ambiguous with "{id}/active" above and
	// panics ServeMux registration.
	mux.HandleFunc("POST /api/kitchen-stations/routes/categories/{categoryID}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		setRoutes(w, r, posRepo.SetCategoryStationRoutes, r.PathValue("categoryID"), "kitchen_station_category_routes", actor.ID)
	})

	mux.HandleFunc("POST /api/kitchen-stations/routes/items/{itemID}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		setRoutes(w, r, posRepo.SetItemStationRoutes, r.PathValue("itemID"), "kitchen_station_item_routes", actor.ID)
	})
}
