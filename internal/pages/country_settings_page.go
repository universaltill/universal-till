package pages

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// localeSafeToPreset decides whether a country's own DefaultLocale
// (country_settings.default_locale, ut-docs#1027) is safe to write into
// store.locale WITHOUT the operator having explicitly chosen it or its
// language pack being installed.
//
// Plain UI text is always safe to preset: I18n.T() gracefully falls back to
// English for any key missing from a not-yet-installed language-pack
// overlay. But store.locale also drives httpx.IsRTL (page direction) and
// httpx.LocalizeDigits (number rendering) immediately and unconditionally —
// neither has a translation-missing fallback. So an RTL locale (fa/ar/ur/…)
// is only safe to preset once its base language is actually available
// (bundled or an installed overlay); a non-RTL locale is always safe
// (Latin digits, LTR either way), which is what lets DE/FR/ES/IT/NL —
// ut-docs#1027's own headline case — preset unconditionally.
func localeSafeToPreset(locale string) bool {
	return locale != "" && (!httpx.IsRTL(locale) || slices.Contains(httpx.AvailableLocales(), baseLang(locale)))
}

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

	// requireManager gates on the "settings" action (039's catalog) via
	// canPerform, not a raw IsManager() check on the session — matching
	// every other admin page (#555's five successor cards; see authz.go's
	// own doc comment). This page was missed by that migration
	// (ut-docs#901/#902): with the old raw check, canPerform's
	// UT_AUTH=off escape hatch never applied here, so this page 403'd
	// permanently under the dev/CI auth-bypass — canPerform's
	// auth.Disabled(...) branch fixes that, with no change to gated
	// (UT_AUTH on) behavior: "settings" is granted to exactly
	// manager/admin/super_admin, the same set IsManager() recognized.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required") // page-error:allow ut-docs#1458 (pending migration to httpx.RenderError — tracked follow-up card, out of #1455's scope)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "country_setting", targetID, action, nil, now, "")
	}

	// redirectTarget carries the "all=1" view state through a POST's
	// redirect back to the GET page (ut-docs#1024 review finding): every
	// form on the page posts back with "?all=1" appended to its action
	// when rendered from the all-countries view (see the template), so a
	// save/delete/add made there lands back on the SAME view instead of
	// silently dropping into the filtered default — where an edited row
	// the operator doesn't see reads as "nothing happened," and an added
	// custom country is invisible until someone thinks to check "show
	// all" on their own.
	redirectTarget := func(r *http.Request, path string) string {
		if r.URL.Query().Get("all") != "1" {
			return path
		}
		if strings.Contains(path, "?") {
			return path + "&all=1"
		}
		return path + "?all=1"
	}

	renderPage := func(w http.ResponseWriter, r *http.Request, errKey string) {
		countries, err := countryRepo.List(r.Context())
		if err != nil {
			http.Error(w, "failed to load country settings", http.StatusInternalServerError) // page-error:allow ut-docs#1458 (pending migration to httpx.RenderError — tracked follow-up card, out of #1455's scope)
			return
		}

		// ut-docs#1024: a single-shop merchant sees only their own country
		// by default — the full 14-row seeded list read as "which country
		// is mine?" rather than settings. "Show all countries" (?all=1) is
		// the explicit secondary affordance back to the old unfiltered
		// view. Genuine multi-country detection (from configured
		// locations) isn't implementable yet — StockLocation carries no
		// country dimension — so that's tracked as a separate follow-up,
		// not built here.
		st := d.CurrentState()
		showAll := r.URL.Query().Get("all") == "1"
		countryUnknown := false
		visible := countries
		if !showAll {
			visible = nil
			for _, c := range countries {
				if c.Code == st.Country {
					visible = append(visible, c)
					break
				}
			}
			if len(visible) == 0 {
				// The shop's configured country has no seeded/custom row
				// (shouldn't happen — setup only ever writes a seeded
				// code — but never render a blank page over it). Also
				// flagged as countryUnknown so the template can explain
				// why every country is showing, rather than offering a
				// "back to just yours" link that would only re-enter this
				// same fallback (review finding: that link was inert).
				visible, showAll, countryUnknown = countries, true, true
			}
		}

		rows := make([]countryRow, 0, len(visible))
		for _, c := range visible {
			rows = append(rows, countryRow{
				CountrySetting: c,
				TaxRatePct:     formatBPAsPercent(c.TaxRateBP),
				AtFloor:        c.ArchiveMinDays == data.GlobalArchiveMinDays,
			})
		}
		httpx.Render("ui/pages/country_settings.html", map[string]any{
			"title":          "Country settings",
			"theme":          st.Theme,
			"menuItems":      d.MenuSnapshot(),
			"countries":      rows,
			"floorDays":      data.GlobalArchiveMinDays,
			"errKey":         errKey,
			"showAll":        showAll,
			"countryUnknown": countryUnknown,
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
			http.Redirect(w, r, redirectTarget(r, "/country-settings?err=countrysettings.error.code_required"), http.StatusSeeOther)
			return
		}

		taxBP, err := parsePercentAsBP(r.PostFormValue("tax_rate_pct"))
		if err != nil {
			http.Redirect(w, r, redirectTarget(r, "/country-settings?err=countrysettings.error.tax_invalid"), http.StatusSeeOther)
			return
		}
		archiveDays, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("archive_min_days")), 10, 64)
		if err != nil {
			http.Redirect(w, r, redirectTarget(r, "/country-settings?err=countrysettings.error.retention_invalid"), http.StatusSeeOther)
			return
		}

		// Preserve the existing name key and default locale (ut-docs#1027) so
		// an edit through this form — this page has no field for either —
		// doesn't blank the label a builtin country renders by, or the
		// locale a fresh till derives from choosing it at setup.
		nameKey := ""
		defaultLocale := ""
		if existing, found, gerr := countryRepo.Get(r.Context(), code); gerr == nil && found {
			nameKey = existing.NameKey
			defaultLocale = existing.DefaultLocale
		}

		cs := data.CountrySetting{
			Code:           code,
			NameKey:        nameKey,
			Currency:       strings.TrimSpace(r.PostFormValue("currency")),
			CurrencySymbol: strings.TrimSpace(r.PostFormValue("currency_symbol")),
			TaxRateBP:      taxBP,
			TaxInclusive:   r.PostFormValue("tax_inclusive") == "1",
			ArchiveMinDays: archiveDays,
			DefaultLocale:  defaultLocale,
		}
		if err := countryRepo.Upsert(r.Context(), cs); err != nil {
			key := "countrysettings.error.save"
			// The floor is the one failure an operator can actually act on, so
			// name it specifically rather than showing a generic save error.
			if archiveDays < data.GlobalArchiveMinDays {
				key = "countrysettings.error.below_floor"
			}
			http.Redirect(w, r, redirectTarget(r, "/country-settings?err="+key), http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, cs.Code, "country_setting_save")
		http.Redirect(w, r, redirectTarget(r, "/country-settings"), http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/country-settings/{code}/delete", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		code := r.PathValue("code")
		if err := countryRepo.Delete(r.Context(), code); err != nil {
			http.Redirect(w, r, redirectTarget(r, "/country-settings?err=countrysettings.error.delete"), http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, code, "country_setting_delete")
		http.Redirect(w, r, redirectTarget(r, "/country-settings"), http.StatusSeeOther)
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
