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

// defaultFiscalRegisterDEEasType is pre-filled in the "add entry" form and
// re-applied server-side if the field arrives blank -- the shop's till is
// almost always this category on ELSTER's own form, but the field stays
// editable for the rare exception.
const defaultFiscalRegisterDEEasType = "Tablet-/App-Kassen-Systeme"

// fiscalRegisterDEDateLayout is this repo's plain-date convention (see
// migration 059's header comment) -- a calendar date typed on a form, not
// an instant, so no time-of-day/timezone component.
const fiscalRegisterDEDateLayout = "2006-01-02"

// fiscalRegisterDELocationGroup is one location's entries, pre-grouped in
// the handler so the template just ranges over groups in order -- the repo
// layer already returns rows ordered by location name (unassigned last),
// so grouping here is a single linear pass, no re-sorting.
type fiscalRegisterDELocationGroup struct {
	LocationID       string
	LocationName     string
	LocationStreet   string
	LocationPostcode string
	LocationCity     string
	// HasLocation is false for the synthetic "no location assigned" group,
	// which never gets an address-edit form (there's no location row to
	// edit).
	HasLocation bool
	Entries     []fiscalRegisterDEEntryView
}

// fiscalRegisterDEEntryView adds the template-only due-soon flags to the
// repo row. §146a Abs. 4 AO's one-month clock runs on BOTH events this page
// tracks -- acquiring a till/TSE and decommissioning one (the AO form's own
// "Außerbetriebnahme-Datum" field, same as "Datum der Anschaffung") -- so
// each gets its own flag rather than collapsing to one boolean that could
// only ever mean "acquired."
type fiscalRegisterDEEntryView struct {
	data.FiscalRegisterDE
	// DueSoon flags an entry acquired within the last 31 days that hasn't
	// been decommissioned yet, for the one-month-due banner. There is no
	// separate "acknowledged" concept (deliberately simple, per the
	// Architect's design) -- it's purely a function of AcquiredOn and now.
	DueSoon bool
	// DecommissionDueSoon flags an entry decommissioned within the last 31
	// days -- the shop's notification duty doesn't end at acquisition; a
	// removed till/TSE has to be reported too (ut-docs#665 review, S3).
	DecommissionDueSoon bool
}

// fiscalRegisterPluginActive reports whether the German tax plugin
// (taxDePluginID, import_page.go) is installed and active. Used by
// menu_page.go's nav-tile gate (ut-docs#1084): ADR-0050 Decision 1 places
// the §146a Abs. 4 AO notification data this page captures squarely in
// the country plugin, and country=DE alone (the tile's only gate before
// ut-docs#1084) let the tile appear on a shop with zero plugins installed
// -- the literal, visible form of the product owner's ut-docs#1026
// complaint. Not applied to this page's own route handlers: direct
// navigation to /fiscal-register has never had a country gate either
// (see registerFiscalRegisterDE's doc comment), and this page is one of
// the manual's screenshotted topics (web/help/*/fiscal-register.md) --
// docs-shots' throwaway till never has any plugin installed by design
// (playwright.docs.config.ts: "always fresh... an installed plugin
// [would] silently bake into 'reproducible' documentation screenshots"),
// so gating the route itself would make this topic unscreenshotable
// without a broader change to that harness. Tracked as the remaining
// half of ut-docs#1026 rather than solved here. A lookup error fails
// closed (false): a broken plugin lookup must never leave the tile
// advertising a page whose data source isn't there.
func fiscalRegisterPluginActive(ctx context.Context, d *common.Deps) bool {
	active, err := data.NewPluginRepo(d.Db).PluginActive(ctx, taxDePluginID)
	if err != nil {
		return false
	}
	return active
}

// withinLastMonth reports whether date (fiscalRegisterDEDateLayout) falls
// within the last 31 days up to and including now -- shared by both the
// acquired-on and decommissioned-on due-soon checks so the two stay
// consistent by construction rather than by two independently maintained
// comparisons.
func withinLastMonth(date string, now time.Time) bool {
	t, err := time.Parse(fiscalRegisterDEDateLayout, date)
	if err != nil {
		return false
	}
	d := now.Sub(t)
	return d >= 0 && d <= 31*24*time.Hour
}

