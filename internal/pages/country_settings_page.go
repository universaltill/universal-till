package pages

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// countryRow is one jurisdiction as the admin page lists it. ArchiveMinDays
// is shown alongside the floor so the operator can see WHY a lower value is
// refused, rather than meeting a bare error after typing one.
type countryRow struct {
	data.CountrySetting
	TaxRatePct string // basis points rendered back as a percent for the form
	AtFloor    bool
}

// registerCountrySettings wires the per-country settings admin page
// (universaltill/ut-docs#635 → #659): CRUD over country_settings — the
// per-jurisdiction defaults (currency, tax, archive retention) a shop's
// country points at. Manager/admin only, modelled on registerKitchenStations.
//
// Two behaviours here are deliberate and are enforced in the repository, not
// just this handler (so an API caller cannot route around them):
//   - archive retention may be raised but never lowered below ADR-0040's
//     global floor;
//   - deleting a BUILTIN country restores its shipped defaults instead of
//     removing the row, so the wizard and the purge gate always have a value.
func registerCountrySettings(mux *http.ServeMux, d *common.Deps) {
	countryRepo := data.NewCountrySettingsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

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
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "country_setting", targetID, action, nil, now, "")
	}

	renderPage := func(w http.ResponseWriter, r *http.Request, errKey string) {
		countries, err := countryRepo.List(r.Context())
		if err != nil {
			http.Error(w, "failed to load country settings", http.StatusInternalServerError)
			return
		}
		rows := make([]countryRow, 0, len(countries))
		for _, c := range countries {
			rows = append(rows, countryRow{
				CountrySetting: c,
				TaxRatePct:     formatBPAsPercent(c.TaxRateBP),
				AtFloor:        c.ArchiveMinDays == data.GlobalArchiveMinDays,
			})
		}
		httpx.Render("ui/pages/country_settings.html", map[string]any{
			"title":     "Country settings",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"countries": rows,
			"floorDays": data.GlobalArchiveMinDays,
			"errKey":    errKey,
		})(w, r)
	}

	mux.HandleFunc("GET /country-settings", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderPage(w, r, r.URL.Query().Get("err"))
	})

	// Create or update. One handler for both: the code is the primary key, so
	// an "add" of an existing code is an edit, which is what an operator
	// re-submitting the same country means anyway.
	mux.HandleFunc("POST /api/country-settings", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()

		code := strings.TrimSpace(r.PostFormValue("code"))
		if code == "" {
			http.Redirect(w, r, "/country-settings?err=countrysettings.error.code_required", http.StatusSeeOther)
			return
		}

		taxBP, err := parsePercentAsBP(r.PostFormValue("tax_rate_pct"))
		if err != nil {
			http.Redirect(w, r, "/country-settings?err=countrysettings.error.tax_invalid", http.StatusSeeOther)
			return
		}
		archiveDays, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("archive_min_days")), 10, 64)
		if err != nil {
			http.Redirect(w, r, "/country-settings?err=countrysettings.error.retention_invalid", http.StatusSeeOther)
			return
		}

		// Preserve the existing name key so an edit through this form doesn't
		// blank the label a builtin country renders by.
		nameKey := ""
		if existing, found, gerr := countryRepo.Get(r.Context(), code); gerr == nil && found {
			nameKey = existing.NameKey
		}

		cs := data.CountrySetting{
			Code:           code,
			NameKey:        nameKey,
			Currency:       strings.TrimSpace(r.PostFormValue("currency")),
			CurrencySymbol: strings.TrimSpace(r.PostFormValue("currency_symbol")),
			TaxRateBP:      taxBP,
			TaxInclusive:   r.PostFormValue("tax_inclusive") == "1",
			ArchiveMinDays: archiveDays,
		}
		if err := countryRepo.Upsert(r.Context(), cs); err != nil {
			key := "countrysettings.error.save"
			// The floor is the one failure an operator can actually act on, so
			// name it specifically rather than showing a generic save error.
			if archiveDays < data.GlobalArchiveMinDays {
				key = "countrysettings.error.below_floor"
			}
			http.Redirect(w, r, "/country-settings?err="+key, http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, cs.Code, "country_setting_save")
		http.Redirect(w, r, "/country-settings", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/country-settings/{code}/delete", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		code := r.PathValue("code")
		if err := countryRepo.Delete(r.Context(), code); err != nil {
			http.Redirect(w, r, "/country-settings?err=countrysettings.error.delete", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, code, "country_setting_delete")
		http.Redirect(w, r, "/country-settings", http.StatusSeeOther)
	})
}

// formatBPAsPercent renders basis points as a percent string for the form,
// trimming a trailing ".00" so whole rates read as "19" not "19.00".
func formatBPAsPercent(bp int64) string {
	s := strconv.FormatFloat(float64(bp)/100, 'f', 2, 64)
	s = strings.TrimSuffix(s, ".00")
	return s
}

// parsePercentAsBP converts the form's percent input to basis points. Storage
// is basis points (the repo's rule for rates); only this boundary knows about
// percent.
func parsePercentAsBP(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	pct, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, err
	}
	if pct < 0 {
		return 0, strconv.ErrRange
	}
	// Round rather than truncate: 8.5% must land on 850 bp, and float
	// representation makes a bare int64() conversion land on 849.
	return int64(pct*100 + 0.5), nil
}
