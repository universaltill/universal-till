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

// registerRegisters wires the registers admin page (universaltill/ut-docs#651).
// Manager/admin only, structural mirror of registerLocations
// (locations_page.go). Unlike a stock location, a register with existing
// shift/sale history CAN still be deactivated -- retiring a till keeps its
// history -- so this page only guards the last-active-register case, never
// RegisterInUse.
func registerRegisters(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the "settings" action (039's catalog) via
	// canPerform — see locations_page.go's identical requireManager for
	// why (ut-docs#901): the old raw IsManager() check never saw
	// canPerform's UT_AUTH=off escape hatch, so this page 403'd
	// permanently under the dev/CI auth-bypass. No change to gated
	// (UT_AUTH on) behavior.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "register", targetID, action, nil, now, "")
	}

	renderRegisters := func(w http.ResponseWriter, r *http.Request, errKey string) {
		regs, err := posRepo.ListRegistersForAdmin(r.Context())
		if err != nil {
			http.Error(w, "failed to load registers", http.StatusInternalServerError)
			return
		}
		// Name-resolution uses every location (including a deactivated one a
		// register might still reference), separately from the create-form
		// picker below, which deliberately only offers active locations.
		allLocs, err := posRepo.ListStockLocations(r.Context())
		if err != nil {
			http.Error(w, "failed to load stock locations", http.StatusInternalServerError)
			return
		}
		locNames := make(map[string]string, len(allLocs))
		for _, l := range allLocs {
			locNames[l.ID] = l.Name
		}
		locs, err := posRepo.ListActiveStockLocations(r.Context())
		if err != nil {
			http.Error(w, "failed to load stock locations", http.StatusInternalServerError)
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
				http.Error(w, "failed to load registers", http.StatusInternalServerError)
				return
			}
			v.InUse = inUse
			views = append(views, v)
		}
		httpx.Render("ui/pages/registers.html", map[string]any{
			"title":          "Registers",
			"theme":          d.CurrentState().Theme,
			"menuItems":      d.MenuSnapshot(),
			"registers":      views,
			"stockLocations": locs,
			"errKey":         errKey,
		})(w, r)
	}

	mux.HandleFunc("GET /registers", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderRegisters(w, r, r.URL.Query().Get("err"))
	})

	mux.HandleFunc("POST /api/registers", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			http.Redirect(w, r, "/registers?err=registers.error.required", http.StatusSeeOther)
			return
		}
		var locationID *string
		if loc := strings.TrimSpace(r.PostFormValue("location_id")); loc != "" {
			locationID = &loc
		}
		id, err := posRepo.CreateRegister(r.Context(), name, locationID)
		if err != nil {
			http.Redirect(w, r, "/registers?err=registers.error.create", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "register_create")
		http.Redirect(w, r, "/registers", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/registers/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			http.Redirect(w, r, "/registers?err=registers.error.required", http.StatusSeeOther)
			return
		}
		if err := posRepo.RenameRegister(r.Context(), id, name); err != nil {
			http.Redirect(w, r, "/registers?err=registers.error.rename", http.StatusSeeOther)
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
			http.Redirect(w, r, "/registers?err=registers.error.update", http.StatusSeeOther)
			return
		}
		// ut-docs#895 review: this endpoint always updates both name and
		// location together now, so "register_rename" would mislabel a
		// location-only edit in the audit trail.
		audit(r, actor.ID, id, "register_update")
		http.Redirect(w, r, "/registers", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/registers/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
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
				http.Redirect(w, r, "/registers?err=registers.error.last_active", http.StatusSeeOther)
				return
			}
		}
		if err := posRepo.SetRegisterActive(r.Context(), id, activate); err != nil {
			http.Redirect(w, r, "/registers?err=registers.error.update", http.StatusSeeOther)
			return
		}
		action := "register_deactivate"
		if activate {
			action = "register_activate"
		}
		audit(r, actor.ID, id, action)
		http.Redirect(w, r, "/registers", http.StatusSeeOther)
	})
}
