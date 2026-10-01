package settings

import (
	"context"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/taxrate"
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

	// store.tax_rate is a percent string, fractional allowed ("8.1"),
	// ut-docs#3259. An unreadable value leaves cfg's default in place for
	// this run; SaveRuntimeConfig then leaves the stored value alone.
	if v, err := s.storedTaxRate(ctx); err == nil {
		if bp, ok := taxrate.ParsePercent(v); ok {
			cfg.Locales.TaxRateBP = bp
		}
	}
}

func (s *Store) storedTaxRate(ctx context.Context) (string, error) {
	v, _, err := s.Get(ctx, "store.tax_rate")
	return strings.TrimSpace(v), err
}

// SaveRuntimeConfig updates DB from a RuntimeConfig. All six keys are
// written in one transaction, so a mid-way failure never leaves a partial
// mix of old and new values behind.
//
// store.tax_rate is written with taxrate.FormatPercent ("19" for a whole
// rate — byte-identical to earlier builds — "8.1" for a fractional one).
// app.Run calls Load then Save on every boot, so a stored rate this build
// cannot parse is NOT overwritten with the config default: before
// ut-docs#3259 a fractional "8.1" failed strconv.Atoi and the boot
// round-trip silently wrote UT_TAX_RATE's "20" over the shop's own rate.
func (s *Store) SaveRuntimeConfig(ctx context.Context, cfg *config.Config) error {
	kv := map[string]string{
		"theme":               cfg.Theme,
		"store.name":          cfg.StoreName,
		"store.currency":      cfg.Locales.Currency,
		"store.locale":        cfg.Locales.Locale,
		"store.tax_inclusive": strconv.FormatBool(cfg.Locales.TaxInclusive),
		"store.tax_rate":      taxrate.FormatPercent(cfg.Locales.TaxRateBP),
	}
	// A failed read is treated like an unparseable value: without knowing
	// what is stored, writing the config default could clobber the shop's
	// real rate (review of ut-docs#3259).
	if cur, err := s.storedTaxRate(ctx); err != nil {
		delete(kv, "store.tax_rate")
		logging.L().Warnf("settings: read store.tax_rate: %v — leaving it unchanged", err)
	} else if cur != "" {
		if _, ok := taxrate.ParsePercent(cur); !ok {
			delete(kv, "store.tax_rate")
			logging.L().Warnf("settings: store.tax_rate %q is not a 0–100 %% rate with at most two decimals — the till uses the default %s %% until it is set again", cur, taxrate.FormatPercent(cfg.Locales.TaxRateBP))
		}
	}
	return s.SetMany(ctx, kv)
}
