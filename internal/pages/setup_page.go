package pages

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// autoRegisterAttemptTimeout bounds the ONE synchronous store-registration
// attempt an explicit opt-in triggers (ADR-0071, ut-docs#879) — the wizard's
// "yes" on its last screen, and Settings toggling the same choice on later.
// A sibling of setupBasePluginAttemptTimeout rather than a reuse of it: the
// same 5s offline-first bound, but named for what it actually times, so the
// two can drift independently if either ever needs to.
const autoRegisterAttemptTimeout = 5 * time.Second

// autoRegisterForSetup is POST /api/setup's ADR-0071 hook: persist the
// operator's explicit opt-in answer FIRST (before any network attempt, same
// mid-request-crash reasoning installBasePluginsForSetup documents for its
// own pending-list persistence), then — on an explicit yes only — make one
// best-effort, time-boxed EnsureRegistered call. Never blocks or fails the
// wizard's own response: the persist error is logged and swallowed, and
// EnsureRegistered already logs-and-swallows its own registration failure.
// A failed attempt is NOT retried in the background: enroll.Init's loop
// deliberately never registers a store (see its own comment — it only fetches
// the signing key and registers a device under an ALREADY-registered store),
// so an opted-in till that was offline at wizard completion simply falls back
// to ADR-0015's lazy triggers — the next plugin-store visit/install, or
// Settings → "Register now". Settings' enrolment card shows the till as not
// registered until then, which is the operator's signal. On no/absent, no
// call at all — ADR-0015's lazy registration stays exactly as it is.
func autoRegisterForSetup(ctx context.Context, d *common.Deps, optIn bool) {
	val := "false"
	if optIn {
		val = "true"
	}
	if err := d.Settings.Set(ctx, common.KeyAutoRegisterOptIn, val); err != nil {
		logging.L().Errorf("setup wizard: persist auto-register opt-in: %v", err)
	}
	if !optIn {
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, autoRegisterAttemptTimeout)
	defer cancel()
	enroll.EnsureRegistered(attemptCtx, d.Cfg, d.Settings)
}

// setupCountry prefills currency + tax for the wizard's country step (docs
// repo: architecture/zero-touch-setup.md, phase B). Compact by design —
// "Other" keeps the defaults and everything stays editable in Settings.
type setupCountry struct {
	Code         string
	NameKey      string
	Currency     string
	TaxRatePct   int
	TaxInclusive bool
}

// setupShopTypes is the ADR-0026 shop-type taxonomy (café, retail, service
// trade, hospitality, market stall/pop-up, other) — reused verbatim, not a
// new list (ut-docs#539). Labels are the setup.shop_type.* locale keys.
var setupShopTypes = []string{"cafe", "retail", "service", "hospitality", "market_stall", "other"}

func isValidShopType(v string) bool {
	for _, t := range setupShopTypes {
		if v == t {
			return true
		}
	}
	return false
}

// wizardCountries reads the wizard's country list from country_settings
// (ut-docs#660) — the compile-time setupCountries slice that used to live
// here was moved into that table by ut-docs#659, and this is the read side
// finally catching up, so an admin's edits (or an operator-added country) in
// Settings → Country settings actually reach the one flow that most needs
// them (first-boot setup).
//
// TaxRateBP (basis points) is rounded to the nearest whole percent:
// setupCountry.TaxRatePct, the POST handler's tax_rate_pct form field, and
// common.State.TaxRatePct are all `int` percent throughout the till's core —
// widening that to fractional percent is a real but separate, much larger
// change (touches State/Settings/the POS engine config, not just the
// wizard) and out of scope here. Every builtin country ships whole-percent
// rates today, so this is lossless for the seeded defaults; only a
// fractional rate an admin sets via #659's CRUD UI would round when
// prefilling this wizard.
//
// "OTHER" is always placed last, matching the original hardcoded slice's
// order and the UX convention of a "not listed" catch-all coming last in a
// dropdown — everything else keeps country_settings.List()'s own order
// (alphabetical by code), a deliberate, minor change from the original
// slice's hand-curated order; nothing in the wizard depends on that order
// beyond display sequence.
func wizardCountries(ctx context.Context, db *sql.DB) ([]setupCountry, error) {
	rows, err := data.NewCountrySettingsRepo(db).List(ctx)
	if err != nil {
		return nil, err
	}
	return countrySettingsToSetupCountries(rows), nil
}

