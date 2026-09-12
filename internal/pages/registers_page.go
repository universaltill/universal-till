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

// registerView adds the resolved location display name to data.RegisterAdmin
// for the template -- LocationID alone is a raw UUID (or nil), neither of
// which is fit to render directly. LocationOptions is this row's own
// edit-picker choices: every active location, plus (ut-docs#895) the
// register's current location even if it has since been deactivated, so
// editing never silently drops the row's existing assignment from the list.
// InUse surfaces RegisterInUse (ut-docs#897) as an informational hint only
// -- see the POST .../active handler below for why it must never gate
// deactivation itself.
type registerView struct {
	data.RegisterAdmin
	LocationName    string
	LocationOptions []data.StockLocation
	// LocationValue is LocationID dereferenced to "" when unset, since Go
	// templates can't usefully compare a *string against a string.
	LocationValue string
	InUse         bool
}

// redirectRegisters/renderRegistersDialogError mirror
// locations_page.go's redirectLocations/renderLocationsDialogError
// (itself mirroring categories_page.go's redirectCategories/
// renderCategoryDialogError) — ut-docs#2185 adopting ut-docs#2010's
// pattern on Registers, the second screen after Locations. See
// redirectLocations's own doc comment for the full reasoning.
func redirectRegisters(w http.ResponseWriter, r *http.Request, target string) {
	if isHtmxDialogRequest(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func renderRegistersDialogError(w http.ResponseWriter, r *http.Request, errKey string) {
	if !isHtmxDialogRequest(r) {
		redirectRegisters(w, r, "/registers?err="+errKey)
		return
	}
	msg := httpx.T(httpx.RequestLocale(r), errKey)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	httpx.RenderPartial("ui/partials/record_dialog_msg.html", map[string]any{"msg": msg})(w, r)
}

// registerRegisters wires the registers admin page (universaltill/ut-docs#651).
// Manager/admin only, structural mirror of registerLocations
// (locations_page.go). Unlike a stock location, a register with existing
// shift/sale history CAN still be deactivated -- retiring a till keeps its
// history -- so this page only guards the last-active-register case, never
// RegisterInUse. Adopts the record_dialog/list_header pattern (ut-docs#2010)
// as of ut-docs#2185 — see redirectRegisters/renderRegistersDialogError above.
func registerRegisters(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the "stock_location_management" action
	// (ut-docs#903, migration 060) via canPerform — see locations_page.go's
	// identical requireManager for the full history (ut-docs#901's
	// UT_AUTH=off fix, then #903's move off the generic "settings" action).
	// Seeded identically to "settings" so no existing till's access
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
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "register", targetID, action, nil, now, "")
	}

	// requirePrimary gates every mutation (create/rename/activate) on this
	// till being the primary (ut-docs#1590). registers is now synced
	// shop-wide (sync_admin_repo.go's adminTables) via a one-way
	// primary-wins pull, so a write accepted on a satellite would silently
	// vanish (a new row deleted, an edit reverted) on the very next admin
	// pull -- refuse it up front instead, with a clear localized message,
	// same pattern as plugins_store_page.go's replica_use_primary gate.
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			renderRegistersDialogError(w, r, "registers.error.replica_use_primary")
			return false
		}
		return true
	}

	renderRegisters := func(w http.ResponseWriter, r *http.Request, errKey string) {
		regs, err := posRepo.ListRegistersForAdmin(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		// Name-resolution uses every location (including a deactivated one a
		// register might still reference), separately from the create-form
		// picker below, which deliberately only offers active locations.
		allLocs, err := posRepo.ListStockLocations(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		locNames := make(map[string]string, len(allLocs))
		for _, l := range allLocs {
			locNames[l.ID] = l.Name
		}
		locs, err := posRepo.ListActiveStockLocations(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		activeLoc := make(map[string]bool, len(locs))
		for _, l := range locs {
			activeLoc[l.ID] = true
		}
		views := make([]registerView, 0, len(regs))
		for _, reg := range regs {
			v := registerView{RegisterAdmin: reg, LocationOptions: locs}
			if reg.LocationID != nil {
				v.LocationName = locNames[*reg.LocationID]
				v.LocationValue = *reg.LocationID
				if !activeLoc[*reg.LocationID] {
					opts := make([]data.StockLocation, len(locs), len(locs)+1)
					copy(opts, locs)
					v.LocationOptions = append(opts, data.StockLocation{ID: *reg.LocationID, Name: v.LocationName})
				}
			}
			inUse, err := posRepo.RegisterInUse(r.Context(), reg.ID)
			if err != nil {
				httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
				return
			}
			v.InUse = inUse
			views = append(views, v)
		}
		registersData := map[string]any{
			"title":          "Registers",
			"theme":          d.CurrentState().Theme,
			"menuItems":      d.MenuSnapshot(),
			"registers":      views,
			"stockLocations": locs,
			"errKey":         errKey,
		}
		// ut-docs#2116: /registers is one of the /admin tree's six
		// destinations -- an htmx request from that panel (NOT a stale
		// history restore, see httpx.IsFragmentSwap) gets just the
		// "content" block plus an out-of-band refresh of the tree so its
		// is-current highlight follows the click; a plain browser GET
		// (deep link, or the redirect a mutation falls back to) still gets
		// the exact same full standalone page as before this card.
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderContentFragment("ui/pages/registers.html", registersData)(w, r)
			writeAdminTreeOOB(w, r, httpx.FuncsFor(httpx.RequestLocale(r)), "/registers", adminGroupsFor(visibleAdminEntries(d, r)))
			return
		}
		httpx.Render("ui/pages/registers.html", registersData)(w, r)
	}

	mux.HandleFunc("GET /registers", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderRegisters(w, r, httpx.QueryErrKey(r))
	})

	mux.HandleFunc("POST /api/registers", func(w http.ResponseWriter, r *http.Request) {
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
			renderRegistersDialogError(w, r, "registers.error.required")
			return
		}
		var locationID *string
		if loc := strings.TrimSpace(r.PostFormValue("location_id")); loc != "" {
			locationID = &loc
		}
		id, err := posRepo.CreateRegister(r.Context(), name, locationID)
		if err != nil {
			renderRegistersDialogError(w, r, "registers.error.create")
			return
		}
		audit(r, actor.ID, id, "register_create")
		redirectRegisters(w, r, "/registers")
	})

	mux.HandleFunc("POST /api/registers/{id}", func(w http.ResponseWriter, r *http.Request) {
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
			renderRegistersDialogError(w, r, "registers.error.required")
			return
		}
		if err := posRepo.RenameRegister(r.Context(), id, name); err != nil {
			renderRegistersDialogError(w, r, "registers.error.rename")
			return
		}
		// ut-docs#895: the same form also carries the register's stock
		// location, so a manager can fix a mis-assignment without
		// recreating the register. Empty selection clears it back to
		// unassigned, same convention as create's location_id.
		var locationID *string
		if loc := strings.TrimSpace(r.PostFormValue("location_id")); loc != "" {
			locationID = &loc
		}
		if err := posRepo.SetRegisterLocation(r.Context(), id, locationID); err != nil {
			renderRegistersDialogError(w, r, "registers.error.update")
			return
		}
		// ut-docs#895 review: this endpoint always updates both name and
		// location together now, so "register_rename" would mislabel a
		// location-only edit in the audit trail.
		audit(r, actor.ID, id, "register_update")
		redirectRegisters(w, r, "/registers")
	})

	mux.HandleFunc("POST /api/registers/{id}/active", func(w http.ResponseWriter, r *http.Request) {
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
			// A shop must always have somewhere to open a shift/take a sale --
			// mirrors the last-active-location guard in locations_page.go.
			// Deliberately NOT gated on RegisterInUse: a register with
			// shift/sale history should still be deactivatable (retiring a
			// till keeps its history).
			activeCount, err := posRepo.CountActiveRegisters(r.Context())
			if err != nil {
				http.Error(w, "failed to check active registers", http.StatusInternalServerError)
				return
			}
			if activeCount <= 1 {
				renderRegistersDialogError(w, r, "registers.error.last_active")
				return
			}
		}
		if err := posRepo.SetRegisterActive(r.Context(), id, activate); err != nil {
			renderRegistersDialogError(w, r, "registers.error.update")
			return
		}
		action := "register_deactivate"
		if activate {
			action = "register_activate"
		}
		audit(r, actor.ID, id, action)
		redirectRegisters(w, r, "/registers")
	})
}
