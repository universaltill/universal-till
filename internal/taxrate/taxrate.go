// Package taxrate holds tiny, dependency-free helpers for basis-point tax
// rates. Neither internal/data nor internal/catimport is a natural home for
// a pure-formatting helper shared between internal/pages callers of both —
// and internal/catimport already imports internal/data (for
// ReadBkpProducts), so internal/data specifically cannot depend on it. This
// package has no imports of its own beyond the standard library, so any of
// them can depend on it without creating a cycle (ut-docs#533).
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

// MaxBP is the upper bound ParsePercent accepts: 100 %.
const MaxBP = 10000

// ParsePercent is FormatPercent's inverse (ut-docs#3259): it reads a plain
// percent string ("19", "8.1", German "8,10") into basis points. Integer
// arithmetic only — no strconv.ParseFloat, so 8.1 % is exactly 810 bp and
// "1e3"/"NaN"/"Inf" are refused. Grammar after trimming spaces: one or more
// ASCII digits, optionally followed by '.' or ',' and at most two digits.
// No sign, no exponent, no bare fraction (".5" — write "0.5"). The result
// must lie in 0–MaxBP (0–100 %); anything else is !ok.
func ParsePercent(s string) (bp int, ok bool) {
	s = strings.TrimSpace(s)
	whole, frac := s, ""
	if i := strings.IndexAny(s, ".,"); i >= 0 {
		whole, frac = s[:i], s[i+1:]
	}
	if whole == "" || len(frac) > 2 || !allDigits(whole) || !allDigits(frac) {
		return 0, false
	}
	// Leading zeros are harmless ("025" is 25 %), but cap the length so the
	// accumulation below can't overflow before the range check.
	whole = strings.TrimLeft(whole, "0")
	if len(whole) > 3 {
		return 0, false
	}
	for len(frac) < 2 {
		frac += "0"
	}
	for _, c := range whole + frac {
		bp = bp*10 + int(c-'0')
	}
	if bp > MaxBP {
		return 0, false
	}
	return bp, true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
