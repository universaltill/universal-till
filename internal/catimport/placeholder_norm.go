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

// normalizeWord folds case, accents, Arabic diacritics/tatweel and letter
// variants so "ÇİKOLATA"/"çikolata" and "قَهْوَة"/"قهوه" compare equal. ZWNJ
// (U+200C) and ZWJ (U+200D) are dropped: they are typing/shaping aids inside
// one word, never a boundary, so "هات‌داگ" == "هاتداگ" (ut-docs#6052). The
// result is only compared or emitted as hidden keywords, never displayed.
func normalizeWord(s string) string {
	s = normReplacer.Replace(s)
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) || r == 'ـ' || r == '\u200c' || r == '\u200d' {
			continue
		}
		b.WriteRune(r)
	}
	// NFD may have decomposed Arabic hamza-on-seat letters to base+Mn (the
	// Mn is gone now); run the replacer again for letters it recomposes.
	return normReplacer.Replace(norm.NFC.String(b.String()))
}

func notWordRune(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// joinerSpacer turns ZWNJ/ZWJ into spaces before the fold drops them.
var joinerSpacer = strings.NewReplacer("\u200c", " ", "\u200d", " ")

// normalizedWords normalises s then splits it into ordered words (maximal
// letter/digit runs). ZWNJ/ZWJ are turned into spaces first, so they separate
// words here even though normalizeWord drops them. This is the SPLIT form:
// "کیک‌هایی" still yields the stem "کیک".
func normalizedWords(s string) []string {
	return strings.FieldsFunc(normalizeWord(joinerSpacer.Replace(s)), notWordRune)
}

// joinedWords is the JOINED form: ZWNJ is dropped by the fold, so the parts
// of a word stay together ("هات‌داگ" -> "هاتداگ"). Callers match on either
// form (ut-docs#6052).
func joinedWords(s string) []string {
	return strings.FieldsFunc(normalizeWord(s), notWordRune)
}

// hasZWNJ reports whether s holds a word-internal joiner (ZWNJ or ZWJ), i.e.
// whether its split and joined forms can differ.
func hasZWNJ(s string) bool { return strings.ContainsAny(s, "\u200c\u200d") }

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
