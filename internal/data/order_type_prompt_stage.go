package data

import "strings"

// OrderTypePromptStageKey selects WHERE in the sale flow the cashier is
// asked for the sale-level dine-in/takeaway order type (ut-docs#2282).
// Unset (new install, or a pre-ut-docs#2282 database) reads as "" from the
// settings table, which NormalizeOrderTypePromptStage treats the same as
// OrderTypePromptStageCartTop (today's behaviour, unchanged) -- never as an
// error. Named "sale.*" to match the sibling SaleDisplayNoSchemeKey just
// above (both are per-shop sale-flow settings, same naming convention).
const OrderTypePromptStageKey = "sale.order_type_prompt_stage"

const (
	// OrderTypePromptStageCartTop is today's behaviour, unchanged: the
	// dine-in/takeaway toggle stays at the top of the basket, always
	// visible, changeable at any time. The default.
	OrderTypePromptStageCartTop = "cart_top"
	// OrderTypePromptStageBeforeSale asks once, in a modal, the moment the
	// cashier adds the FIRST item to an empty basket -- the basket-top
	// toggle still renders afterward and can still be changed.
	OrderTypePromptStageBeforeSale = "before_sale"
	// OrderTypePromptStageAtPay hides the basket-top toggle entirely; the
	// modal opens when the cashier presses Pay, and the tender screen only
	// opens once the modal is answered. Cancelling returns to the basket
	// unchanged.
	OrderTypePromptStageAtPay = "at_pay"
)

// NormalizeOrderTypePromptStage resolves a stored/submitted value to one of
// the three known stages, falling back to OrderTypePromptStageCartTop for
// anything else -- unset, empty, corrupted, or a future value this build
// doesn't know about. Same "unknown -> safe default" shape as
// parseDrawerPin/receiptPolicy's own fallbacks: a garbled setting must never
// be worse than never having set it, and cart_top (today's always-visible
// toggle) is the behaviour that predates this setting entirely.
func NormalizeOrderTypePromptStage(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case OrderTypePromptStageBeforeSale:
		return OrderTypePromptStageBeforeSale
	case OrderTypePromptStageAtPay:
		return OrderTypePromptStageAtPay
	default:
		return OrderTypePromptStageCartTop
	}
}
