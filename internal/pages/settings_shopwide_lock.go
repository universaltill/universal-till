package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Read-only shop-wide settings while the main till is away (ut-docs#2981).
//
// An additional till refuses a shop-wide settings save while its main till
// is unreachable (saveShopSettings / saveStateThrough,
// settings_sync_proxy.go): the admin bundle is main-till-wins, so a local
// write would be overwritten by the next pull. Settings shows that up
// front: every region below renders inside
// `<fieldset class="set-lock" disabled>` (web/ui/pages/settings.html), which
// disables its native controls, still shows the current local value, and
// carries the settings.error.main_till_unreachable note. The server-side
// refusal stays — this is only the rendering.
//
// shopWideLockRegions maps each lockable region (its data-lock-region in
// settings.html) to ONE representative key that region's handler writes;
// TestShopWideLockRegions_MatchTemplateAndScope fails if a region is
// missing from the template or its key is not data.SettingShopWide.
// Locking is per form/region, not per card: some cards mix per-till and
// shop-wide controls (#settings-update: the Android update form is
// per-till, the update schedule is shop-wide).
//
// Deliberately left editable (per-till keys, or not a settings write):
// theme (theme), the Display card (display.*, incl. display.effects_*,
// window mode, launch on startup, exit to OS), the printer card
// (printer.*), this till's name and register (sync.till_name /
// sync.till_register_id on an additional till), the Android update form and
// "check for updates" (no settings key), enrolment (marketplace.device_*),
// backups, diagnostics, the TSE block (it already defers to the main till,
// tseFollowsMain), demo-data clean-up (catalogue rows, not settings) and
// the pending base-plugin dismiss (setup.pending_base_plugins, per-till).
// The raw key editor (#settings-all) is out of scope.
var shopWideLockRegions = map[string]string{
	"settings-auto-register":                  common.KeyAutoRegisterOptIn,                // POST /api/settings/auto-register
	"settings-update-schedule":                keyAutoUpdateEnabled,                       // POST /api/settings/update-schedule (update.auto_enabled + update.auto_time)
	"settings-payments":                       "payments.default_method",                  // payments-default; payments-fee writes payments.fee.<method>
	"settings-order-no":                       data.SaleDisplayNoSchemeKey,                // POST /api/settings/order-no-scheme
	"settings-order-type-prompt":              data.OrderTypePromptModeKey,                // POST /api/settings/order-type-prompt
	"settings-barcode":                        data.BarcodeEnabledSymbologiesKey,          // POST /api/settings/barcode-symbology
	"settings-catalog-import-barcode-default": data.CatalogImportBarcodeFromSKUDefaultKey, // POST /api/settings/catalog-import-barcode-default
	"settings-catalog-pre-pack-unit-price":    data.CatalogPrePackUnitPriceEnabledKey,     // POST /api/settings/catalog-pre-pack-unit-price
	"settings-sell-screen":                    common.KeyBrowsingMode,                     // POST /api/settings/browsing-mode
	"settings-stock-tracking":                 common.KeyAllowNegativeInventory,           // POST /api/settings/allow-negative-inventory
	"settings-restore-prompt":                 common.KeyRestorePromptStatus,              // POST /api/settings/dismiss-restore-prompt
	"settings-retention":                      common.KeyReportRetentionMode,              // POST /api/settings/report-retention
	"settings-invoice":                        keyInvoiceSellerName,                       // POST /api/settings/invoice (invoice.seller_*)
	"settings-idle-lock":                      common.KeyIdleLock,                         // POST /api/settings/idle-lock
	"settings-kiosk-idle-reset":               common.KeyKioskIdleReset,                   // POST /api/settings/kiosk-idle-reset
	"settings-kiosk-payment-mode":             common.KeyKioskPaymentMode,                 // POST /api/settings/kiosk-payment-mode
	"settings-telemetry":                      "marketplace.telemetry_opt_in",             // POST /api/settings/telemetry
	"settings-store-name":                     common.KeyStoreName,                        // POST /api/settings/store-name
	"settings-currency":                       common.KeyCurrency,                         // POST /api/settings/save (currency form)
	"settings-language":                       common.KeyLocale,                           // POST /api/settings/save (locale form)
	"settings-staff-languages":                common.KeyStaffLocales,                     // POST /api/settings/staff-languages
	"settings-shop-type":                      common.KeyShopType,                         // POST /api/settings/shop-type
}

// lockedSettingsRegions is the set of shop-wide regions to render read-only
// now: all of them on an additional till whose main till counts as
// unreachable, none otherwise. Never nil (settings.html indexes it). It
// reads the status chip's cached link state (replicaLinkView: settings
// rows, the link client's and PrimaryWatch's in-memory state) — no network
// probe, so a dead main till never slows the page down.
func lockedSettingsRegions(ctx context.Context, d *common.Deps) map[string]bool {
	out := map[string]bool{}
	if !tillFollowsMain(ctx, d) || replicaLinkView(ctx, d).State != linkUnreachable {
		return out
	}
	for id := range shopWideLockRegions {
		out[id] = true
	}
	return out
}