// builtinSetupCountries is renderWizard's fallback when country_settings
// can't be read (review finding N2) — the exact values setupCountries used
// to hardcode before ut-docs#660, so a DB read failure degrades to the
// pre-#660 behaviour rather than taking down first boot entirely.
func builtinSetupCountries() []setupCountry {
	return countrySettingsToSetupCountries(data.BuiltinCountryDefaults())
}

// countrySettingsToSetupCountries is the one place CountrySetting (basis
// points, DB row) becomes setupCountry (whole-percent, the wizard's view
// model) — shared by the live DB read and the builtin-defaults fallback so
// they can't drift from each other on rounding or OTHER-ordering.
func countrySettingsToSetupCountries(rows []data.CountrySetting) []setupCountry {
	out := make([]setupCountry, 0, len(rows))
	var other *setupCountry
	for _, r := range rows {
		sc := setupCountry{
			Code:         r.Code,
			NameKey:      r.NameKey,
			Currency:     r.Currency,
			TaxRatePct:   int((r.TaxRateBP + 50) / 100), // round half up, see doc comment
			TaxInclusive: r.TaxInclusive,
		}
		if sc.Code == "OTHER" {
			other = &sc
			continue
		}
		out = append(out, sc)
	}
	if other != nil {
		out = append(out, *other)
	}
	return out
}

// wizardCountryCodes extracts the codes detectCountry needs, excluding
// "OTHER" — that contract predates ut-docs#660 (detectCountry never treated
// "OTHER" as a real detection target) and is preserved here rather than
// changed.
func wizardCountryCodes(countries []setupCountry) []string {
	codes := make([]string, 0, len(countries))
	for _, c := range countries {
		if c.Code != "OTHER" {
			codes = append(codes, c.Code)
		}
	}
	return codes
}

