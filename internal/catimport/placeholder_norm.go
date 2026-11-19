package catimport

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Language-neutral text normalisation for matching item names against
// translated icon labels (ut-docs#2654). Applied to labels and input alike;
// it is generic Unicode/script folding, not a per-language word list.

var normReplacer = strings.NewReplacer(
	"İ", "i", "I", "i", "ı", "i", // Turkish dotted/dotless i collapse
	"ß", "ss",
	// Arabic-script unification.
	"ي", "ي", "ى", "ي", "ی", "ي",
	"ك", "ك", "ک", "ك",
	"ة", "ه", "ۀ", "ه",
	"أ", "ا", "إ", "ا", "آ", "ا", "ٱ", "ا",
)

// ignorables are the invisible default-ignorable characters normalizeWord
// drops (ut-docs#6052, #6057): soft hyphen, ZWSP, ZWNJ, ZWJ, LRM, RLM, ALM,
// word joiner, the bidi isolates U+2066-2069 and the BOM/ZWNBSP. An explicit
// list, not \p{Cf}: Go and Node Unicode tables differ, which would break
// fold parity. web/public/icon-picker-filter.js `fold` must list the same set.
const ignorables = "\u00ad\u200b\u200c\u200d\u200e\u200f\u061c\u2060\u2066\u2067\u2068\u2069\ufeff"

// normalizeWord folds case, accents, Arabic diacritics/tatweel and letter
// variants so "ÇİKOLATA"/"çikolata" and "قَهْوَة"/"قهوه" compare equal. The
// ignorables are dropped: they are typing/shaping/direction aids, usually
// inside one word, so "هات‌داگ" == "هاتداگ" (ut-docs#6052, #6057). Some of them (ZWSP,
// ZWNJ, ZWJ and the bidi marks/isolates) may equally sit between two words, so
// normalizedWords also offers a split form. The result is only
// compared or emitted as hidden keywords, never displayed.
func normalizeWord(s string) string {
	s = normReplacer.Replace(s)
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) || r == 'ـ' || strings.ContainsRune(ignorables, r) {
			continue
		}
		b.WriteRune(r)
	}
	// NFD may have decomposed Arabic hamza-on-seat letters to base+Mn (the
	// Mn is gone now); run the replacer again for letters it recomposes.
	return normReplacer.Replace(norm.NFC.String(b.String()))
}

func notWordRune(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// invisibleSeparators are the ignorables that can sit between two words as
// well as inside one: ZWSP, ZWNJ, ZWJ and the bidi marks/isolates (LRM, RLM,
// ALM, U+2066-2069). Their split and joined forms can differ. SHY, WJ and
// FEFF are pure joiners ("same word") and are not listed.
const invisibleSeparators = "\u200b\u200c\u200d\u200e\u200f\u061c\u2066\u2067\u2068\u2069"

// joinerSpacer turns the invisible separators into spaces before the fold
// drops them, so they split words in the SPLIT form (as they did before
// ut-docs#6057, when they were plain non-letters).
var joinerSpacer = strings.NewReplacer(
	"\u200b", " ", "\u200c", " ", "\u200d", " ", "\u200e", " ", "\u200f", " ",
	"\u061c", " ", "\u2066", " ", "\u2067", " ", "\u2068", " ", "\u2069", " ")

// normalizedWords normalises s then splits it into ordered words (maximal
// letter/digit runs). The invisible separators are turned into spaces first, so they separate
// words here even though normalizeWord drops them. This is the SPLIT form:
// "کیک‌هایی" still yields the stem "کیک".
func normalizedWords(s string) []string {
	return strings.FieldsFunc(normalizeWord(joinerSpacer.Replace(s)), notWordRune)
}

// joinedWords is the JOINED form: the ignorables are dropped by the fold, so the parts
// of a word stay together ("هات‌داگ" -> "هاتداگ"). Callers match on either
// form (ut-docs#6052).
func joinedWords(s string) []string {
	return strings.FieldsFunc(normalizeWord(s), notWordRune)
}

// hasWordJoiner reports whether s holds an invisible separator/joiner whose
// split and joined forms can differ (see invisibleSeparators).
func hasWordJoiner(s string) bool { return strings.ContainsAny(s, invisibleSeparators) }

// stripArticle drops a leading Arabic definite article "ال" from a
// normalised word of at least 4 runes.
func stripArticle(w string) (string, bool) {
	r := []rune(w)
	if len(r) >= 4 && r[0] == 'ا' && r[1] == 'ل' {
		return string(r[2:]), true
	}
	return w, false
}

// FoldForSearch is the picker search fold (ut-docs#6042): normalizeWord on
// the whole string — separators are kept. web/public/icon-picker-filter.js
// `fold` must return the same result for every input;
// testdata/search_fold_parity.json pins both.
func FoldForSearch(s string) string { return normalizeWord(s) }
