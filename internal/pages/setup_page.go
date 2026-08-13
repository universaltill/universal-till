package pages

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

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

var setupCountries = []setupCountry{
	{"GB", "setup.country.gb", "GBP", 20, true},
	{"IR", "setup.country.ir", "IRT", 10, true},
	{"US", "setup.country.us", "USD", 0, false},
	{"DE", "setup.country.de", "EUR", 19, true},
	{"FR", "setup.country.fr", "EUR", 20, true},
	{"ES", "setup.country.es", "EUR", 21, true},
	{"IT", "setup.country.it", "EUR", 22, true},
	{"NL", "setup.country.nl", "EUR", 21, true},
	{"TR", "setup.country.tr", "TRY", 20, true},
	{"AE", "setup.country.ae", "AED", 5, true},
	{"SA", "setup.country.sa", "SAR", 15, true},
	{"IN", "setup.country.in", "INR", 18, true},
	{"PK", "setup.country.pk", "PKR", 18, true},
	{"OTHER", "setup.country.other", "", 0, true},
}

// registerSetup wires the first-boot wizard: language → country (prefills
// currency/tax) → shop name → shop type → restore-from-another-POS?
// (ut-docs#617) → admin PIN → done. Every step has a sane default; both
// routes refuse to run once an operator exists (they are auth-exempt for
// exactly that window).
func registerSetup(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	posRepo := data.NewPOSRepo(d.Db)

	renderWizard := func(w http.ResponseWriter, r *http.Request, errKey string, langUnavailableCode string) {
		data := map[string]any{
			"countries": setupCountries,
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
		code := detectCountry()
		if r.Method == http.MethodPost {
			code = strings.ToUpper(strings.TrimSpace(r.PostFormValue("country")))
		}
		if code != "" {
			for _, c := range setupCountries {
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
				http.Redirect(w, r, "/setup?lang="+code, http.StatusSeeOther)
				return
			}
			if code != "" {
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

		// Locale/currency/tax — same application path as /api/settings/save.
		st := d.CurrentState()
		if v := strings.TrimSpace(r.Form.Get("currency")); v != "" && httpx.CurrencyByCode(v).Code == v {
			st.Currency = v
		}
		if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
			st.Country = v
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
		// Both engines: the kiosk's separate instance (ut-docs#449) must see
		// the same tax config or it would silently charge stale rates.
		newCfg := pos.Config{
			TaxInclusive:                 st.TaxInclusive,
			TaxRateBasisPoints:           st.TaxRatePct * 100,
			ServiceChargeRateBasisPoints: st.ServiceChargeRateBasisPoints,
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
		// ut-docs#617: "csv/excel" lands the new operator straight in the
		// existing catalog importer instead of home — no detour through
		// Settings/Catalog navigation. Every other choice keeps today's
		// behaviour.
		redirectTo := "/"
		if restoreChoice == "csv_excel" {
			redirectTo = "/import"
		}
		http.Redirect(w, r, redirectTo, http.StatusSeeOther)
	})
}
