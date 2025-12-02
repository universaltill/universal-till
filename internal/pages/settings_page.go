package pages

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerSettings(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		all, _ := d.Settings.All(r.Context())
		data := map[string]any{
			"title":       "Settings",
			"theme":       d.State.Theme,
			"settings":    d.State,
			"settingsMap": all,
			"menuItems":   d.Menu,
		}
		httpx.Render("ui/pages/settings.html", data)(w, r)
	})

	mux.HandleFunc("/api/settings/theme", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.Form.Get("theme")); v != "" {
			d.State.Theme = v
			common.SaveState(r.Context(), d.Settings, d.State)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/settings/save", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.Form.Get("currency")); v != "" {
			d.State.Currency = v
		}
		if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
			d.State.Country = v
		}
		if v := strings.TrimSpace(r.Form.Get("region")); v != "" {
			d.State.Region = v
		}
		d.State.TaxInclusive = r.Form.Get("taxInclusive") == "on"
		d.State.AllowNegativeInventory = r.Form.Get("allowNegativeInventory") == "on"
		if v := r.Form.Get("taxRatePct"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				d.State.TaxRatePct = n
			}
		}
		common.SaveState(r.Context(), d.Settings, d.State)
		httpx.InitCurrency(d.State.Currency)
		resolver := ui.PriceResolverAdapter{Store: d.BtnStore}
		d.Engine = pos.NewServiceWithResolver(pos.Config{
			TaxInclusive:       d.State.TaxInclusive,
			TaxRateBasisPoints: d.State.TaxRatePct * 100,
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
		switch key {
		case common.KeyTheme:
			d.State.Theme = value
		case common.KeyCurrency:
			d.State.Currency = value
			httpx.InitCurrency(d.State.Currency)
		case common.KeyCountry:
			d.State.Country = value
		case common.KeyRegion:
			d.State.Region = value
		case common.KeyTaxInclusive:
			d.State.TaxInclusive = strings.ToLower(value) == "true" || value == "1" || value == "on"
			resolver := ui.PriceResolverAdapter{Store: d.BtnStore}
			d.Engine = pos.NewServiceWithResolver(pos.Config{
				TaxInclusive:       d.State.TaxInclusive,
				TaxRateBasisPoints: d.State.TaxRatePct * 100,
			}, resolver)
		case common.KeyTaxRate:
			if n, err := strconv.Atoi(value); err == nil {
				d.State.TaxRatePct = n
			}
		case "pos.allow_negative_inventory":
			d.State.AllowNegativeInventory = strings.ToLower(value) == "true" || value == "1" || value == "on"
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
