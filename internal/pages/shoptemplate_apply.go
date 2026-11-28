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

// seedJoinedTillTemplate seeds the per-till template keys (the theme) of a
// till that joined a main till after setup (ADR-0190, ADR-0171 §4). The join
// replaces this till's database with the main till's snapshot at restart, so
// the first-run wizard's applyShopTemplate never ran here; the shop type
// arrives with the snapshot or a later admin pull. Main/single tills do
// nothing (the wizard or the type-change flow owns their seeding), and
// applyShopTemplate sends a satellite only to ApplyPerTill, which is
// once-per-till (marker) and never writes a shop-wide key or the ledger.
func seedJoinedTillTemplate(ctx context.Context, d *common.Deps) {
	if d == nil || d.Settings == nil || !tillFollowsMain(ctx, d) {
		return
	}
	shopType, _, err := d.Settings.Get(ctx, common.KeyShopType)
	if err != nil || shopType == "" {
		return
	}
	r := shopTemplateFor(d, shopType)
	if r == nil {
		return
	}
	// ApplyPerTill directly, not applyShopTemplate: a promotion clearing the
	// primary URL between the check above and a second tillFollowsMain check
	// would otherwise run the full main-till Apply (ledger + shop-wide keys).
	before, _, _ := d.Settings.Get(ctx, common.KeyTheme)
	if err := shoptemplate.ApplyPerTill(ctx, templateSettingsStore{d}, r); err != nil {
		logging.L().Warnf("join: seed per-till theme for shop_type %q: %v", shopType, err)
		return
	}
	// A seeded theme is effective now, not at the next restart: republish it to
	// the live state and the cached error-page theme (as the settings save does).
	if after, _, err := d.Settings.Get(ctx, common.KeyTheme); err == nil && after != "" && after != before {
		d.UpdateState(func(s *common.RuntimeState) { s.Theme = after })
		httpx.InitTheme(after)
	}
}
