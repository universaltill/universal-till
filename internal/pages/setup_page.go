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
// currency/tax) → shop name → admin PIN → done. Every step has a sane
// default; both routes refuse to run once an operator exists (they are
// auth-exempt for exactly that window).
func registerSetup(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	posRepo := data.NewPOSRepo(d.Db)

	renderWizard := func(w http.ResponseWriter, r *http.Request, errKey string) {
		httpx.RenderPartial("ui/pages/setup.html", map[string]any{
			"countries": setupCountries,
			"errKey":    errKey,
		})(w, r)
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
		renderWizard(w, r, "")
	})

	mux.HandleFunc("POST /api/setup", func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()

		pin, pin2 := r.PostFormValue("pin"), r.PostFormValue("pin_confirm")
		if auth.ValidatePINFormat(pin) != nil {
			renderWizard(w, r, "auth.error.pin_format")
			return
		}
		if pin != pin2 {
			renderWizard(w, r, "auth.error.pin_mismatch")
			return
		}
		hash, err := auth.HashPIN(pin)
		if err != nil {
			renderWizard(w, r, "auth.error.pin_format")
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
			renderWizard(w, r, "setup.error.save_failed")
			return
		}
		d.SetState(st)
		httpx.InitCurrency(st.Currency)
		d.Engine.SetConfig(pos.Config{
			TaxInclusive:                 st.TaxInclusive,
			TaxRateBasisPoints:           st.TaxRatePct * 100,
			ServiceChargeRateBasisPoints: st.ServiceChargeRateBasisPoints,
		})

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
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}
