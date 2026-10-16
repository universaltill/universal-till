// Package catalogrules holds the few catalogue value rules the shared
// catimport parser and the till's catalog types must agree on: the item
// types and the net-quantity rule (ADR-0164 §1, ut-docs#4525). It is
// stdlib-only. internal/catalogtypes re-exports these, so the till, the
// parser and ut-cloud apply one definition.
package catalogrules

// Item types (ut-docs#4214, 071_items_item_type.sql).
const (
	ItemTypeStandard    = "standard"
	ItemTypeGiftVoucher = "gift_voucher"
)

// Net-quantity units (ut-docs#3391).
const (
	NetQuantityGrams       = "g"
	NetQuantityMillilitres = "ml"
	NetQuantityEach        = "ea"
)

// ValidNetQuantity reports whether a value/unit pair may be stored: both
// absent, or a positive whole value with one of the known units.
func ValidNetQuantity(value *int64, unit *string) bool {
	if value == nil && unit == nil {
		return true
	}
	if value == nil || unit == nil || *value <= 0 {
		return false
	}
	switch *unit {
	case NetQuantityGrams, NetQuantityMillilitres, NetQuantityEach:
		return true
	}
	return false
}
