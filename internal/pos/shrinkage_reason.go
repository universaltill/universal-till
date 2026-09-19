package pos

// Shrinkage reason categories (ut-docs#1465, G41): the fixed, pre-tender
// vocabulary for WHY a basket line was removed instead of sold — distinct
// from a post-tender refund (G27, internal/pages/refund_page.go), which
// covers a completed sale being returned. Mirrors how OrderStatus* in
// order_status.go declares its own fixed vocabulary alongside a
// ValidOrderStatus-style helper and an "every value" slice: core owns the
// vocabulary, the data/handler/UI layers just render and persist it.
//
//   - ShrinkageReasonVoid: a mis-ring or an order change — the item was
//     never going to be handed to (or kept by) the customer as rung up.
//   - ShrinkageReasonComp: a goodwill zero-charge to the customer — the
//     item WAS handed over, but the shop chose not to charge for it.
//   - ShrinkageReasonWaste: spoilage, a kitchen mistake, or a dropped item
//     — the item never reached a customer at all.
//
// Fixed at exactly these three for v1 (non-goal: configurable/shop-specific
// reason categories) — see the card's own non-goals list.
const (
	ShrinkageReasonVoid  = "void"
	ShrinkageReasonComp  = "comp"
	ShrinkageReasonWaste = "waste"
)

// ShrinkageReasons is the fixed vocabulary, in the order the reason-picker
// UI presents them.
var ShrinkageReasons = []string{ShrinkageReasonVoid, ShrinkageReasonComp, ShrinkageReasonWaste}

// ValidShrinkageReason reports whether reason is one of the three known
// categories — used by /api/pos/remove to reject anything else with a 400
// rather than silently recording junk in shrinkage_events.reason_category.
func ValidShrinkageReason(reason string) bool {
	switch reason {
	case ShrinkageReasonVoid, ShrinkageReasonComp, ShrinkageReasonWaste:
		return true
	default:
		return false
	}
}
