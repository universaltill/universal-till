package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// validOrderTypePromptMode reports whether mode is one of the four
// sale.order_type_prompt values (ut-docs#2282, "off" ut-docs#3632). The
// dedicated settings endpoint and the cloud settings door both refuse
// anything else; the generic upsert leans on InitOrderTypePromptMode's
// fallback to "top" instead.
func validOrderTypePromptMode(mode string) bool {
	switch mode {
	case data.OrderTypePromptModeTop, data.OrderTypePromptModeBeforeItem,
		data.OrderTypePromptModeAtPay, data.OrderTypePromptModeOff:
		return true
	}
	return false
}

// eodOrderTypesForDisplay returns the "by order type" EOD rows to show, or
// nil to hide the section (ut-docs#3632): a shop with the order type
// switched off only ever has "" (dine-in) lines, so a one-row "Dine in"
// breakdown is noise. A takeaway row (a sale rung up before the switch, or
// a synced legacy peer) keeps the section so its revenue stays visible.
func eodOrderTypesForDisplay(rows []data.OrderTypeSales) []data.OrderTypeSales {
	if !httpx.OrderTypeOff() {
		return rows
	}
	for _, r := range rows {
		if r.OrderType == pos.OrderTypeTakeaway {
			return rows
		}
	}
	return nil
}

// orderTypeOffShopTypes are the ADR-0026 shop types that, by default, don't
// sell food or drink to eat in (ut-docs#3632). The setup wizard starts them
// with sale.order_type_prompt = "off"; a shop can switch it back on in
// Settings at any time.
var orderTypeOffShopTypes = map[string]bool{"retail": true, "service": true}

// applyShopTypeOrderTypeDefault persists "off" for a retail/service shop
// finishing the setup wizard, unless sale.order_type_prompt is already set
// (an explicit choice, or a restored/joined database, is never overwritten).
// Best-effort like the shop-type layout sync next to it: a failure only
// leaves the documented default ("top") in place, so it is logged, never
// fatal to finishing setup.
func applyShopTypeOrderTypeDefault(ctx context.Context, d *common.Deps, shopType string) {
	if !orderTypeOffShopTypes[shopType] {
		return
	}
	cur, _, err := d.Settings.Get(ctx, data.OrderTypePromptModeKey)
	if err != nil {
		logging.L().Warnf("setup: read %s: %v", data.OrderTypePromptModeKey, err)
		return
	}
	if cur != "" {
		return
	}
	// settings-write:allow first-boot wizard only (setup_page.go): runs before this till follows a main till
	if err := d.Settings.Set(ctx, data.OrderTypePromptModeKey, data.OrderTypePromptModeOff); err != nil {
		logging.L().Warnf("setup: set %s=off for shop_type %q: %v", data.OrderTypePromptModeKey, shopType, err)
		return
	}
	httpx.InitOrderTypePromptMode(data.OrderTypePromptModeOff)
}
