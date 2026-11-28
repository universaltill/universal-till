package pages

import (
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
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
