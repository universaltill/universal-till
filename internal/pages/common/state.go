package common

import (
	"context"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

const (
	KeyTheme          = "theme"
	KeyCurrency       = "store.currency"
	KeyCountry        = "store.country"
	KeyRegion         = "store.region"
	KeyTaxInclusive   = "store.tax_inclusive"
	KeyTaxRate        = "store.tax_rate"
	KeyUIScale        = "display.ui_scale"
	KeyOSK            = "display.osk"
	KeyIdleLock       = "auth.idle_lock_minutes"
	KeyKioskIdleReset = "kiosk.idle_reset_seconds"
)

// DefaultIdleLockMinutes locks an unattended till after 10 minutes unless
// configured otherwise (docs: pos-auth.md idle auto-lock).
const DefaultIdleLockMinutes = 10

// DefaultKioskIdleResetSeconds reloads an idle self-order kiosk back to its
// start screen after 60s unless configured otherwise (ADR-0020) — distinct
// from DefaultIdleLockMinutes, which revokes a cashier SESSION; the kiosk
// route is auth-exempt (no session to revoke).
const DefaultKioskIdleResetSeconds = 60

// LoadState pulls settings from the DB-backed settings store with cfg defaults.
func LoadState(ctx context.Context, store *settings.Store, cfg *config.Config) RuntimeState {
	get := func(key, def string) string {
		if v, ok, _ := store.Get(ctx, key); ok && strings.TrimSpace(v) != "" {
			return v
		}
		return def
	}

	st := RuntimeState{
		Theme:                  get(KeyTheme, cfg.Theme),
		Currency:               get(KeyCurrency, cfg.Locales.Currency),
		Country:                get(KeyCountry, "GB"),
		Region:                 get(KeyRegion, ""),
		TaxRatePct:             cfg.Locales.TaxRate,
		AllowNegativeInventory: false,
	}

	if v := get(KeyTaxInclusive, strconv.FormatBool(cfg.Locales.TaxInclusive)); v != "" {
		st.TaxInclusive, _ = strconv.ParseBool(v)
	}
	if v := get(KeyUIScale, ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			st.UIScale = f
		}
	}
	if v := get(KeyTaxRate, strconv.Itoa(cfg.Locales.TaxRate)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.TaxRatePct = n
		}
	}
	if v := get("pos.allow_negative_inventory", strconv.FormatBool(st.AllowNegativeInventory)); v != "" {
		st.AllowNegativeInventory, _ = strconv.ParseBool(v)
	}
	st.IdleLockMinutes = DefaultIdleLockMinutes
	if v := get(KeyIdleLock, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			st.IdleLockMinutes = n
		}
	}
	// On-screen keyboard: auto = only on touch screens (pointer: coarse).
	st.OSKMode = get(KeyOSK, "auto")

	st.KioskIdleResetSeconds = DefaultKioskIdleResetSeconds
	if v := get(KeyKioskIdleReset, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			st.KioskIdleResetSeconds = n
		}
	}

	return st
}

func SaveState(ctx context.Context, store *settings.Store, st RuntimeState) {
	_ = store.Set(ctx, KeyTheme, st.Theme)
	_ = store.Set(ctx, KeyCurrency, st.Currency)
	_ = store.Set(ctx, KeyCountry, st.Country)
	_ = store.Set(ctx, KeyRegion, st.Region)
	_ = store.Set(ctx, KeyTaxInclusive, strconv.FormatBool(st.TaxInclusive))
	_ = store.Set(ctx, KeyTaxRate, strconv.Itoa(st.TaxRatePct))
	if st.UIScale > 0 {
		_ = store.Set(ctx, KeyUIScale, strconv.FormatFloat(st.UIScale, 'f', -1, 64))
	}
	_ = store.Set(ctx, "pos.allow_negative_inventory", strconv.FormatBool(st.AllowNegativeInventory))
	_ = store.Set(ctx, KeyIdleLock, strconv.Itoa(st.IdleLockMinutes))
	_ = store.Set(ctx, KeyKioskIdleReset, strconv.Itoa(st.KioskIdleResetSeconds))
	if st.OSKMode != "" {
		_ = store.Set(ctx, KeyOSK, st.OSKMode)
	}
}

func BuildMenu(base []MenuItem, pm *plugins.Manager) []MenuItem {
	items := append([]MenuItem{}, base...)
	for _, p := range pm.MenuPlugins {
		if p.Route != "" && p.Label != "" {
			items = append(items, MenuItem{Href: p.Route, Label: p.Label})
		}
	}
	return items
}
