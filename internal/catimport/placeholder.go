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
	// ut-docs#2506 grew the library from 5 tiles to ~70; the rows below
	// keep "specific before broad" ordering: a multi-word item name
	// resolves to its first-listed keyword. Coffee words stay first so
	// "Chai Latte" still resolves to coffee (latte) while plain "Chai"
	// or "Green Tea" gets the teapot.
	// Two-word keywords match as adjacent whole words and sit first,
	// ahead of the single words they contain ("Hot Chocolate" is a drink,
	// not a chocolate bar; "Ginger Beer" is a soft drink).
	{"hot chocolate", "hot-chocolate"},
	{"hot dog", "hot-dog"},
	{"hot drinks", "coffee"},
	{"hot drink", "coffee"},
	{"ginger ale", "can"},
	{"ginger beer", "can"},
	{"root beer", "can"},
	{"cinnamon roll", "pastry"},
	{"ice cream", "ice-cream"},
	{"hotdog", "hot-dog"},
	{"prawn", "seafood"},
	{"coffee", "coffee"},
	{"cappuccino", "coffee"},
	{"latte", "coffee"},
	{"americano", "coffee"},
	{"mocha", "coffee"},
	{"espresso", "espresso"},
	{"macchiato", "espresso"},
	{"cortado", "espresso"},
	{"boba", "bubble-tea"},
	{"chai", "tea"},
	{"tea", "tea"},
	{"cocoa", "hot-chocolate"},
	{"smoothie", "smoothie"},
	{"milkshake", "smoothie"},
	{"shake", "smoothie"},
	{"juice", "juice"},
	{"water", "water"},
	{"milk", "milk"},
	{"beer", "beer"},
	{"lager", "beer"},
	{"ale", "beer"},
	{"pint", "beer"},
	{"prosecco", "champagne"},
	{"champagne", "champagne"},
	{"cava", "champagne"},
	{"sekt", "champagne"},
	{"wine", "wine"},
	{"cocktail", "cocktail"},
	{"mojito", "cocktail"},
	{"martini", "cocktail"},
	{"margarita", "cocktail"},
	{"spritz", "cocktail"},
	{"whisky", "spirits"},
	{"whiskey", "spirits"},
	{"gin", "spirits"},
	{"vodka", "spirits"},
	{"rum", "spirits"},
	{"shot", "spirits"},
	{"cola", "can"},
	{"lemonade", "can"},
	{"soda", "drink"},
	{"drink", "drink"},
	{"drinks", "drink"},
	{"beverages", "drink"},
	{"beverage", "drink"},
	{"croissant", "croissant"},
	{"cupcake", "cupcake"},
	{"muffin", "cupcake"},
	{"cheesecake", "cake-slice"},
	{"cake", "cake"},
	{"cookie", "cookie"},
	{"biscuit", "cookie"},
	{"donut", "donut"},
	{"doughnut", "donut"},
	{"baguette", "baguette"},
	{"pretzel", "baguette"},
	{"bagel", "bread"},
	{"bread", "bread"},
	{"loaf", "bread"},
	{"toast", "bread"},
	{"pastry", "pastry"},
	{"danish", "pastry"},
	{"bakery", "pastry"},
	{"sandwich", "sandwich"},
	{"panini", "sandwich"},
	{"toastie", "sandwich"},
	{"wrap", "sandwich"},
	{"burger", "burger"},
	{"cheeseburger", "burger"},
	{"pizza", "pizza"},
	{"sausage", "hot-dog"},
	{"bratwurst", "hot-dog"},
	{"currywurst", "hot-dog"},
	{"salad", "salad"},
	{"soup", "soup"},
	{"noodles", "noodles"},
	{"ramen", "noodles"},
	{"sushi", "noodles"},
	{"rice", "noodles"},
	{"pasta", "bowl"},
	{"porridge", "bowl"},
	{"breakfast", "breakfast"},
	{"egg", "breakfast"},
	{"eggs", "breakfast"},
	{"omelette", "breakfast"},
	{"cheese", "cheese"},
	{"chicken", "chicken"},
	{"wings", "chicken"},
	{"steak", "meat"},
	{"beef", "meat"},
	{"meat", "meat"},
	{"ham", "ham"},
	{"bacon", "ham"},
	{"salmon", "fish"},
	{"fish", "fish"},
	{"prawns", "seafood"},
	{"shrimp", "seafood"},
	{"seafood", "seafood"},
	{"popcorn", "popcorn"},
	{"apple", "apple"},
	{"apples", "apple"},
	{"banana", "banana"},
	{"bananas", "banana"},
	{"orange", "citrus"},
	{"oranges", "citrus"},
	{"lemon", "citrus"},
	{"strawberries", "berries"},
	{"berries", "berries"},
	{"cherries", "berries"},
	{"grapes", "grapes"},
	{"melon", "melon"},
	{"watermelon", "melon"},
	{"avocado", "avocado"},
	{"carrot", "carrot"},
	{"carrots", "carrot"},
	{"vegetables", "carrot"},
	{"lettuce", "greens"},
	{"spinach", "greens"},
	{"pepper", "pepper"},
	{"peppers", "pepper"},
	{"mushroom", "mushroom"},
	{"mushrooms", "mushroom"},
	{"nuts", "nuts"},
	{"gelato", "ice-cream"},
	{"sundae", "sundae"},
	{"lolly", "ice-lolly"},
	{"popsicle", "ice-lolly"},
	{"ice", "ice-cream"},
	{"lollipop", "lollipop"},
	{"chocolate", "chocolate"},
	{"candy", "candy"},
	{"sweets", "candy"},
	{"gift", "gift"},
	{"voucher", "gift"},
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
	// joined is the name's words in order, space-separated and padded, so
	// a two-word keyword ("hot dog") matches only as whole adjacent words
	// (ut-docs#2506 review): never "hot" and "dog" apart, never a substring.
	joined := " " + strings.Join(orderedWords(s), " ") + " "
	for _, ki := range keywordIcons {
		if strings.Contains(ki.keyword, " ") {
			if strings.Contains(joined, " "+ki.keyword+" ") {
				return ki.icon, true
			}
			continue
		}
		// A plural is the same thing ("Sandwiches", "Cakes"): accept the
		// keyword with an "s"/"es" suffix, still whole-word only.
		if words[ki.keyword] || words[ki.keyword+"s"] || words[ki.keyword+"es"] {
			return ki.icon, true
		}
	}
	return "", false
}

