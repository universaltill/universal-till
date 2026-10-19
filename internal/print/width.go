package print

import "unicode"

// displayWidth is the number of printer columns s takes: one per rune,
// except zero-width joiners (U+200C ZWNJ, U+200D ZWJ) and combining marks
// (Mn, Me — accents, Arabic harakat), which take none. Row padding and
// clipping measure with it, so a fa label like "باقی\u200cمانده" (it carries a
// ZWNJ) still right-aligns its amount with the other rows (ut-docs#4685).
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		if !zeroWidth(r) {
			n++
		}
	}
	return n
}

// zeroWidth reports whether r takes no printer column of its own.
func zeroWidth(r rune) bool {
	return r == '\u200c' || r == '\u200d' || unicode.In(r, unicode.Mn, unicode.Me)
}
