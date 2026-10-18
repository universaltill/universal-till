package pages

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
)

// giftVoucherItemNameI18n is the i18n key setup names its gift voucher
// item with (seedDefaultGiftVoucherItem) and the rename reads.
const giftVoucherItemNameI18n = "catalog.item_type.gift_voucher"

// seededGiftVoucherRenamer wraps the translator the plugin manager
// publishes language-pack strings to (ut-docs#4398). After each publish —
// boot, and every install, enable, update or removal, the base-plugin
// background retry included — it renames the gift voucher item setup
// seeded in English before the shop's pack was live. One choke point
// rather than a call at every plugin lifecycle site.
type seededGiftVoucherRenamer struct {
	plugins.Localizer
	db *sql.DB
}

// SetPluginLocales forwards the layers, then renames the seeded item.
// syncLocales calls it outside any transaction, so the rename's own write
// transaction never nests in one.
func (l seededGiftVoucherRenamer) SetPluginLocales(pl config.PluginLocales) {
	l.Localizer.SetPluginLocales(pl)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	renameSeededGiftVoucherItem(ctx, l.db, httpx.DefaultLocale())
}

// renameSeededGiftVoucherItem gives the seeded gift voucher item the
// shop's word for it once locale has one, while the item still carries
// the English name it was seeded with (data.CatalogRepo.
// RenameSeededGiftVoucherItem decides "untouched"). An additional till
// leaves it alone: its catalogue is its main till's, and the rename
// reaches it from there. Logged, never returned: like the seed, it is
// best-effort.
func renameSeededGiftVoucherItem(ctx context.Context, db *sql.DB, locale string) {
	if db == nil {
		return
	}
	if primary, _, err := data.NewSettingsRepo(db).Get(ctx, "sync.primary_url"); err != nil || strings.TrimSpace(primary) != "" {
		return
	}
	from, to := httpx.T("en", giftVoucherItemNameI18n), httpx.T(locale, giftVoucherItemNameI18n)
	if to == giftVoucherItemNameI18n || to == from {
		return
	}
	renamed, err := data.NewCatalogRepo(db).RenameSeededGiftVoucherItem(ctx, from, to)
	if err != nil {
		logging.L().Warnf("gift voucher item: rename into %q: %v", locale, err)
		return
	}
	if renamed {
		logging.L().Infof("gift voucher item: renamed the setup item %q to %q now that %q is installed", from, to, locale)
	}
}
