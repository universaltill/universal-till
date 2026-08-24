package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/taxrate"
)

// taxCodeRow is one row of the tax-code management table (ut-docs#259):
// percent values pre-formatted for display, and a pre-built hx-vals JSON
// payload for the activate/deactivate toggle button, which resubmits the
// row's full current fields through the SAME POST /api/catalog/tax-codes/
// update endpoint the edit form uses -- there is no separate toggle-only
// endpoint, and no delete endpoint at all (tax_codes.id is FK-referenced by
// items.tax_code_id, 001_init.sql).
type taxCodeRow struct {
	ID              string
	Name            string
	RatePercent     string
	TakeawayPercent string // "" when no takeaway override is pinned
	IsActive        bool
	ToggleVals      string
}

// buildTaxCodeRows assembles the table's view rows from the repository's
// TaxCodeView rows.
func buildTaxCodeRows(views []data.TaxCodeView) []taxCodeRow {
	rows := make([]taxCodeRow, 0, len(views))
	for _, v := range views {
		takeawayPct := ""
		if v.TakeawayRateBP != nil {
			takeawayPct = taxrate.FormatPercent(int(*v.TakeawayRateBP))
		}
		vals, _ := json.Marshal(map[string]any{
			"id":           v.ID,
			"name":         v.Name,
			"rate":         taxrate.FormatPercent(int(v.RateBP)),
			"takeawayRate": takeawayPct,
			"isActive":     boolToFormFlag(!v.IsActive), // toggle flips the current state
		})
		rows = append(rows, taxCodeRow{
			ID:              v.ID,
			Name:            v.Name,
			RatePercent:     taxrate.FormatPercent(int(v.RateBP)),
			TakeawayPercent: takeawayPct,
			IsActive:        v.IsActive,
			ToggleVals:      string(vals),
		})
	}
	return rows
}

func boolToFormFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// taxCodeFormActive reads the isActive checkbox, same hidden-fallback
// convention internal/pages/catalog/handlers.go's formCheckboxActive uses
// (checked ⇒ the browser submits BOTH the hidden "0" and the checkbox "1",
// hidden-then-checkbox in DOM order) -- reimplemented locally rather than
// exported from that package, since this handler lives in package pages,
// not pages/catalog.
func taxCodeFormActive(r *http.Request) bool {
	vals := r.Form["isActive"]
	if len(vals) == 0 {
		return true // no isActive field submitted at all: default active
	}
	for _, v := range vals {
		if v == "1" {
			return true
		}
	}
	return false
}

// parsePercentToBP parses a percent string ("19", "19.5") into basis
// points, the same *100-and-round direction internal/pages/init.go's
// TaxRatePct*100 conversion already uses. There is no ParsePercent helper
// in internal/taxrate (only FormatPercent, the display side), so this is
// the parse-side counterpart, kept local to this handler. Rejects
// unparseable, non-finite, negative, and >100% (>10000bp) input as a
// basic sanity bound (ut-docs#259) -- not a real limit on any real tax
// regime, just a guard against a fat-fingered entry silently persisting.
//
// The bound is INCLUSIVE of 100%, deliberately: catimport.ParseTaxRateBP
// (the import-side parser this must agree with) accepts `f > 100` as its
// reject condition, tax_codes.html's input carries max="100", and the
// message the user gets back says "between 0 and 100". Review finding,
// ut-docs#259: this originally rejected `bp >= 10000`, so entering exactly
// 100 was refused with a message that claimed 100 was allowed, and a code
// the CSV importer would happily create could not be re-entered by hand.
func parsePercentToBP(val string) (int, error) {
	// Both bounds are checked on the FLOAT, before the int conversion, for
	// the same reason catimport.ParseTaxRateBP does it that way: Go leaves
	// an out-of-range float64->int conversion implementation-defined (amd64
	// wraps to MinInt64, arm64 saturates to MaxInt64), so a post-conversion
	// range check on something like "1e300" is only accidentally correct on
	// whichever machine it was tried. Review finding, ut-docs#259 -- same
	// class as ut-docs#512's finding B1 on the import-side parser.
	f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 100 {
		return 0, fmt.Errorf("invalid rate")
	}
	bp := int(math.Round(f * 100))
	if bp < 0 || bp > 10000 {
		return 0, fmt.Errorf("invalid rate")
	}
	return bp, nil
}

// parseTaxCodeForm validates the shared create/update submission shape:
// name + rate (required) and takeawayRate (optional, blank => no override).
// Returns an httpStatusError (defined in plugin_settings_page.go, same
// package) carrying the exact status/message to send, so the caller can
// respond directly rather than falling back to a generic 500.
func parseTaxCodeForm(r *http.Request, locale string) (name string, rateBP int, takeawayBP *int, err error) {
	name = strings.TrimSpace(r.Form.Get("name"))
	if name == "" {
		// Localised, not a bare English literal: the page's own JS renders
		// this body verbatim into the form's status line, and a
		// whitespace-only name passes the input's `required` attribute in
		// the browser, so this IS user-reachable. Same key shape every
		// sibling admin CRUD page already uses (locations.error.required,
		// promotions.error.required, tables.error.required). Review
		// finding, ut-docs#259.
		return "", 0, nil, httpStatusError{status: http.StatusBadRequest, msg: httpx.T(locale, "taxcodes.err.name_required")}
	}
	rateBP, perr := parsePercentToBP(r.Form.Get("rate"))
	if perr != nil {
		return "", 0, nil, httpStatusError{status: http.StatusBadRequest, msg: httpx.T(locale, "taxcodes.err.invalid_rate")}
	}
	if raw := strings.TrimSpace(r.Form.Get("takeawayRate")); raw != "" {
		tbp, terr := parsePercentToBP(raw)
		if terr != nil {
			return "", 0, nil, httpStatusError{status: http.StatusBadRequest, msg: httpx.T(locale, "taxcodes.err.invalid_rate")}
		}
		takeawayBP = &tbp
	}
	return name, rateBP, takeawayBP, nil
}

