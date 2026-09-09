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

// IconPath maps a built-in icon key to its bundled asset's public path.
// ok is false for a key this package doesn't recognise — callers that take
// an icon key from outside the process (ut-docs#1844's picker takes one
// from a POST body) must check it rather than trust an empty path is a
// deliberate "no icon" answer.
func IconPath(icon string) (path string, ok bool) {
	switch icon {
	case "coffee", "pastry", "sandwich", "drink", "generic":
		return "/public/assets/category-icons/" + icon + ".svg", true
	default:
		return "", false
	}
}

// PlaceholderIconPath is PlaceholderIcon plus IconPath in one call — what
// callers actually want: a path ready to store as an item_images.path
// value (see data.CatalogRepo.EnsureDefaultThumbnail), through the same
// "/public/assets/..." convention the manual upload path already uses.
// PlaceholderIcon only ever returns a key IconPath recognises, so the ok
// return is dropped here.
func PlaceholderIconPath(name, category string) string {
	path, _ := IconPath(PlaceholderIcon(name, category))
	return path
}

// BuiltinIcon is one bundled category icon offered by the catalog image
// picker (ut-docs#1844): Key is the stored identifier (matches the asset
// filename and what IconPath/PlaceholderIcon return), Path its public
// asset path, and I18nKey the locale key for its display label — the
// picker template renders {{ T .I18nKey }}, never a hardcoded label
// (universal-till/CLAUDE.md's i18n rule).
type BuiltinIcon struct {
	Key, Path, I18nKey string
}

// BuiltinIcons returns every built-in category icon in a fixed display
// order, for a UI picker to enumerate. The set intentionally matches
// IconPath's known keys exactly (TestBuiltinIcons_MatchesIconPath pins
// this) — adding an icon here with no IconPath case, or vice versa, is a
// bug, not a style choice.
func BuiltinIcons() []BuiltinIcon {
	return []BuiltinIcon{
		{Key: "coffee", Path: mustIconPath("coffee"), I18nKey: "catalog.builtin_icon.coffee"},
		{Key: "drink", Path: mustIconPath("drink"), I18nKey: "catalog.builtin_icon.drink"},
		{Key: "sandwich", Path: mustIconPath("sandwich"), I18nKey: "catalog.builtin_icon.sandwich"},
		{Key: "pastry", Path: mustIconPath("pastry"), I18nKey: "catalog.builtin_icon.pastry"},
		{Key: "generic", Path: mustIconPath("generic"), I18nKey: "catalog.builtin_icon.generic"},
	}
}

// mustIconPath is BuiltinIcons()'s own literal list resolving its paths
// through IconPath instead of duplicating the "/public/assets/..." string
// a second time — the two can't drift out of sync by construction. Panics
// on an unknown key, which would mean BuiltinIcons() itself has a typo;
// TestBuiltinIcons_MatchesIconPath also catches this without needing to
// crash the binary.
func mustIconPath(icon string) string {
	path, ok := IconPath(icon)
	if !ok {
		panic("catimport: BuiltinIcons() key " + icon + " unknown to IconPath")
	}
	return path
}
