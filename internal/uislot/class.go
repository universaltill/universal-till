package uislot

// Class is how prominently a row (a Settings card, a Menu tile, an Items
// row, a rail entry) is offered. ADR-0190: a shop-type template may demote a
// row to Advanced or promote it to Essential; it never removes one. The zero
// value is invalid on purpose, so a row that forgot to declare a class fails
// the completeness guard instead of silently meaning "common".
type Class string

const (
	// ClassEssential rows are asked about or confirmed first for a shop.
	ClassEssential Class = "essential"
	// ClassCommon rows sit in their own category as today.
	ClassCommon Class = "common"
	// ClassAdvanced rows live under "More settings" only (still reachable).
	ClassAdvanced Class = "advanced"
)

// Valid reports whether c is one of the three declared classes. The zero
// value "" is invalid.
func (c Class) Valid() bool {
	switch c {
	case ClassEssential, ClassCommon, ClassAdvanced:
		return true
	}
	return false
}
