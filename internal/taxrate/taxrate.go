// Package taxrate holds tiny, dependency-free helpers for basis-point tax
// rates shared across packages that would otherwise import-cycle.
//
// internal/pages imports both internal/data and internal/catimport;
// internal/catimport already imports internal/data (for ReadBkpProducts) —
// so internal/data cannot depend on internal/catimport, and neither is a
// natural home for a pure-formatting helper anyway. This package has no
// imports of its own beyond the standard library, so any of them can
// depend on it without creating a cycle (ut-docs#533).
package taxrate

import (
	"fmt"
	"strings"
)

// FormatPercent renders basis points as a plain percent string ("19",
// "19.5") — the exact shape ParseTaxRateBP (internal/catimport) reads back.
// Deliberately NOT money.Money/minorToDecimal: basis points are hundredths
// of a percent, a fixed scale of their own, not money decimals.
func FormatPercent(bp int) string {
	sign := ""
	if bp < 0 {
		sign, bp = "-", -bp
	}
	if bp%100 == 0 {
		return fmt.Sprintf("%s%d", sign, bp/100)
	}
	return sign + strings.TrimRight(fmt.Sprintf("%d.%02d", bp/100, bp%100), "0")
}
