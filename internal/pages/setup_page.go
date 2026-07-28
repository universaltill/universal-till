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
	// ReducedTaxRatePct is the takeaway/reduced VAT rate (0 = not modeled
	// for this country yet — order type then never changes the default
	// rate, a safe no-op, not a claim that the country has no such rate).
	// Only DE is populated: real, verified German law (§12 UStG, 7%
	// reduced rate — see docs/germany-pos-parity-backlog.md). Other
	// countries' reduced/zero VAT rates were NOT researched for this
	// change and are deliberately left at 0 rather than guessed.
	ReducedTaxRatePct int
}

var setupCountries = []setupCountry{
	{"GB", "setup.country.gb", "GBP", 20, true, 0},
	{"IR", "setup.country.ir", "IRT", 10, true, 0},
	{"US", "setup.country.us", "USD", 0, false, 0},
	{"DE", "setup.country.de", "EUR", 19, true, 7},
	{"FR", "setup.country.fr", "EUR", 20, true, 0},
	{"ES", "setup.country.es", "EUR", 21, true, 0},
	{"IT", "setup.country.it", "EUR", 22, true, 0},
	{"NL", "setup.country.nl", "EUR", 21, true, 0},
	{"TR", "setup.country.tr", "TRY", 20, true, 0},
	{"AE", "setup.country.ae", "AED", 5, true, 0},
	{"SA", "setup.country.sa", "SAR", 15, true, 0},
	{"IN", "setup.country.in", "INR", 18, true, 0},
	{"PK", "setup.country.pk", "PKR", 18, true, 0},
	{"OTHER", "setup.country.other", "", 0, true, 0},
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
		st := d.UpdateState(func(s *common.RuntimeState) {
			if v := strings.TrimSpace(r.Form.Get("currency")); v != "" && httpx.CurrencyByCode(v).Code == v {
				s.Currency = v
			}
			if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
				s.Country = v
			}
			if v := r.Form.Get("tax_rate_pct"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
					s.TaxRatePct = n
				}
			}
			if v := r.Form.Get("reduced_tax_rate_pct"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
					s.ReducedTaxRatePct = n
				}
			}
			s.TaxInclusive = r.Form.Get("tax_inclusive") != "off"
		})
		common.SaveState(r.Context(), d.Settings, st)
		httpx.InitCurrency(st.Currency)
		d.Engine.SetConfig(pos.Config{
			TaxInclusive:              st.TaxInclusive,
			TaxRateBasisPoints:        st.TaxRatePct * 100,
			ReducedTaxRateBasisPoints: st.ReducedTaxRatePct * 100,
		})

		if name := strings.TrimSpace(r.Form.Get("store_name")); name != "" {
			_ = d.Settings.Set(r.Context(), "store.name", name)
		}
		_ = d.Settings.Set(r.Context(), "setup.completed", "true")

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
