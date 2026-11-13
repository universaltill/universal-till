// Package catalogrules holds the few catalogue value rules the shared
// catimport parser and the till's catalog types must agree on: the item
// types, the net-quantity rule and the single-line name rule (ADR-0164 §1,
// ut-docs#4525, #5860). It is stdlib-only. internal/catalogtypes re-exports
// the item types and the net-quantity rule, and internal/data delegates
// CheckSingleLineName here, so the till, the parser and ut-cloud apply one
// definition.
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

// MaxReorderLevel is the sanity ceiling for items.reorder_level (ut-docs#276,
// ut-docs#2084): the catalog endpoint and the import both enforce it.
const MaxReorderLevel = 1_000_000

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
