package pages

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/builtinlayouts"
	"github.com/universaltill/universal-till/internal/taxrate"
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
	checkinAfterRegistration(d, enroll.EnsureRegistered(attemptCtx, d.Cfg, d.Settings)) // ADR-0148 §2
}

// setupCountry prefills currency + tax for the wizard's country step (docs
// repo: architecture/zero-touch-setup.md, phase B). Compact by design —
// "Other" keeps the defaults and everything stays editable in Settings.
type setupCountry struct {
	Code         string
	NameKey      string
	Currency     string
	TaxRateBP    int
	TaxRate      string // TaxRateBP as a percent string ("8.1"), the form's value
	TaxInclusive bool
}

// setupShopTypes is the ADR-0026 shop-type taxonomy (café, retail, service
// trade, hospitality, market stall/pop-up, other) — reused verbatim, not a
// new list (ut-docs#539). Labels are the setup.shop_type.* locale keys. The
// list lives in internal/plugins so the manifest's `layout.shop_type:<type>`
// capability validates against the same set (ADR-0129, ut-docs#3176).
var setupShopTypes = plugins.ShopTypes()

func isValidShopType(v string) bool { return plugins.IsKnownShopType(v) }

// wizardCountries reads the wizard's country list from country_settings
// (ut-docs#660) — the compile-time setupCountries slice that used to live
// here was moved into that table by ut-docs#659, and this is the read side
// finally catching up, so an admin's edits (or an operator-added country) in
// Settings → Country settings actually reach the one flow that most needs
// them (first-boot setup).
//
// TaxRateBP (basis points) is carried exactly and rendered with
// taxrate.FormatPercent ("20", "8.1") into the country step's prefill; the
// POST handler reads tax_rate_pct back with taxrate.ParsePercent into
// common.RuntimeState.TaxRateBP. Until ut-docs#3259 the till's default rate
// was whole-percent only, so a fractional country rate (Switzerland 8.1 %)
// was rounded here and silently lost.
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
// points, DB row) becomes setupCountry (the wizard's view model, carrying
// the same basis points plus their percent rendering) — shared by the live
// DB read and the builtin-defaults fallback so they can't drift from each
// other on rounding or OTHER-ordering.
func countrySettingsToSetupCountries(rows []data.CountrySetting) []setupCountry {
	out := make([]setupCountry, 0, len(rows))
	var other *setupCountry
	for _, r := range rows {
		sc := setupCountry{
			Code:         r.Code,
			NameKey:      r.NameKey,
			Currency:     r.Currency,
			TaxRateBP:    int(r.TaxRateBP),
			TaxRate:      taxrate.FormatPercent(int(r.TaxRateBP)), // exact, no rounding (ut-docs#3259)
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

// keyStoreNameRequired is the wizard error for a blank or placeholder shop
// name; it re-opens the wizard on the shop-name step (ut-docs#3096).
const keyStoreNameRequired = "setup.error.store_name_required"

// maxStoreNameRunes matches the shop-name inputs' maxlength="60".
const maxStoreNameRunes = 60

// isRefusedStoreName reports whether a first-boot shop name must be refused:
// blank, a till/cloud default (config.IsPlaceholderStoreName), or the name
// field's own placeholder text in the request's language, or longer than
// the field's maxlength (enforced here too: the form's cap is client-side
// only). Shared by the wizard and the bare POST /api/auth/setup fallback
// (ut-docs#3096, #3116).
func isRefusedStoreName(r *http.Request, name string) bool {
	return config.IsPlaceholderStoreName(name) ||
		utf8.RuneCountInString(strings.TrimSpace(name)) > maxStoreNameRunes ||
		strings.EqualFold(strings.TrimSpace(name), httpx.T(httpx.RequestLocale(r), "setup.store.placeholder"))
}

// registerSetup wires the first-boot wizard: language → country (prefills
// currency/tax) → business identity (DE only) → shop name → shop type →
// admin PIN → done. Every step has a sane default; both routes refuse to run
// once an operator exists (they are auth-exempt for exactly that window).
// Finishing lands on /import?welcome=1 (ut-docs#3709), where the new shop
// picks how to fill its catalog: import a file, load sample data, or skip.
func registerSetup(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	posRepo := data.NewPOSRepo(d.Db)

	renderWizard := func(w http.ResponseWriter, r *http.Request, errKey string, langUnavailableCode string) {
		// Best-effort, matching every other failure this wizard already
		// tolerates below (locale persist, plugin install, store
		// registration) — first boot must never become UNDOABLE because of a
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
					data["detectedTaxRate"] = c.TaxRate
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
		// ut-docs#1180: ADR-0025 decision 4 — a fiscal (tax) plugin match,
		// PROMPTED never silently installed. Same TTL-cached-catalog
		// posture as installableLangs just above.
		//
		// ut-docs#1460: resolved against tseProvisionCountry (the fixed
		// Germany country code step 3 itself is scoped to, ADR-0053), NOT
		// the wizard's currently-detected/posted `code`. This tile only
		// ever renders inside step 3, which is Germany-only regardless of
		// `code` (setup.html's `country === 'DE' ? 3 : 4` — the ONLY thing
		// that keeps this now-unconditionally-rendered markup off a non-DE
		// operator's screen; see TestSetupWizardShowsTaxPluginInstallTileForDEOnly's
		// pinned assertion on that exact ternary), so hardcoding the
		// country here doesn't leak the tile anywhere else. The bug this
		// replaced: resolving against `code` meant the tile depended on the
		// OS-detected country at the wizard's very first GET (ut-docs#590)
		// — step 2's country picker is pure client-side Alpine with no
		// server round-trip on a tile click, so a till whose OS
		// locale/timezone wasn't already German-tagged (the pilot café's
		// TECLAST tablet: en-GB/Europe/London) never got this tile's markup
		// into the DOM at all; no later hand-picked country could reveal
		// markup that was never rendered. Alpine's own `x-show="step ===
		// 3"` is what reveals it once the operator actually reaches step 3
		// — this only has to be resolved once, not re-resolved per pick.
		//
		// Side effect worth knowing about: this now runs the tax-catalog
		// fetch (setupTaxCatalogEntries, bounded by
		// setupTaxCatalogFetchTimeout, 5m TTL-cached) unconditionally on
		// every first-boot render, not only when the OS happened to detect
		// Germany — setupInstallableTaxPlugin used to short-circuit on the
		// countryTaxLocale miss before ever calling it. Bounded and cached,
		// same as installableLangs' own catalog fetch just above, and never
		// on the checkout path, so this doesn't violate offline-first — but
		// it is a small first-boot latency regression for the common
		// non-German till, worth knowing if setup ever feels slower. Since
		// ut-docs#1512 an unreachable catalog also puts the (Offline) tile's
		// markup in the DOM for every till; step 3's Germany-only routing
		// still keeps it off a non-DE operator's screen.
		//
		// The second return (catalogUnavailable) is deliberately NOT put in
		// data: ut-docs#1512 carries the offline state on the tile itself
		// (installableTaxPlugin.Offline), which drives step 3's "couldn't
		// check yet — we'll install it when you're online" note and button.
		taxPlugin, _ := setupInstallableTaxPlugin(r.Context(), d, tseProvisionCountry)
		data["installableTaxPlugin"] = taxPlugin
		// ut-docs#3244: no tile to render (no catalog match), but the
		// operator's consent is still queued — step 3 says so.
		if taxPlugin == nil {
			data["taxPluginQueued"] = setupTaxPluginQueued(r.Context(), d, tseProvisionCountry)
		}
		// tax_plugin_pending: set by POST /api/setup/tax-plugin's failure
		// redirect (query param, not stored state) — shows the "still
		// installing in the background" note once, on the page that
		// redirect lands on. Mirrors install_pending above.
		if r.URL.Query().Get("tax_plugin_pending") == "1" {
			data["taxPluginPending"] = true
		}
		// tax_plugin_skip_ack: set by POST /api/setup/tax-plugin-skip's
		// redirect (ut-docs#1506) — distinct from tax_plugin_pending above,
		// which only ever fires from a FAILED install attempt. This one means
		// the operator explicitly skipped a fiscal plugin the tile called
		// required; startStep below sends them on to step 4 (where "Skip for
		// now" always meant to land them), not back to step 3, while still
		// surfacing the stronger skip_warning copy on the step they land on.
		taxPluginSkipAck := resumeTaxCountry != "" && r.URL.Query().Get("tax_plugin_skip_ack") == "1"
		data["taxPluginSkipped"] = taxPluginSkipAck
		// Which step an error re-render lands on: business-identity errors
		// (setup.error.tse_*) belong to step 3, everything else (PIN, save)
		// to the PIN step (6). On a POST re-render the identity fields the
		// operator already typed are echoed back so a tax-number typo
		// doesn't cost them the whole step (the template attribute-escapes
		// these; same trust level as the country echo above).
		errStep := 6
		if strings.HasPrefix(errKey, "setup.error.tse_") {
			errStep = 3
		}
		if errKey == keyStoreNameRequired {
			errStep = 4 // the shop-name step (ut-docs#3096)
		}
		data["errStep"] = errStep
		// startStep is the step the wizard actually opens on: an error
		// re-render lands on errStep, an explicit tax-plugin skip (ut-docs#1506)
		// moves on to step 4, a tax-plugin install round-trip returns to step
		// 3 (the Germany-only business-identity step its tile lives on), and
		// everything else starts at 1.
		startStep := 1
		switch {
		case errKey != "":
			startStep = errStep
		case taxPluginSkipAck:
			startStep = 4
		case resumeTaxCountry != "":
			startStep = 3
		}
		data["startStep"] = startStep
		if r.Method == http.MethodPost {
			data["tseLegalName"] = strings.TrimSpace(r.PostFormValue("tse_legal_name"))
			data["tseOwnerName"] = strings.TrimSpace(r.PostFormValue("tse_owner_name"))
			data["tseTaxNumber"] = strings.TrimSpace(r.PostFormValue("tse_tax_number"))
			data["tseAddress"] = strings.TrimSpace(r.PostFormValue("tse_address"))
			// ut-docs#3096: a re-render (any error) keeps the shop and till
			// names from step 4. Dropping them let a PIN typo end with the
			// shop still called "My Store".
			// A refused placeholder ("My Store") is not echoed back, so
			// Next stays disabled until a real name is typed.
			if errKey != keyStoreNameRequired {
				data["storeName"] = strings.TrimSpace(r.PostFormValue("store_name"))
			}
			data["tillName"] = strings.TrimSpace(r.PostFormValue("till_name"))
		}
		// ut-docs#3872: plugin panels for setup.wizard.steps, asked in
		// parallel within the 2 s slot budget; "" when none answers.
		data["pluginSlot"] = renderSetupSlot(r, d, httpx.RequestLocale(r))
		httpx.RenderPartial("ui/pages/setup.html", data)(w, r)
	}

	mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil {
			// The first-boot wizard renders via RenderPartial with its own
			// standalone template (no base.html/nav rail; see setup.html),
			// before enrollment state even exists — httpx.RenderError's
			// operator layout doesn't apply here, and there's no sale
			// screen yet to route "Back to sale" to.
			http.Error(w, "setup unavailable", http.StatusInternalServerError) // page-error:allow pre-enrollment wizard has no base layout to render into
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
		// ut-docs#2135: a cookie that EXISTS but is no longer valid (recorded
		// against a superseded shop default or generation) is ignored when
		// resolving the locale, so it must not count as "an earlier visit"
		// here either — otherwise a stale jar suppresses detection and the
		// wizard renders its fallback language with nothing having chosen it.
		_, hasOverride := httpx.LocaleOverride(r)
		langUnavailableCode := ""
		if !hasQueryLang && !hasOverride {
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
				// base-plugin install in POST /api/setup): a failed write
				// here must never block
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

	// ut-docs#1506: the same step's "Skip for now" action, when it's leaving
	// the fiscal plugin above uninstalled — queues the SAME background retry
	// the install handler's failure branch does, so "we'll keep retrying"
	// holds regardless of which door the operator left through. Same
	// auth-exempt, NeedsFirstBoot-gated tier.
	mux.HandleFunc("POST /api/setup/tax-plugin-skip", setupTaxPluginSkipHandler(d, svc))

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

		// ut-docs#3096: the shop must be named — never left as migration
		// 001's seeded "My Store", the cloud's default, or the step's own
		// placeholder text. Checked with the other pre-persist validations,
		// so a refusal saves nothing.
		storeName := strings.TrimSpace(r.PostFormValue("store_name"))
		if isRefusedStoreName(r, storeName) {
			renderWizard(w, r, keyStoreNameRequired, "")
			return
		}

		// Locale/currency/tax — same application path as /api/settings/save.
		st := d.CurrentState()
		// web/ui/pages/setup.html's currencyTouched only flips true on a
		// genuine tap on a country tile — see the ut-docs#970 comment just
		// below for why a submitted non-blank currency alone proves nothing.
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
		// Percent 0–100, fractional allowed ("8.1", "8,1"): ut-docs#3259.
		if v := r.Form.Get("tax_rate_pct"); v != "" {
			if bp, ok := taxrate.ParsePercent(v); ok {
				st.TaxRateBP = bp
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
		// CurrentState()'s own seed).
		httpx.SetDefaultLocale(st.Locale)
		// ut-docs#2135: finishing the wizard is the shop stating its
		// language, so it retires the wizard's OWN step-1 ?lang= cookie
		// along with any other. That cookie used to win for this browser
		// and is why the bug existed: a shop clicked through setup in
		// English, and the till rendered English for a year afterwards
		// while Settings said German. The cost is deliberate and worth
		// naming — an operator who picked a language for THEMSELVES in
		// step 1, different from the shop's, has to pick it again after
		// setup (Menu → language, or ?lang=). The shop's own setting
		// winning is the whole point of the card.
		retireLocaleOverrides(r.Context(), d.Settings)
		// Both engines: the kiosk's separate instance (ut-docs#449) must see
		// the same tax config or it would silently charge stale rates.
		applyEngineConfig(r.Context(), d)

		if err := d.Settings.Set(r.Context(), "store.name", storeName); err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
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
			// ut-docs#1902: shop_type=service activates the builtin Salon
			// layout (ADR-0088) — hides Tables/Kitchen stations, relabels
			// Items to Services. Best-effort: a failure here must never
			// block finishing setup over a cosmetic menu personalization —
			// the shop still works, just with the generic everything-visible
			// menu until a later Settings save retries it.
			// ut-docs#2006: reload unless it's a genuine no-op (no error,
			// nothing changed) — an error still reloads, since a failed
			// reinstall can leave the DB changed (removeSalon succeeded)
			// even though Sync itself returned an error.
			changed, err := builtinlayouts.Sync(r.Context(), d.Db, v)
			if err != nil {
				logging.L().Warnf("setup: could not sync builtin layout for shop_type %q: %v", v, err)
			}
			if err != nil || changed {
				if err := d.ReloadPlugins(r.Context()); err != nil {
					logging.L().Warnf("setup: could not reload plugins after shop_type layout sync: %v", err)
				}
			}
			// ut-docs#3632: a retail/service shop has no eat-in, so it
			// starts with the dine-in/takeaway choice switched off.
			applyShopTypeOrderTypeDefault(r.Context(), d, v)
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
			map[string]string{"via": "wizard", "country": st.Country, "currency": st.Currency}, now, "")
		setSessionCookie(w, token, int(auth.SessionTTL.Seconds()))

		// Country base-plugin auto-install (ut-docs#591): best-effort —
		// persists the pending list before any network attempt, then makes
		// one short-timeout synchronous attempt; either way this never
		// blocks or fails the wizard's own response. A no-op for a country with nothing mapped.
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
		// or a skipped identity step. fiscal.signing_device_configured is NOT touched
		// here — it only ever flips true on confirmed local receipt of the
		// operational credential (applyFiscalTSEReady).
		startTSEProvisioningForSetup(r.Context(), d, st.Country, tseIdentity)

		// ut-docs#3709: the wizard always lands on the import page's welcome
		// mode, where the new shop picks how to fill its catalog (import a
		// file, load sample data, or skip). Sample data and importing used
		// to be wizard steps; both now live on /import only.
		//
		// ut-docs#1174 item D: a definitive TSE kickoff rejection during the
		// wizard's own synchronous attempt (not_configured /
		// subscription_inactive — fast synchronous cloud answers, not
		// flakiness) wins over the welcome page: it is a legal notice, and
		// only the sale screen renders it (index_page.go re-checks the REAL
		// stored state before rendering, so the param can never conjure a
		// warning that isn't true). Carried as a query param, no stored
		// state. A kickoff still pending/transient adds nothing — the
		// background ticker plus Settings already cover it, and /import
		// stays reachable from the catalog's Import button.
		redirectTo := "/import?welcome=1"
		if st, err := loadTSEProvisioningState(r.Context(), d); err == nil && st != nil && st.Status == tseStatusKickoffRejected {
			redirectTo = "/?tse_setup=rejected"
		}
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
	})
}