// orderedWords is tokenize's split, kept in order (duplicates and all).
func orderedWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
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
	if _, known := iconByKey[icon]; !known {
		return "", false
	}
	return categoryIconPublicDir + icon + ".svg", true
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
// (universal-till/CLAUDE.md's i18n rule). Group is its picker section
// and Keywords its space-separated English search terms for the picker's
// filter box (ut-docs#2506). ID is its category icon id ("lucide:beer",
// manage-shop catalog contract §0.12) — what a category pick stores since
// ut-docs#2717 — or "" for the hand-drawn generic tile, which has none.
type BuiltinIcon struct {
	Key, ID, Path, I18nKey, Group, Keywords string
}

// BuiltinIcons returns every built-in category icon in registry order
// (icons.go), for a UI picker to enumerate. The set matches IconPath's
// known keys exactly (TestBuiltinIcons_MatchesIconPath pins this).
func BuiltinIcons() []BuiltinIcon {
	out := make([]BuiltinIcon, 0, len(iconDefs))
	for _, d := range iconDefs {
		out = append(out, BuiltinIcon{
			Key:      d.Key,
			ID:       d.Src,
			Path:     mustIconPath(d.Key),
			I18nKey:  "catalog.builtin_icon." + d.Key,
			Group:    d.Group,
			Keywords: strings.Join(d.Keywords, " "),
		})
	}
	return out
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