// registerTaxCodes mounts the tax-code management UI (ut-docs#259): a
// manager-gated list + create/edit form for tax_codes, plus the
// activate/deactivate toggle -- there is no hard-delete path, since
// tax_codes.id is FK-referenced by items.tax_code_id. Placed in the
// top-level pages package (not pages/catalog) so it can call the unexported
// canPerform gate directly, same as plugin_settings_page.go.
func registerTaxCodes(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewCatalogRepo(d.Db)

	renderTaxCodesTable := func(w http.ResponseWriter, r *http.Request) {
		views, err := repo.ListAllTaxCodes(r.Context())
		if err != nil {
			// Same repo call and locale key as the GET /catalog/tax-codes
			// handler below, but a distinct call site: this one runs only
			// AFTER a Create/Update write to the same table has already
			// succeeded in this same request, so a schema-level failure can
			// never reach it (the write would have failed first). It is
			// still independently reachable, and covered, via a row the
			// write leaves untouched but the re-read cannot scan --
			// TestTaxCodesAPI_Create_TableRenderErrorIsLocalized (ut-docs#945).
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "taxcodes.err.list_failed", "taxcodes_list_render", err)
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith([]string{
			filepath.Join("web", "ui", "partials", "tax_codes_table.html"),
		}, funcs)("tax_codes_table", map[string]any{"TaxCodes": buildTaxCodeRows(views)})(w, r)
	}

	mux.HandleFunc("GET /catalog/tax-codes", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "tax_code_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		views, err := repo.ListAllTaxCodes(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "taxcodes.err.list_failed", "taxcodes_list_page", err)
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith([]string{
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "tax_codes.html"),
			filepath.Join("web", "ui", "partials", "nav.html"),
			filepath.Join("web", "ui", "partials", "bugreport_panel.html"),
			filepath.Join("web", "ui", "partials", "tax_codes_table.html"),
		}, funcs)("base", map[string]any{
			"title":     "Tax codes",
			"menuItems": d.MenuSnapshot(),
			"theme":     d.CurrentState().Theme,
			"TaxCodes":  buildTaxCodeRows(views),
		})(w, r)
	})

	mux.HandleFunc("POST /api/catalog/tax-codes", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "tax_code_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		_ = r.ParseForm()
		locale := httpx.ResolveLocale(w, r)
		name, rateBP, takeawayBP, err := parseTaxCodeForm(r, locale)
		if err != nil {
			if se, ok := err.(httpStatusError); ok {
				http.Error(w, se.msg, se.status)
				return
			}
			// Genuinely unreachable via any real request: parseTaxCodeForm
			// (above) always returns either a nil error or one already wrapped
			// as httpStatusError (the branch above) -- there is no code path in
			// its current contract that returns a bare, unwrapped error. Still
			// routed through the localized helper for defensive consistency
			// (never a raw err.Error() on the wire) -- ut-docs#945 review: no
			// dedicated regression test since no real request can reach this.
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "taxcodes.err.invalid_form", "taxcodes_create_form", err)
			return
		}
		if _, err := repo.CreateTaxCode(r.Context(), name, rateBP, takeawayBP); err != nil {
			if errors.Is(err, data.ErrTaxCodeNameExists) {
				http.Error(w, httpx.T(locale, "taxcodes.err.duplicate_name"), http.StatusBadRequest)
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "taxcodes.err.save_failed", "taxcodes_create_save", err)
			return
		}
		renderTaxCodesTable(w, r)
	})

	mux.HandleFunc("POST /api/catalog/tax-codes/update", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "tax_code_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		_ = r.ParseForm()
		locale := httpx.ResolveLocale(w, r)
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		name, rateBP, takeawayBP, err := parseTaxCodeForm(r, locale)
		if err != nil {
			if se, ok := err.(httpStatusError); ok {
				http.Error(w, se.msg, se.status)
				return
			}
			// Genuinely unreachable -- see the identical comment on the create
			// handler above; parseTaxCodeForm never returns a bare error here
			// either. Routed through the localized helper defensively, with no
			// dedicated regression test for the same reason (ut-docs#945).
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "taxcodes.err.invalid_form", "taxcodes_update_form", err)
			return
		}
		active := taxCodeFormActive(r)
		if err := repo.UpdateTaxCode(r.Context(), id, name, rateBP, takeawayBP, active); err != nil {
			switch {
			case errors.Is(err, data.ErrTaxCodeNameExists):
				http.Error(w, httpx.T(locale, "taxcodes.err.duplicate_name"), http.StatusBadRequest)
			case errors.Is(err, data.ErrTaxCodeNotFound):
				http.Error(w, httpx.T(locale, "taxcodes.err.not_found"), http.StatusNotFound)
			default:
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "taxcodes.err.save_failed", "taxcodes_update_save", err)
			}
			return
		}
		renderTaxCodesTable(w, r)
	})
}
