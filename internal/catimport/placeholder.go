package catimport

import (
	"strings"
	"unicode"
)

// Placeholder thumbnails (ut-docs#1189 Phase 1): an item imported with no
// source image gets a bundled generic category icon instead of a blank
// tile — offline, instant, zero AI/network, matching what SumUp/Square/
// Loyverse actually ship (a generic category icon, not a per-item
// generated photo — the research-first check #1054 this card's own body
// asks for). Phase 2 (a real per-item photo via opt-in cloud-side
// enrichment) is a separate, gated follow-up — see the card.
//
// keywordIcons is the curated keyword→icon table, matched against whole
// WORDS (see tokenize), not raw substrings — a plain strings.Contains
// first draft of this table matched "tea" inside "Steak Sandwich" and
// "cola" inside "Chocolate Bar" (review finding F5, ut-docs#1189), so an
// entirely unrelated item silently got the wrong icon. Order still
// matters when a name legitimately contains two different keywords (e.g.
// "Iced Chai Latte" has both "chai" and "latte"): the first matching
// entry wins, so a more specific keyword should sit before a broader one
// it would otherwise be shadowed by. Extending this list needs no other
// code change (the card's own acceptance criterion) — add a row here.
// Known limitation (review finding F6): this table is English-only, so a
// shop cataloguing in Persian/Arabic/Turkish (e.g. "قهوة", "Türk Kahvesi")
// gets "generic" for everything — acceptable for Phase 1's offline,
// zero-translation scope, but worth knowing rather than assuming full
// multilingual coverage.
var keywordIcons = []struct {
	keyword, icon string
}{
	{"coffee", "coffee"},
	{"cappuccino", "coffee"},
	{"latte", "coffee"},
	{"espresso", "coffee"},
	{"chai", "coffee"},
	{"mocha", "coffee"},
	{"americano", "coffee"},
	{"tea", "coffee"},
	{"bagel", "pastry"},
	{"croissant", "pastry"},
	{"muffin", "pastry"},
	{"pastry", "pastry"},
	{"cake", "pastry"},
	{"donut", "pastry"},
	{"doughnut", "pastry"},
	{"cookie", "pastry"},
	{"bakery", "pastry"},
	{"sandwich", "sandwich"},
	{"panini", "sandwich"},
	{"baguette", "sandwich"},
	{"wrap", "sandwich"},
	{"burger", "sandwich"},
	{"cola", "drink"},
	{"soda", "drink"},
	{"juice", "drink"},
	{"water", "drink"},
	{"lemonade", "drink"},
	{"smoothie", "drink"},
	{"drink", "drink"},
	{"beverage", "drink"},
}

// PlaceholderIcon picks an icon key for an item with no source image,
// matching (case-insensitively, whole-word) against the item's name first,
// then falling back to its category, then to "generic" when nothing
// matches either. Name is checked first because it's the more specific
// signal — a "Chai Latte" in an uncategorised import still resolves to
// coffee, but a recognised category (e.g. "Coffee") still catches an item
// whose own name is a house-blend number with no matching keyword (see
// TestPlaceholderIcon_CategoryOutranksAmbiguousName).
func PlaceholderIcon(name, category string) string {
	if icon, ok := matchKeyword(name); ok {
		return icon
	}
	if icon, ok := matchKeyword(category); ok {
		return icon
	}
	return "generic"
}

func matchKeyword(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	words := tokenize(s)
	for _, ki := range keywordIcons {
		if words[ki.keyword] {
			return ki.icon, true
		}
	}
	return "", false
}

// tokenize lowercases s and splits it into whole words (a maximal run of
// letters/digits is one word; anything else — spaces, hyphens, commas,
// punctuation — is a separator), returned as a set for O(1) membership
// checks. "Coca-Cola" → {"coca","cola"}; "Steak Sandwich" → {"steak",
// "sandwich"} (crucially NOT matching "tea", which a naive substring
// check found inside "s-TEA-k" — see keywordIcons' doc comment, F5).
func tokenize(s string) map[string]bool {
	words := map[string]bool{}
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			words[strings.ToLower(b.String())] = true
			b.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return words
}

// iconPath maps an icon key to its bundled asset's public path, empty for
// an unknown key. The bundled set itself lives at
// web/public/assets/category-icons/*.svg.
func iconPath(icon string) string {
	switch icon {
	case "coffee", "pastry", "sandwich", "drink", "generic":
		return "/public/assets/category-icons/" + icon + ".svg"
	default:
		return ""
	}
}

// PlaceholderIconPath is PlaceholderIcon plus iconPath in one call — what
// callers actually want: a path ready to store as an item_images.path
// value (see data.CatalogRepo.EnsureDefaultThumbnail), through the same
// "/public/assets/..." convention the manual upload path already uses.
func PlaceholderIconPath(name, category string) string {
	return iconPath(PlaceholderIcon(name, category))
}
