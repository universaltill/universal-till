package settings

import (
	"context"
	"strconv"

	"github.com/universaltill/universal-till/internal/config"
)

func (s *Store) LoadRuntimeConfig(ctx context.Context, cfg *config.Config) {
	name, _, _ := s.Get(ctx, "store.name")
	if name != "" {
		cfg.StoreName = name
	}

	curr, _, _ := s.Get(ctx, "store.currency")
	if curr != "" {
		cfg.Locales.Currency = curr
	}

	curr_symbol, _, _ := s.Get(ctx, "store.currency_symbol")
	if curr != "" {
		cfg.Locales.CurrencySymbol = curr_symbol
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
		cfg.Locales.TaxRate, _ = strconv.Atoi(taxIncStr)
	}

}

// SaveRuntimeConfig updates DB from a RuntimeConfig.
func (s *Store) SaveRuntimeConfig(ctx context.Context, cfg *config.Config) error {
	if err := s.Set(ctx, "store.name", cfg.StoreName); err != nil {
		return err
	}
	if err := s.Set(ctx, "store.currency", cfg.Locales.Currency); err != nil {
		return err
	}
	if err := s.Set(ctx, "store.currency_symbol", cfg.Locales.CurrencySymbol); err != nil {
		return err
	}
	if err := s.Set(ctx, "store.locale", cfg.Locales.Locale); err != nil {
		return err
	}
	if err := s.Set(ctx, "pos.tax_inclusive", strconv.FormatBool(cfg.Locales.TaxInclusive)); err != nil {
		return err
	}
	if err := s.Set(ctx, "store.tax_rate", strconv.Itoa(cfg.Locales.TaxRate)); err != nil {
		return err
	}
	return nil
}
