package pages

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// discoverPrintersTimeout bounds the LAN scan the "Discover printers"
// button runs per click — same bounded, per-click (never ambient/background)
// shape as discoverBrowseTimeout in discovery_api.go.
//
// Longer than the 4s a pure mDNS browse needed (ut-docs#1606), because the
// scan now also sweeps the till's own subnet. The budget it has to cover:
// an mDNS browse (a third of this) running concurrently with a connect-only
// pass over a /24 — 254 addresses at discovery.sweepConcurrency 64 in flight
// and a 700ms dial budget, so about 2.8s — and then an ESC/POS probe of
// whatever was found listening, which waits up to escposReadTimeout (3s)
// because cheap embedded printers connect fast and answer slowly.
//
// Cutting the scan short does not fail loudly; it silently returns fewer
// printers, which is exactly the "my printer isn't in the list" bug this card
// exists to fix. So the budget leaves real headroom rather than finishing
// just barely.
const discoverPrintersTimeout = 12 * time.Second

// discoveryBrowsePrinters is a package var over discovery.DiscoverPrinters,
// same seam-for-testability pattern as discoveryBrowse in discovery_api.go
// — lets a test substitute a fast fake instead of waiting out a real
// multi-second network scan.
var discoveryBrowsePrinters = discovery.DiscoverPrinters

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
// soft-disabled, never deleted. Each station has a destination type
// (ut-docs#544): 'printer' (a ticket), 'display' (a kitchen screen at
// /kitchen-display/{id}, kitchen_display.go) or 'both'.
func registerKitchenStations(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	catRepo := data.NewCatalogRepo(d.Db)

	// requireManager gates on the "settings" action (039's catalog) via
	// canPerform — see country_settings_page.go's identical
	// requireManager for why (ut-docs#901/#902): the old raw
	// IsManager() check never saw canPerform's UT_AUTH=off escape hatch,
	// so this page 403'd permanently under the dev/CI auth-bypass. No
	// change to gated (UT_AUTH on) behavior.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required") // page-error:allow not yet migrated, tracked in ut-docs#1458
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "kitchen_station", targetID, action, nil, now, "")
	}

	// requirePrimary gates station create/active, category routes and item
	// routes on this till being the primary (ut-docs#1585). kitchen_stations/
	// category_station_routes/item_station_routes all sync shop-wide as
	// admin tables (adminTables, ut-docs#1546) via a one-way primary-wins
	// pull, so a write accepted on a joined till would silently vanish on
	// the very next admin pull -- refuse it up front instead, with a clear
	// localized message, same pattern as registers_page.go's requirePrimary
	// (ut-docs#1590). NOT used by the station update route below: that
	// route has its own narrower carve-out, because printer_address is
	// deliberately till-local and never synced (skipCols in
	// sync_admin_repo.go) -- see the comment at that handler for why it
	// splits the write instead of reusing this gate wholesale.
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.replica_use_primary", http.StatusSeeOther)
			return false
		}
		return true
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
			http.Error(w, "failed to load kitchen stations", http.StatusInternalServerError) // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}
		categories, err := catRepo.ListCategories(ctx)
		if err != nil {
			http.Error(w, "failed to load categories", http.StatusInternalServerError) // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}
		catRoutes, err := posRepo.AllCategoryStationRoutes(ctx)
		if err != nil {
			http.Error(w, "failed to load category routing", http.StatusInternalServerError) // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}
		overrides, err := posRepo.ListItemStationOverrides(ctx)
		if err != nil {
			http.Error(w, "failed to load item overrides", http.StatusInternalServerError) // page-error:allow not yet migrated, tracked in ut-docs#1458
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
				http.Error(w, "failed to search items", http.StatusInternalServerError) // page-error:allow not yet migrated, tracked in ut-docs#1458
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

	// Discover printers (ut-docs#140, ut-docs#1606): a bounded, per-click
	// scan offering candidates for the new-station form's address field —
	// never auto-trusted or auto-wired (security-first: discovery only
	// presents, the operator still confirms by clicking a candidate and
	// submitting the create form themselves). Manager-gated, same as every
	// other route on this page.
	//
	// Two sources, one gate (see discovery.DiscoverPrinters): an mDNS browse
	// for printers that advertise themselves, plus a :9100 sweep of the
	// till's own subnet for the many cheap ESC/POS units that advertise
	// nothing at all — and every candidate from either source must answer an
	// ESC/POS status query before it is offered. Advertising as a printer is
	// not the same as being able to print a receipt: an office inkjet on
	// :9100 is a real printer that renders ESC/POS as a page of garbage.
	mux.HandleFunc("GET /api/kitchen-stations/discover-printers", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		candidates, err := discoveryBrowsePrinters(r.Context(), discoverPrintersTimeout)
		if err != nil {
			// Never put the raw driver/network error in the response (same
			// rule discovery_api.go's discoverPrimariesHandler follows,
			// ut-docs#303/#538): log it server-side, generic marker to the
			// client — the page's own "kitchenstations.discover.error"
			// i18n string is what the operator actually sees.
			log.Printf("[discovery] printer LAN scan failed: %v", err)
			http.Error(w, "discovery scan failed", http.StatusInternalServerError)
			return
		}
		if candidates == nil {
			candidates = []discovery.PrinterCandidate{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  map[string]any{"printers": candidates},
			"error": nil,
		})
	})

	// stationForm validates the shared create/edit form fields. An omitted
	// destination_type means 'printer' (the pre-#544 form had no such field,
	// and any caller still posting without it keeps its old behavior). The
	// printer address is required only when the station prints: a station
	// with no address would silently swallow every line routed to it (code
	// review, ut-docs#516: TransportForAddress("") returns a nil transport
	// with no error, which reads as "unconfigured" further down the pipeline
	// rather than failing loud here) — but a display-only station never
	// prints, so demanding an address there would just force a fake value.
	stationForm := func(r *http.Request) (name, destinationType, address, errKey string) {
		_ = r.ParseForm()
		name = strings.TrimSpace(r.PostFormValue("name"))
		destinationType = strings.TrimSpace(r.PostFormValue("destination_type"))
		address = strings.TrimSpace(r.PostFormValue("printer_address"))
		if destinationType == "" {
			destinationType = data.KitchenDestinationPrinter
		}
		switch {
		case name == "":
			errKey = "kitchenstations.error.required"
		case !data.ValidKitchenDestinationType(destinationType):
			errKey = "kitchenstations.error.destination_invalid"
		case address == "" && (data.KitchenStation{DestinationType: destinationType}).PrintsTickets():
			errKey = "kitchenstations.error.address_required"
		}
		return name, destinationType, address, errKey
	}

	mux.HandleFunc("POST /api/kitchen-stations", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		name, destinationType, address, errKey := stationForm(r)
		if errKey != "" {
			http.Redirect(w, r, "/kitchen-stations?err="+errKey, http.StatusSeeOther)
			return
		}
		id, err := posRepo.CreateKitchenStation(r.Context(), name, destinationType, address)
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
		name, destinationType, address, errKey := stationForm(r)
		if errKey != "" {
			http.Redirect(w, r, "/kitchen-stations?err="+errKey, http.StatusSeeOther)
			return
		}
		// A joined till gets a narrower rule than requirePrimary's blanket
		// refusal above (ut-docs#1585): printer_address is till-local and
		// never synced (skipCols in sync_admin_repo.go), so a joined till
		// legitimately sets its own address here
		// (TestAdminDumpApplyRoundTrip_KitchenStationPrinterAddressStaysLocal
		// pins this as intended behavior — without it, a multi-till shop
		// would have no way to point a shared station at each till's own
		// printer). name/destination_type are shop-wide and primary-owned,
		// so a change to either of those is still refused -- only an
		// address-only edit is let through, via
		// SetKitchenStationPrinterAddress, not the full UpdateKitchenStation.
		if d.SyncPrimaryURL(r.Context()) != "" {
			current, found, err := posRepo.GetKitchenStation(r.Context(), id)
			if err != nil {
				http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.update", http.StatusSeeOther)
				return
			}
			if !found {
				http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.not_found", http.StatusSeeOther)
				return
			}
			// TrimSpace on both sides even though current.Name/DestinationType
			// are already trimmed, persisted values (Create/UpdateKitchenStation
			// only ever write stationForm's trimmed output) -- a defensive
			// belt-and-suspenders against a stray untrimmed value ever reaching
			// the DB some other way, so a joined till can never be permanently
			// unable to set its own printer address over whitespace alone.
			if name != strings.TrimSpace(current.Name) || destinationType != strings.TrimSpace(current.DestinationType) {
				http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.replica_use_primary", http.StatusSeeOther)
				return
			}
			if err := posRepo.SetKitchenStationPrinterAddress(r.Context(), id, address); err != nil {
				http.Redirect(w, r, "/kitchen-stations?err=kitchenstations.error.update", http.StatusSeeOther)
				return
			}
			audit(r, actor.ID, id, "kitchen_station_update")
			http.Redirect(w, r, "/kitchen-stations", http.StatusSeeOther)
			return
		}
		if err := posRepo.UpdateKitchenStation(r.Context(), id, name, destinationType, address); err != nil {
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
		if !requirePrimary(w, r) {
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
		if !requirePrimary(w, r) {
			return
		}
		setRoutes(w, r, posRepo.SetCategoryStationRoutes, r.PathValue("categoryID"), "kitchen_station_category_routes", actor.ID)
	})

	mux.HandleFunc("POST /api/kitchen-stations/routes/items/{itemID}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		setRoutes(w, r, posRepo.SetItemStationRoutes, r.PathValue("itemID"), "kitchen_station_item_routes", actor.ID)
	})
}
