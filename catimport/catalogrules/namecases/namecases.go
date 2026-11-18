// Package namecases is the shared case table for
// catalogrules.CheckSingleLineName (ut-docs#5860), so the till's and
// ut-cloud's tests check every entry point against one list. Stdlib-only,
// no imports. Blank input is not in the table: blank is each caller's own
// check and has a different error code on each side.
package namecases

// Case is one input and the result CheckSingleLineName must give.
type Case struct {
	Label string
	In    string
	Want  string // the trimmed name when OK; unused otherwise
	OK    bool
}

// Cases returns a fresh slice on every call, so no caller can mutate the
// table another test reads.
func Cases() []Case {
	return []Case{
		{"ascii", "Flat White", "Flat White", true},
		{"german umlauts", "Käse Brötchen Größe", "Käse Brötchen Größe", true},
		{"rtl with marks and balanced isolates", "  \u200Fقهوة \u2067ساخنة\u2069  ", "\u200Fقهوة \u2067ساخنة\u2069", true},
		{"persian zwnj", "می\u200Cخواهم", "می\u200Cخواهم", true},
		{"zwj family emoji", "\U0001F468\u200D\U0001F469\u200D\U0001F467 Pack", "\U0001F468\u200D\U0001F469\u200D\U0001F467 Pack", true},
		{"fe0f emoji", "Heart \u2764\uFE0F", "Heart \u2764\uFE0F", true},
		{"spaces trimmed", "   Tea   ", "Tea", true},
		{"lrm allowed", "a\u200Eb", "a\u200Eb", true},
		{"alm allowed", "a\u061Cb", "a\u061Cb", true},
		{"balanced lri", "a\u2066b\u2069", "a\u2066b\u2069", true},

		{"esc", "a\x1bb", "", false},
		{"del", "a\x7fb", "", false},
		{"nel u+0085", "a\u0085b", "", false},
		{"newline", "a\nb", "", false},
		{"tab", "a\tb", "", false},
		{"line separator", "a\u2028b", "", false},
		{"paragraph separator", "a\u2029b", "", false},
		{"lre", "a\u202Ab", "", false},
		{"rle", "a\u202Bb", "", false},
		{"pdf", "a\u202Cb", "", false},
		{"lro", "a\u202Db", "", false},
		{"rlo", "a\u202Eb", "", false},
		{"zero width space", "a\u200Bb", "", false},
		{"bom", "a\ufeffb", "", false},
		{"soft hyphen", "a\u00ADb", "", false},
		{"word joiner", "a\u2060b", "", false},
		{"unbalanced pdi", "a\u2069b", "", false},
		{"pdi before lri", "a\u2069b\u2066c", "", false},
		{"unclosed lri", "a\u2066b", "", false},
		{"vs17 u+e0100", "a\U000E0100b", "", false},
		{"vs256 u+e01ef", "a\U000E01EFb", "", false},
		{"tag char u+e0041", "a\U000E0041b", "", false},
		{"invalid utf-8", "a\xffb", "", false},
	}
}
