package pages

import (
	"context"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

const (
	keyTheme        = "theme"
	keyCurrency     = "store.currency"
	keyCountry      = "store.country"
	keyRegion       = "store.region"
	keyTaxInclusive = "store.tax_inclusive"
	keyTaxRate      = "store.tax_rate"
)

type runtimeState struct {
	Theme        string
	Currency     string
	Country      string
	Region       string
	TaxInclusive bool
	TaxRatePct   int
}

// loadState pulls settings from the DB-backed settings store with cfg defaults.
func loadState(ctx context.Context, store *settings.Store, cfg *config.Config) runtimeState {
	get := func(key, def string) string {
		if v, ok, _ := store.Get(ctx, key); ok && strings.TrimSpace(v) != "" {
			return v
		}
		return def
	}

	st := runtimeState{
		Theme:      get(keyTheme, cfg.Theme),
		Currency:   get(keyCurrency, cfg.Locales.Currency),
		Country:    get(keyCountry, "GB"),
		Region:     get(keyRegion, ""),
		TaxRatePct: cfg.Locales.TaxRate,
	}

	if v := get(keyTaxInclusive, strconv.FormatBool(cfg.Locales.TaxInclusive)); v != "" {
		st.TaxInclusive, _ = strconv.ParseBool(v)
	}
	if v := get(keyTaxRate, strconv.Itoa(cfg.Locales.TaxRate)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.TaxRatePct = n
		}
	}

	return st
}

func saveState(ctx context.Context, store *settings.Store, st runtimeState) {
	_ = store.Set(ctx, keyTheme, st.Theme)
	_ = store.Set(ctx, keyCurrency, st.Currency)
	_ = store.Set(ctx, keyCountry, st.Country)
	_ = store.Set(ctx, keyRegion, st.Region)
	_ = store.Set(ctx, keyTaxInclusive, strconv.FormatBool(st.TaxInclusive))
	_ = store.Set(ctx, keyTaxRate, strconv.Itoa(st.TaxRatePct))
}

type menuItem struct {
	Href  string
	Label string
}

func buildMenu(base []menuItem, pm *plugins.Manager) []menuItem {
	items := append([]menuItem{}, base...)
	for _, p := range pm.MenuPlugins {
		if p.Route != "" && p.Label != "" {
			items = append(items, menuItem{Href: p.Route, Label: p.Label})
		}
	}
	return items
}
