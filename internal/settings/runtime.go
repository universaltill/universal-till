package settings

import (
	"context"
	"strconv"

	"github.com/universaltill/universal-till/internal/config"
)

func (s *Store) LoadRuntimeConfig(ctx context.Context, cfg *config.Config) {
	if theme, _, _ := s.Get(ctx, "theme"); theme != "" {
		cfg.Theme = theme
	}

	name, _, _ := s.Get(ctx, "store.name")
	if name != "" {
		cfg.StoreName = name
	}

	curr, _, _ := s.Get(ctx, "store.currency")
	if curr != "" {
		cfg.Locales.Currency = curr
	}

	locale, _, _ := s.Get(ctx, "store.locale")
	if locale != "" {
		cfg.Locales.Locale = locale
	}

	taxIncStr, _, _ := s.Get(ctx, "store.tax_inclusive")
	if taxIncStr != "" {
		cfg.Locales.TaxInclusive, _ = strconv.ParseBool(taxIncStr)
	}

	taxRateStr, _, _ := s.Get(ctx, "store.tax_rate")
	if taxRateStr != "" {
		cfg.Locales.TaxRate, _ = strconv.Atoi(taxRateStr)
	}

}

// SaveRuntimeConfig updates DB from a RuntimeConfig. All six keys are
// written in one transaction, so a mid-way failure never leaves a partial
// mix of old and new values behind.
func (s *Store) SaveRuntimeConfig(ctx context.Context, cfg *config.Config) error {
	return s.SetMany(ctx, map[string]string{
		"theme":               cfg.Theme,
		"store.name":          cfg.StoreName,
		"store.currency":      cfg.Locales.Currency,
		"store.locale":        cfg.Locales.Locale,
		"store.tax_inclusive": strconv.FormatBool(cfg.Locales.TaxInclusive),
		"store.tax_rate":      strconv.Itoa(cfg.Locales.TaxRate),
	})
}