// registerSetup wires the first-boot wizard: language → country (prefills
// currency/tax) → shop name → shop type → restore-from-another-POS?
// (ut-docs#617) → admin PIN → done. Every step has a sane default; both
// routes refuse to run once an operator exists (they are auth-exempt for
// exactly that window).
func registerSetup(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	posRepo := data.NewPOSRepo(d.Db)

	renderWizard := func(w http.ResponseWriter, r *http.Request, errKey string, langUnavailableCode string) {
		// Best-effort, matching every other failure this wizard already
		// tolerates below (locale persist, restore prompt, plugin install,
		// demo seed) — first boot must never become UNDOABLE because of a
		// transient/edge-case DB read (offline-first's "never blocked"
		// posture extends here too, per review finding N2). The builtin
		// defaults are the exact values setupCountries used to hardcode, so
		// this is a graceful degrade to the pre-#660 behaviour, not a guess.
		countries, err := wizardCountries(r.Context(), d.Db)
		if err != nil {
			logging.L().Errorf("setup wizard: load country settings, falling back to builtin defaults: %v", err)
			countries = builtinSetupCountries()
		}
		data := map[string]any{
			"countries": countries,
			"shopTypes": setupShopTypes,
			"errKey":    errKey,
			// Matches the wizard's pre-#590 default (tax-inclusive on) for the
			// case nothing was detected — only overridden below when a country
			// actually matches, same as the country step's own @change handler
			// leaves taxinc alone until a real selection changes it.
			"detectedTaxInclusive": true,
		}
		// Which country the wizard opens on:
		//   - GET → the OS-detected one (ut-docs#590). Detected fresh on every
		//     render: this wizard has no server-side draft state between steps,
		//     so a full-page reload naturally re-detects rather than persisting
		//     a choice the operator hasn't submitted yet. Always freely
		//     changeable in the select below; "" (nothing detected) just leaves
		//     the placeholder.
		//   - POST re-render (PIN error, save failure) → the operator's OWN
		//     submitted pick, never re-detection. They have already been through
		//     the country step, and the hidden currency/tax_rate_pct inputs are
		//     bound to this same x-data: re-detecting here would silently swap a
		//     deliberate "France, 20%" for "Germany, 19%" behind an operator who
		//     is only retyping a mistyped PIN, and the retry would then save the
		//     wrong tax rate without ever showing them the country step again.
		//   - GET carrying ?tax_country= → the country POST
		//     /api/setup/tax-plugin just acted on (see resumeTaxCountry
		//     below). Same "never re-detect over the operator's own pick"
		//     reasoning as the POST branch.
		code := detectCountry(wizardCountryCodes(countries))
		if r.Method == http.MethodPost {
			code = strings.ToUpper(strings.TrimSpace(r.PostFormValue("country")))
		}
		// ut-docs#1180 (review): POST /api/setup/tax-plugin redirects back
		// here after an explicit install tap. Its tile lives on step 3, not
		// step 1 like the language tiles, so a bare /setup redirect would
		// drop the operator at step 1 with the country re-derived from OS
		// detection — silently discarding the country they picked themselves
		// (a Pi imaged in English whose operator chose DE by hand loses it)
		// along with anything typed on step 3. Carry it back over the
		// redirect as a query param instead — no stored state, same posture
		// as install_pending — and resume on the step the button lives on.
		// Only a country that is BOTH tax-mapped and a real wizard country is
		// honoured, so the param can't steer the wizard anywhere the tile
		// itself couldn't.
		resumeTaxCountry := ""
		if r.Method == http.MethodGet {
			if q := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("tax_country"))); q != "" {
				if _, mapped := countryTaxLocale[q]; mapped {
					for _, c := range wizardCountryCodes(countries) {
						if c == q {
							code, resumeTaxCountry = q, q
							break
						}
					}
				}
			}
		}
		if code != "" {
			for _, c := range countries {
				if c.Code == code {
					data["detectedCountry"] = c.Code
					data["detectedCurrency"] = c.Currency
					data["detectedTaxRatePct"] = c.TaxRatePct
					data["detectedTaxInclusive"] = c.TaxInclusive
					break
				}
			}
		}
		if langUnavailableCode != "" {
			data["detectedLangCode"] = langUnavailableCode
		}
		// ut-docs#1092: catalog languages installable from step 1. Served
		// from the package-level TTL cache; the fetch itself is bounded
		// (setupLanguageCatalogFetchTimeout), so this render never hangs on
		// the marketplace, and an unreachable catalog degrades to
		// bundled-only plus a "more languages once connected" note.
		langs, catalogUnavailable := setupInstallableLanguages(r.Context(), d)
		data["installableLangs"] = langs
		data["langCatalogUnavailable"] = catalogUnavailable
		// install_pending: set by POST /api/setup/language's failure redirect
		// (query param, not stored state) — shows the "still installing in
		// the background" note once, on the page that redirect lands on.
		if p := r.URL.Query().Get("install_pending"); isPlausibleLocale(p) {
			data["installPendingLang"] = p
		}
		// ut-docs#1180: ADR-0025 decision 4 — a fiscal (tax) plugin match
		// for the now-resolved country, PROMPTED never silently installed.
		// Uses this same final `code` (the operator's own pick on a POST
		// re-render, never an earlier draft), same TTL-cached-catalog
		// posture as installableLangs just above.
		//
		// The second return (catalogUnavailable) is deliberately NOT put in
		// data: unlike langCatalogUnavailable there is no "…once connected"
		// note to drive with it yet, and an unread template key reads as if
		// there were. Wiring that note is a follow-up (see the code-review
		// record for ut-docs#1180) — it needs product sign-off on whether an
		// unreachable catalog should say anything at all about a *fiscal*
		// plugin during setup, plus copy in every locale.
		taxPlugin, _ := setupInstallableTaxPlugin(r.Context(), d, code)
		data["installableTaxPlugin"] = taxPlugin
		// tax_plugin_pending: set by POST /api/setup/tax-plugin's failure
		// redirect (query param, not stored state) — shows the "still
		// installing in the background" note once, on the page that
		// redirect lands on. Mirrors install_pending above.
		if r.URL.Query().Get("tax_plugin_pending") == "1" {
			data["taxPluginPending"] = true
		}
		// Which step an error re-render lands on: business-identity errors
		// (setup.error.tse_*) belong to step 3, everything else (PIN, save)
		// to the PIN step (7). On a POST re-render the identity fields the
		// operator already typed are echoed back so a tax-number typo
		// doesn't cost them the whole step (the template attribute-escapes
		// these; same trust level as the country echo above).
		errStep := 7
		if strings.HasPrefix(errKey, "setup.error.tse_") {
			errStep = 3
		}
		data["errStep"] = errStep
		// startStep is the step the wizard actually opens on: an error
		// re-render lands on errStep, a tax-plugin install round-trip returns
		// to step 3 (the Germany-only business-identity step its tile lives
		// on), and everything else starts at 1.
		startStep := 1
		switch {
		case errKey != "":
			startStep = errStep
		case resumeTaxCountry != "":
			startStep = 3
		}
		data["startStep"] = startStep
		if r.Method == http.MethodPost {
			data["tseLegalName"] = strings.TrimSpace(r.PostFormValue("tse_legal_name"))
			data["tseOwnerName"] = strings.TrimSpace(r.PostFormValue("tse_owner_name"))
			data["tseTaxNumber"] = strings.TrimSpace(r.PostFormValue("tse_tax_number"))
			data["tseAddress"] = strings.TrimSpace(r.PostFormValue("tse_address"))
		}
		httpx.RenderPartial("ui/pages/setup.html", data)(w, r)
	}

	mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil {
			http.Error(w, "setup unavailable", http.StatusInternalServerError)
			return
		}
		if !firstBoot {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		// Language detection (ut-docs#590): only on a genuinely first visit —
		// no explicit ?lang= yet and no ut_lang cookie from an earlier visit —
		// so detection is a one-time default, never a re-nagging lock, exactly
		// per the card's requirement. An available detected language redirects
		// through the existing ?lang= mechanism (same one the step-1 language
		// buttons already use), which sets the cookie and re-renders; an
		// unavailable one falls through to render with a "coming soon" note.
		_, hasQueryLang := r.URL.Query()["lang"]
		_, cookieErr := r.Cookie("ut_lang")
		langUnavailableCode := ""
		if !hasQueryLang && cookieErr != nil {
			code, available := detectLanguage()
			if available {
				// ut-docs#1180 (CI-discovered): this used to redirect to a
				// bare "/setup?lang="+code, discarding every other query
				// param on the request — invisible in any environment where
				// $LANG/$LC_ALL is unset (detectLanguage returns
				// available=false and this branch never runs at all), which
				// is why it passed locally and in review but failed in CI's
				// runner (LANG=en_US.UTF-8, so this branch fires on the very
				// first GET). It silently dropped ?tax_country=/
				// ?tax_plugin_pending=1 on a first-ever visit with no
				// ut_lang cookie yet — exactly a fresh install's first click
				// of the new tax-plugin install button. Preserve the
				// original query string and only set/overwrite lang, so this
				// redirect stays transparent to tax_country today and to
				// whatever else a future step round-trips through GET
				// /setup tomorrow.
				q := r.URL.Query()
				q.Set("lang", code)
				http.Redirect(w, r, "/setup?"+q.Encode(), http.StatusSeeOther)
				return
			}
			// ut-docs#1110: a language the marketplace catalog already offers
			// is NOT "genuinely unavailable" — it must never pair the "we
			// don't have de yet" note with a working de install tile on the
			// very same screen (the card's own headline scenario,
			// reproduced by a second mechanism). Checking here is a cache
			// hit, not a second network round-trip: setupInstallableLanguages
			// serves the same TTL cache renderWizard reads from a few lines
			// into its own call below.
			langs, _ := setupInstallableLanguages(r.Context(), d)
			catalogHasCode := false
			for _, l := range langs {
				if l.Locale == code {
					catalogHasCode = true
					break
				}
			}
			if code != "" && !catalogHasCode {
				langUnavailableCode = code
				// Best-effort, per this wizard's standing pattern (see the
				// demo-data seed below): a failed write here must never block
				// rendering the wizard itself. Recorded for ut-docs#589's
				// child 3 (auto-file a board ticket for a missing language).
				if err := d.Settings.Set(r.Context(), "setup.detected_lang_unavailable", code); err != nil {
					logging.L().Errorf("setup wizard: persist detected unavailable locale: %v", err)
				}
			}
		}
		renderWizard(w, r, "", langUnavailableCode)
	})

	// ut-docs#1092: install a marketplace catalog language from the wizard's
	// step 1. Same auth-exempt, NeedsFirstBoot-gated tier as POST /api/setup
	// (pre-provisioning — no admin session exists yet). Not a /self-order
	// route, so the kiosk-engine guard doesn't apply.
	mux.HandleFunc("POST /api/setup/language", setupLanguageInstallHandler(d, svc))

	// ut-docs#1180: install a marketplace tax-capability plugin from the
	// wizard's Germany-only business-identity step (ADR-0025 decision 4 —
	// prompted, never silent). Same auth-exempt, NeedsFirstBoot-gated tier as
	// POST /api/setup/language above.
	mux.HandleFunc("POST /api/setup/tax-plugin", setupTaxPluginInstallHandler(d, svc))

	// ut-docs#1165: step 1's background "a newer version exists — update
	// before continuing?" check and its explicit apply action. Same
	// auth-exempt, NeedsFirstBoot-gated tier as POST /api/setup/language
	// above — no admin session exists yet at first boot, so the manager-gated
	// POST /api/update/check + /api/update/apply (update_api.go) can't be
	// reused directly here.
	mux.HandleFunc("POST /api/setup/update-check", setupUpdateCheckHandler(d, svc))
	mux.HandleFunc("POST /api/setup/update-apply", setupUpdateApplyHandler(d, svc))

	mux.HandleFunc("POST /api/setup", func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()

		// Restore-from-another-POS step (ut-docs#617): a pure UI/settings
		// choice, no network call — never blocks setup, offline-first is a
		// non-issue here by construction. "later" persists a flag so
		// Settings → Data can offer a resume link; "csv_excel" just changes
		// where the wizard lands post-setup, straight into the existing
		// /import flow instead of home. Anything else (including "no" and
		// the unset default) is a no-op.
		restoreChoice := strings.TrimSpace(r.PostFormValue("restore_choice"))
		// ut-docs#1168: set only when the operator actually browsed to a
		// file and previewed it inline on this step (web/ui/pages/setup.html
		// captures the staged_id the preview response embeds) — empty means
		// either "no"/"later"/never previewed, all of which keep today's
		// behaviour exactly.
		stagedImportID := strings.TrimSpace(r.PostFormValue("staged_import_id"))

		pin, pin2 := r.PostFormValue("pin"), r.PostFormValue("pin_confirm")
		if auth.ValidatePINFormat(pin) != nil {
			renderWizard(w, r, "auth.error.pin_format", "")
			return
		}
		if pin != pin2 {
			renderWizard(w, r, "auth.error.pin_mismatch", "")
			return
		}
		hash, err := auth.HashPIN(pin)
		if err != nil {
			renderWizard(w, r, "auth.error.pin_format", "")
			return
		}

		// Germany-only TSE business identity (ADR-0053, ut-docs#802):
		// validated up front, with the other pre-persist validations — a
		// partial or malformed submission re-renders the wizard on the
		// business-identity step before anything is saved. An entirely
		// blank step (skipped — the free tier brings its own fiscalisation,
		// ADR-0045) and a non-DE country both come back as a clean zero
		// identity with no error.
		tseIdentity, tseErrKey := parseTSEIdentityForm(r.Form.Get("country"), r.PostFormValue)
		if tseErrKey != "" {
			renderWizard(w, r, tseErrKey, "")
			return
		}

		// Locale/currency/tax — same application path as /api/settings/save.
		st := d.CurrentState()
		// web/ui/pages/setup.html's currencyTouched only flips true on a
		// genuine @change on the country select — see the ut-docs#970
		// comment just below for why a submitted non-blank currency alone
		// proves nothing. Named here (not just inlined) because
		// commitStagedImportForSetup, further down, reuses the exact same
		// signal for the exact same reason (ut-docs#1168 review, finding
		// 2): it must never confirm a currency the operator never actually
		// chose.
		currencyTouched := r.Form.Get("currency_touched") == "1"
		if v := strings.TrimSpace(r.Form.Get("currency")); v != "" && httpx.IsKnownCurrency(v) {
			st.Currency = v
			// Only mark confirmed (ut-docs#970) when the operator actually
			// interacted with the country select — country/currency start
			// PRE-FILLED from OS locale + timezone detection (ut-docs#590),
			// not from a choice, so a submitted non-blank value alone proves
			// nothing (review finding F3: this originally marked confirmed
			// on every completed wizard run, since the field is essentially
			// never blank).
			if currencyTouched {
				if err := d.Settings.Set(r.Context(), common.KeyCurrencyConfirmed, "true"); err != nil {
					http.Error(w, "setup failed", http.StatusInternalServerError)
					return
				}
			}
		}
		if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
			st.Country = v
			// ut-docs#1027: derive the locale from the country's own
			// country_settings row, server-side — never trust a client-
			// posted locale value for this (this endpoint is auth-exempt
			// during first boot). Blank DefaultLocale (OTHER, or a country
			// with no mapped default) leaves st.Locale exactly as
			// CurrentState() seeded it, same leave-alone contract as
			// currency/tax_rate_pct/tax_inclusive above.
			//
			// Review finding (blocker): plain UI text gracefully falls back
			// to English via I18n.T() when a language pack isn't installed
			// yet, but store.locale ALSO drives httpx.IsRTL (page direction)
			// and httpx.LocalizeDigits (number rendering) immediately and
			// unconditionally — neither has a translation-missing fallback.
			// An RTL locale (fa/ar/ur/...) whose base language isn't
			// actually installed would silently mirror the whole UI and
			// switch to Perso-/Eastern-Arabic digits while still showing
			// English text — worse than today's en-US, not better. A
			// non-RTL locale is always safe to preset (Latin digits, LTR
			// either way), which covers this card's own headline case (DE)
			// unconditionally; an RTL one only presets once its base
			// language is already available.
			if cs, ok, csErr := data.NewCountrySettingsRepo(d.Db).Get(r.Context(), v); csErr == nil && ok && localeSafeToPreset(cs.DefaultLocale) {
				st.Locale = cs.DefaultLocale
			}
		}
		if v := r.Form.Get("tax_rate_pct"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
				st.TaxRatePct = n
			}
		}
		st.TaxInclusive = r.Form.Get("tax_inclusive") != "off"
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			renderWizard(w, r, "setup.error.save_failed", "")
			return
		}
		d.SetState(st)
		httpx.InitCurrency(st.Currency)
		// ut-docs#1027: live-apply the just-derived locale, same posture as
		// InitCurrency above — SetDefaultLocale no-ops on an empty value, so
		// this is safe even when no country matched (st.Locale left at
		// CurrentState()'s own seed). Only affects ResolveLocale's final
		// fallback (no request-scoped ?lang=/ut_lang cookie yet); a fresh
		// wizard run's own step-1 language detection cookie, when present,
		// still wins for this browser, exactly as it does today.
		httpx.SetDefaultLocale(st.Locale)
		// Both engines: the kiosk's separate instance (ut-docs#449) must see
		// the same tax config or it would silently charge stale rates.
		newCfg := pos.Config{
			TaxInclusive:                 st.TaxInclusive,
			TaxRateBasisPoints:           st.TaxRatePct * 100,
			ServiceChargeRateBasisPoints: common.EffectiveServiceChargeRateBP(st),
		}
		d.Engine.SetConfig(newCfg)
		if d.KioskEngine != nil {
			d.KioskEngine.SetConfig(newCfg)
		}

		if name := strings.TrimSpace(r.Form.Get("store_name")); name != "" {
			if err := d.Settings.Set(r.Context(), "store.name", name); err != nil {
				http.Error(w, "setup failed", http.StatusInternalServerError)
				return
			}
		}
		if name := strings.TrimSpace(r.Form.Get("till_name")); name != "" {
			if err := d.Settings.Set(r.Context(), "till.name", name); err != nil {
				http.Error(w, "setup failed", http.StatusInternalServerError)
				return
			}
		}
		// Shop type (ut-docs#539, taxonomy per ADR-0026): optional, only a
		// known value is persisted — a garbage/absent value just leaves the
		// key unset, nothing else depends on it being present.
		if v := strings.TrimSpace(r.Form.Get("shop_type")); v != "" && isValidShopType(v) {
			if err := d.Settings.Set(r.Context(), common.KeyShopType, v); err != nil {
				http.Error(w, "setup failed", http.StatusInternalServerError)
				return
			}
		}
		if err := d.Settings.Set(r.Context(), "setup.completed", "true"); err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
		}

		// A fresh till needs a real usable register the moment onboarding
		// finishes, not just an admin user — the Shifts page's register
		// picker is driven entirely from real `registers` rows, and without
		// one Open Shift 500s on a FK constraint failure (ut-docs#429).
		if _, err := posRepo.EnsureRegister(r.Context()); err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
		}

		// Admin operator + session — the same first-boot semantics as
		// POST /api/auth/setup (which stays as the bare fallback).
		adminID, err := ensureFirstBootAdmin(r, svc)
		if err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
		}
		if err := svc.Repo().SetUserPIN(r.Context(), adminID, hash); err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
		}
		token, err := svc.CreateSession(r.Context(), adminID)
		if err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, adminID, "user", adminID, "first_boot_setup",
			map[string]string{"via": "wizard", "country": st.Country, "currency": st.Currency, "restore_choice": restoreChoice}, now, "")
		setSessionCookie(w, token, int(auth.SessionTTL.Seconds()))

		// Best-effort, same posture as the demo-data seed below: a failed
		// write here must never block wizard completion.
		if restoreChoice == "later" {
			if err := d.Settings.Set(r.Context(), common.KeyRestorePromptStatus, common.RestorePromptStatusDeferred); err != nil {
				logging.L().Errorf("setup wizard: persist restore-prompt deferred: %v", err)
			}
		}

		// Country base-plugin auto-install (ut-docs#591): best-effort, same
		// posture as restoreChoice/demo-data above — persists the pending
		// list before any network attempt, then makes one short-timeout
		// synchronous attempt; either way this never blocks or fails the
		// wizard's own response. A no-op for a country with nothing mapped.
		installBasePluginsForSetup(r.Context(), d, st.Country)

		// Eager store registration on explicit opt-in (ADR-0071, ut-docs#879):
		// same best-effort posture as the base-plugin install above — the
		// choice is persisted BEFORE the one time-boxed network attempt, the
		// attempt itself never blocks or fails the wizard's response, and a
		// "no"/absent answer changes nothing about today's lazy registration.
		// Truthy check matches the telemetry checkbox convention
		// (settings_page.go): "on" from the checkbox, "1" as the alternative.
		autoRegisterForSetup(r.Context(), d,
			r.Form.Get("auto_register") == "on" || r.Form.Get("auto_register") == "1")

		// German TSE provisioning kickoff (ADR-0053, ut-docs#802): same
		// best-effort posture as the base-plugin install above — the pending
		// state is persisted BEFORE the one time-boxed network attempt, so
		// the wizard finishes with no network and the background retry
		// (StartTSEProvisionRetry) picks it up. A no-op for a non-DE country
		// or a skipped identity step. fiscal.tse_configured is NOT touched
		// here — it only ever flips true on confirmed local receipt of the
		// operational credential (applyFiscalTSEReady).
		startTSEProvisioningForSetup(r.Context(), d, st.Country, tseIdentity)

		// Sample-data opt-in (ut-docs#539, extended to customers/promos by
		// ut-docs#567): checkbox default is unchecked. Best-effort by
		// design — the same reasoning as offline-first's "checkout is
		// never blocked by the network" extends to first boot: a failed
		// sample seed must never block wizard completion, so log and
		// continue rather than erroring after setup already succeeded.
		if r.Form.Get("demo_data") == "on" {
			seedRepo := data.NewDemoSeedRepo(d.Db)
			if err := seedRepo.SeedDemoCatalogue(r.Context()); err != nil {
				logging.L().Errorf("setup wizard: seed demo catalogue: %v", err)
			}
			if err := seedRepo.SeedDemoCustomersPromos(r.Context()); err != nil {
				logging.L().Errorf("setup wizard: seed demo customers/promos: %v", err)
			}
		}
		// ut-docs#617/#1168: "csv/excel" lands the new operator straight in
		// the catalog importer instead of home — no detour through
		// Settings/Catalog navigation. Every other choice keeps today's
		// behaviour. When the operator also previewed a file inline on this
		// step, try to finish the job for them: country/currency are saved
		// and the admin session now exists (both happened above), so the
		// preview can be replayed as a real commit — the same job the
		// operator would otherwise do by hand on /import a moment later.
		// Best-effort: a failed auto-commit falls back to exactly today's
		// "/import" detour, plus the staged_id so the already-previewed file
		// is one click away instead of a re-upload.
		//
		// ONLY attempted when currencyTouched (ut-docs#1168 review, finding
		// 2, blocker): the operator's country pick is the only thing that
		// stands in for the real ut-docs#970 currency-confirm prompt here,
		// and that pick started PRE-FILLED from OS locale detection, not a
		// choice — a completed wizard's currency is "confirmed" only if
		// they actually touched it. Auto-committing under an untouched
		// (guessed) currency would silently label every imported price
		// under it AND mark the till's currency confirmed for good,
		// suppressing every future manual import's real prompt too —
		// reversing #970's own review finding on the one path built to
		// avoid exactly that. When untouched, skip straight to the normal
		// fallback below: the operator hits /import, presses the real
		// Import button, and gets the genuine confirm prompt like anyone
		// else would.
		redirectTo := "/"
		if restoreChoice == "csv_excel" {
			redirectTo = "/import"
			if stagedImportID != "" {
				redirectTo = "/import?staged_id=" + url.QueryEscape(stagedImportID)
				if adminUser, ok := svc.Resolve(r.Context(), token); currencyTouched && ok &&
					commitStagedImportForSetup(r.Context(), mux, adminUser, stagedImportID, st.Currency) {
					// ut-docs#1168 review (nit): no page currently reads an
					// ?imported= query param, so land on the plain page
					// rather than promising an affordance that doesn't
					// exist yet.
					redirectTo = "/catalog"
				}
			}
		}
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
	})
}
