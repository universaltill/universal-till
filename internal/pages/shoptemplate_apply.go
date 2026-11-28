package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/shoptemplate"
)

// shopTemplateFor resolves the template for a shop type (nil = none). It is
// a seam: the integration wires it to the loaded, catalog-validated set
// (TemplateState, S5); until then, and in any build where the set failed to
// load, no template means nothing is seeded.
var shopTemplateFor = func(d *common.Deps, shopType string) *shoptemplate.Resolved {
	if d == nil || d.ShopTemplates == nil {
		return nil
	}
	return d.ShopTemplates.ForType(shopType)
}

// templateSettingsStore adapts the settings repository to
// shoptemplate.SettingsStore.
type templateSettingsStore struct{ d *common.Deps }

func (a templateSettingsStore) Get(ctx context.Context, k string) (string, bool, error) {
	return a.d.Settings.Get(ctx, k)
}

func (a templateSettingsStore) Set(ctx context.Context, k, v string) error {
	// settings-write:allow apply-once template seed; applyShopTemplate only runs on a main/single till (never one that follows a main till)
	return a.d.Settings.Set(ctx, k, v)
}

func (a templateSettingsStore) SetPerTill(ctx context.Context, k, v string) error {
	// settings-write:allow per-till seed (theme and its marker, data.PerTillSettingPrefixes); this till's own key
	return a.d.Settings.Set(ctx, k, v)
}

// applyShopTemplate seeds the shop type's apply-once defaults (ADR-0190,
// S6). Main or single till writes the ledger and shop-wide values; a till
// that follows a main till gets those through the admin bundle and seeds only
// its own per-till theme; local and
// offline; failure is logged and never blocks setup or a sale.
func applyShopTemplate(ctx context.Context, d *common.Deps, shopType string) {
	if d == nil || d.Settings == nil {
		return
	}
	r := shopTemplateFor(d, shopType)
	if r == nil {
		return
	}
	if tillFollowsMain(ctx, d) {
		// A satellite never writes shop-wide keys or the ledger (the main till
		// owns them); it seeds only its own per-till theme (design section 3
		// point 4).
		if err := shoptemplate.ApplyPerTill(ctx, templateSettingsStore{d}, r); err != nil {
			logging.L().Warnf("setup: seed per-till theme for shop_type %q: %v", shopType, err)
		}
		return
	}
	if err := shoptemplate.Apply(ctx, templateSettingsStore{d}, r); err != nil {
		logging.L().Warnf("setup: apply shop template %q for shop_type %q: %v", r.ID(), shopType, err)
		return
	}
	// Republish the live order-type mode when the template seeded it.
	if v, _, err := d.Settings.Get(ctx, data.OrderTypePromptModeKey); err == nil && v != "" {
		httpx.InitOrderTypePromptMode(v)
	}
}
