package pages

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// isManagerOrAuthOff gates manager-only settings; with UT_AUTH=off there is
// no session to check, so dev/CI tooling passes.
func isManagerOrAuthOff(r *http.Request) bool {
	if auth.Disabled(os.Getenv("UT_AUTH")) {
		return true
	}
	u, ok := auth.FromContext(r.Context())
	return ok && u.IsManager()
}

func registerSettings(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		all, _ := d.Settings.All(r.Context())
		st := d.CurrentState()
		scale := st.UIScale
		if scale <= 0 {
			scale = 1
		}
		data := map[string]any{
			"title":       "Settings",
			"theme":       st.Theme,
			"themes":      availableThemes(r.Context(), d),
			"settings":    st,
			"settingsMap": all,
			"menuItems":   d.Menu,
			"uiScale":     strconv.FormatFloat(scale, 'f', -1, 64),
			"isManager":   isManagerOrAuthOff(r),
			"printer":     printerConfig(r.Context(), d),
		}
		httpx.Render("ui/pages/settings.html", data)(w, r)
	})

	// Idle auto-lock window (docs: pos-auth.md). Manager/admin only — an
	// unattended till's security posture is not a cashier decision.
	mux.HandleFunc("POST /api/settings/idle-lock", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		n, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("minutes")))
		if err != nil || n < 0 || n > 480 {
			http.Error(w, "minutes must be between 0 and 480", http.StatusBadRequest)
			return
		}
		st := d.UpdateState(func(s *common.RuntimeState) { s.IdleLockMinutes = n })
		common.SaveState(r.Context(), d.Settings, st)
		d.AuthSvc.SetIdleLockMinutes(n)
		if !auth.Disabled(os.Getenv("UT_AUTH")) {
			httpx.InitIdleLock(n)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Interface scale for this till's screen; saved and applied immediately.
	mux.HandleFunc("POST /api/settings/ui-scale", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f, err := strconv.ParseFloat(strings.TrimSpace(r.Form.Get("scale")), 64)
		if err != nil || f < 0.5 || f > 2.0 {
			http.Error(w, "scale must be between 0.5 and 2.0", http.StatusBadRequest)
			return
		}
		st := d.UpdateState(func(s *common.RuntimeState) { s.UIScale = f })
		common.SaveState(r.Context(), d.Settings, st)
		httpx.InitUIScale(f)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/settings/theme", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.Form.Get("theme")); v != "" {
			st := d.UpdateState(func(s *common.RuntimeState) { s.Theme = v })
			common.SaveState(r.Context(), d.Settings, st)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/settings/save", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		st := d.UpdateState(func(s *common.RuntimeState) {
			if v := strings.TrimSpace(r.Form.Get("currency")); v != "" {
				s.Currency = v
			}
			if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
				s.Country = v
			}
			if v := strings.TrimSpace(r.Form.Get("region")); v != "" {
				s.Region = v
			}
			s.TaxInclusive = r.Form.Get("taxInclusive") == "on"
			s.AllowNegativeInventory = r.Form.Get("allowNegativeInventory") == "on"
			if v := r.Form.Get("taxRatePct"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 {
					s.TaxRatePct = n
				}
			}
		})
		common.SaveState(r.Context(), d.Settings, st)
		httpx.InitCurrency(st.Currency)
		resolver := ui.PriceResolverAdapter{Store: d.BtnStore}
		d.Engine = pos.NewServiceWithResolver(pos.Config{
			TaxInclusive:       st.TaxInclusive,
			TaxRateBasisPoints: st.TaxRatePct * 100,
		}, resolver)
		w.WriteHeader(http.StatusNoContent)
	})

	// generic key/value upsert
	mux.HandleFunc("/api/settings/upsert", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		value := strings.TrimSpace(r.Form.Get("value"))
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		if err := d.Settings.Set(r.Context(), key, value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// reflect into state for known keys
		truthy := func(v string) bool { return strings.ToLower(v) == "true" || v == "1" || v == "on" }
		st := d.UpdateState(func(s *common.RuntimeState) {
			switch key {
			case common.KeyTheme:
				s.Theme = value
			case common.KeyCurrency:
				s.Currency = value
			case common.KeyCountry:
				s.Country = value
			case common.KeyRegion:
				s.Region = value
			case common.KeyTaxInclusive:
				s.TaxInclusive = truthy(value)
			case common.KeyTaxRate:
				if n, err := strconv.Atoi(value); err == nil {
					s.TaxRatePct = n
				}
			case "pos.allow_negative_inventory":
				s.AllowNegativeInventory = truthy(value)
			}
		})
		switch key {
		case common.KeyCurrency:
			httpx.InitCurrency(st.Currency)
		case common.KeyTaxInclusive:
			resolver := ui.PriceResolverAdapter{Store: d.BtnStore}
			d.Engine = pos.NewServiceWithResolver(pos.Config{
				TaxInclusive:       st.TaxInclusive,
				TaxRateBasisPoints: st.TaxRatePct * 100,
			}, resolver)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