// registerFiscalRegisterDE wires the §146a Abs. 4 AO fiscal register page
// (ut-docs#665). Manager/admin only, structural mirror of
// registerRegisters/registerLocations. Data capture ONLY -- no export, no
// XML, no filing on the shop's behalf (ut-docs#937 is the separate export
// follow-up); this page exists so a shop has the data on hand to file its
// own Mein ELSTER notification.
func registerFiscalRegisterDE(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	// Entries persist in plugin_storage under the German tax plugin's
	// namespace since ADR-0072 (ut-docs#1106, migration 075) — the plugin
	// owns the §146a Abs. 4 AO data (uninstall removes it), core still owns
	// this page. Registers, locations, addresses and audit stay on posRepo.
	fiscalStore := data.NewFiscalRegisterDEStore(d.Db, taxDePluginID)

	// requireManager gates on the "settings" action (039's catalog) via
	// canPerform, same pattern (and same UT_AUTH=off rationale) as
	// registers_page.go/locations_page.go -- ut-docs#903 tracks a dedicated
	// permission action separately; this page deliberately reuses
	// "settings" for now, per the Architect's design.
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
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "fiscal_register_de", targetID, action, nil, now, "")
	}

	renderFiscalRegister := func(w http.ResponseWriter, r *http.Request, errKey string) {
		entries, err := fiscalStore.List(r.Context())
		if err != nil {
			http.Error(w, "failed to load fiscal register", http.StatusInternalServerError) // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}
		// The "add entry" picker offers active registers only, same
		// convention as registers_page.go's own create-form picker -- a
		// deactivated till isn't one a shop would be newly registering with
		// the tax office.
		registers, err := posRepo.ListRegisters(r.Context())
		if err != nil {
			http.Error(w, "failed to load registers", http.StatusInternalServerError) // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}

		now := time.Now().UTC()
		// fiscalStore.List already orders rows by location name
		// (unassigned last), so a single linear pass -- start a new group
		// the first time a location id is seen, append to it otherwise --
		// reproduces that same order in the grouped output with no
		// re-sorting.
		var groups []*fiscalRegisterDELocationGroup
		var noLocation *fiscalRegisterDELocationGroup
		byLocation := map[string]*fiscalRegisterDELocationGroup{}
		for _, e := range entries {
			view := fiscalRegisterDEEntryView{FiscalRegisterDE: e}
			if e.DecommissionedOn == nil {
				view.DueSoon = withinLastMonth(e.AcquiredOn, now)
			} else {
				view.DecommissionDueSoon = withinLastMonth(*e.DecommissionedOn, now)
			}
			if e.LocationID == "" {
				if noLocation == nil {
					noLocation = &fiscalRegisterDELocationGroup{HasLocation: false}
				}
				noLocation.Entries = append(noLocation.Entries, view)
				continue
			}
			g, ok := byLocation[e.LocationID]
			if !ok {
				g = &fiscalRegisterDELocationGroup{
					LocationID:       e.LocationID,
					LocationName:     e.LocationName,
					LocationStreet:   e.LocationStreet,
					LocationPostcode: e.LocationPostcode,
					LocationCity:     e.LocationCity,
					HasLocation:      true,
				}
				byLocation[e.LocationID] = g
				groups = append(groups, g)
			}
			g.Entries = append(g.Entries, view)
		}
		if noLocation != nil {
			groups = append(groups, noLocation)
		}
		out := make([]fiscalRegisterDELocationGroup, len(groups))
		for i, g := range groups {
			out[i] = *g
		}

		httpx.Render("ui/pages/fiscal_register.html", map[string]any{
			"title":     "Fiscal register",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"groups":    out,
			"registers": registers,
			"errKey":    errKey,
		})(w, r)
	}

	mux.HandleFunc("GET /fiscal-register", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderFiscalRegister(w, r, r.URL.Query().Get("err"))
	})

	mux.HandleFunc("POST /api/fiscal-register", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		registerID := strings.TrimSpace(r.PostFormValue("register_id"))
		easType := strings.TrimSpace(r.PostFormValue("eas_type"))
		if easType == "" {
			easType = defaultFiscalRegisterDEEasType
		}
		easSoftware := strings.TrimSpace(r.PostFormValue("eas_software"))
		easSerial := strings.TrimSpace(r.PostFormValue("eas_serial"))
		tseSerial := strings.TrimSpace(r.PostFormValue("tse_serial"))
		tseCertificationID := strings.TrimSpace(r.PostFormValue("tse_certification_id"))
		tseType := strings.TrimSpace(r.PostFormValue("tse_type"))
		acquiredOn := strings.TrimSpace(r.PostFormValue("acquired_on"))
		commissionedOnRaw := strings.TrimSpace(r.PostFormValue("commissioned_on"))

		if registerID == "" || easSoftware == "" || easSerial == "" || tseSerial == "" ||
			tseCertificationID == "" || tseType == "" || acquiredOn == "" {
			http.Redirect(w, r, "/fiscal-register?err=fiscalregister.error.required", http.StatusSeeOther)
			return
		}
		if _, err := time.Parse(fiscalRegisterDEDateLayout, acquiredOn); err != nil {
			http.Redirect(w, r, "/fiscal-register?err=fiscalregister.error.invalid_date", http.StatusSeeOther)
			return
		}
		var commissionedOn *string
		if commissionedOnRaw != "" {
			if _, err := time.Parse(fiscalRegisterDEDateLayout, commissionedOnRaw); err != nil {
				http.Redirect(w, r, "/fiscal-register?err=fiscalregister.error.invalid_date", http.StatusSeeOther)
				return
			}
			commissionedOn = &commissionedOnRaw
		}

		id, err := fiscalStore.Create(r.Context(), registerID, easType, easSoftware, easSerial,
			tseSerial, tseCertificationID, tseType, acquiredOn, commissionedOn)
		if err != nil {
			http.Redirect(w, r, "/fiscal-register?err=fiscalregister.error.create", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "fiscal_register_de_create")
		http.Redirect(w, r, "/fiscal-register", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/fiscal-register/{id}/decommission", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		// Server-stamped to today -- this is a "mark it now" action, not a
		// backdated entry (per the Architect's design).
		today := time.Now().UTC().Format(fiscalRegisterDEDateLayout)
		if err := fiscalStore.Decommission(r.Context(), id, today); err != nil {
			http.Redirect(w, r, "/fiscal-register?err=fiscalregister.error.decommission", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "fiscal_register_de_decommission")
		http.Redirect(w, r, "/fiscal-register", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/fiscal-register/locations/{id}/address", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		street := strings.TrimSpace(r.PostFormValue("street"))
		postcode := strings.TrimSpace(r.PostFormValue("postcode"))
		city := strings.TrimSpace(r.PostFormValue("city"))
		if err := posRepo.SetStockLocationAddressDE(r.Context(), id, street, postcode, city); err != nil {
			http.Redirect(w, r, "/fiscal-register?err=fiscalregister.address.error", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "stock_location_address_update_de")
		http.Redirect(w, r, "/fiscal-register", http.StatusSeeOther)
	})
}
