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
// variants so "ÇİKOLATA"/"çikolata" and "قَهْوَة"/"قهوه" compare equal.
func normalizeWord(s string) string {
	s = normReplacer.Replace(s)
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) || r == 'ـ' {
			continue
		}
		b.WriteRune(r)
	}
	// NFD may have decomposed Arabic hamza-on-seat letters to base+Mn (the
	// Mn is gone now); run the replacer again for letters it recomposes.
	return normReplacer.Replace(norm.NFC.String(b.String()))
}

// normalizedWords normalises s then splits it into ordered words (maximal
// letter/digit runs; ZWNJ and all other characters are separators).
func normalizedWords(s string) []string {
	return strings.FieldsFunc(normalizeWord(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

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
