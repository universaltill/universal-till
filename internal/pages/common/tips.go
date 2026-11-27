package common

import (
	"context"
	"strings"

	"github.com/universaltill/universal-till/internal/pos"
)

const (
	// KeyTipsEnabled turns the cashier-facing tip entry at checkout on or
	// off ("1"/"0"). Unset follows the shop type: on for a service shop.
	KeyTipsEnabled = "payments.tips_enabled"
	// KeyTipPresets is the comma-separated list of percentage presets the
	// tip row offers ("10,15,20"; up to two decimals each, at most five).
	KeyTipPresets = "payments.tip_presets"
)

// settingReader is the read half of the settings store.
type settingReader interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

// TipsEnabled reports whether the till offers tip entry at checkout: the
// shop's explicit choice if it made one, otherwise on for the service shop
// type and off for every other.
func TipsEnabled(ctx context.Context, s settingReader) bool {
	if s == nil {
		return false
	}
	if v, ok, _ := s.Get(ctx, KeyTipsEnabled); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "on":
			return true
		case "0", "false", "off":
			return false
		}
	}
	shopType, _, _ := s.Get(ctx, "shop.type")
	return strings.EqualFold(strings.TrimSpace(shopType), "service")
}

// TipPresetBPs is the shop's tip preset percentages in basis points.
func TipPresetBPs(ctx context.Context, s settingReader) []int64 {
	raw := ""
	if s != nil {
		raw, _, _ = s.Get(ctx, KeyTipPresets)
	}
	return pos.ParseTipPresets(raw)
}
