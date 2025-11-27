package pages

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerSettings(mux *http.ServeMux, d *deps) {
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		all, _ := d.settings.All(r.Context())
		data := map[string]any{
			"title":       "Settings",
			"theme":       d.state.Theme,
			"settings":    d.state,
			"settingsMap": all,
			"menuItems":   d.menu,
		}
		httpx.Render("ui/pages/settings.html", data)(w, r)
	})

	mux.HandleFunc("/api/settings/theme", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.Form.Get("theme")); v != "" {
			d.state.Theme = v
			saveState(r.Context(), d.settings, d.state)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/settings/save", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.Form.Get("currency")); v != "" {
			d.state.Currency = v
		}
		if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
			d.state.Country = v
		}
		if v := strings.TrimSpace(r.Form.Get("region")); v != "" {
			d.state.Region = v
		}
		d.state.TaxInclusive = r.Form.Get("taxInclusive") == "on"
		if v := r.Form.Get("taxRatePct"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				d.state.TaxRatePct = n
			}
		}
		saveState(r.Context(), d.settings, d.state)
		httpx.InitCurrency(d.state.Currency)
		resolver := ui.PriceResolverAdapter{Store: d.btnStore}
		d.engine = pos.NewServiceWithResolver(pos.Config{TaxInclusive: d.state.TaxInclusive}, resolver)
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
		if err := d.settings.Set(r.Context(), key, value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// reflect into state for known keys
		switch key {
		case keyTheme:
			d.state.Theme = value
		case keyCurrency:
			d.state.Currency = value
			httpx.InitCurrency(d.state.Currency)
		case keyCountry:
			d.state.Country = value
		case keyRegion:
			d.state.Region = value
		case keyTaxInclusive:
			d.state.TaxInclusive = strings.ToLower(value) == "true" || value == "1" || value == "on"
			resolver := ui.PriceResolverAdapter{Store: d.btnStore}
			d.engine = pos.NewServiceWithResolver(pos.Config{TaxInclusive: d.state.TaxInclusive}, resolver)
		case keyTaxRate:
			if n, err := strconv.Atoi(value); err == nil {
				d.state.TaxRatePct = n
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
