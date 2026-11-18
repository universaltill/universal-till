package catalogrules

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrNameInvalidCharacters: a name holds a line break, tab, control or
// invisible format character.
var ErrNameInvalidCharacters = errors.New("name contains a line break, control or invisible formatting character")

// CheckSingleLineName trims s and returns it, or ErrNameInvalidCharacters
// (ut-docs#5858, #5860). After trimming it refuses invalid UTF-8, every
// control rune (Cc), U+2028/U+2029 and every format rune (Cf) except ZWNJ,
// ZWJ, LRM, RLM, ALM and the isolates U+2066-2069, which RTL names need.
// Isolates must balance so a name cannot carry its direction into the text
// written after it. It also refuses the supplementary variation selectors
// U+E0100-E01EF: invisible (Mn, not Cf), and receipts and fonts do not
// render ideographic variation sequences. The BMP variation selectors
// U+FE00-FE0F (emoji presentation), the Mongolian free variation selectors
// and the combining grapheme joiner stay allowed. A blank result is
// ("", nil): blank stays each caller's own check.
func CheckSingleLineName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", ErrNameInvalidCharacters
	}
	depth := 0
	for _, r := range s {
		switch {
		case unicode.IsControl(r), r == '\u2028', r == '\u2029':
			return "", ErrNameInvalidCharacters
		case r == '\u2066', r == '\u2067', r == '\u2068':
			depth++
		case r == '\u2069':
			if depth == 0 {
				return "", ErrNameInvalidCharacters
			}
			depth--
		case r == '\u200C', r == '\u200D', r == '\u200E', r == '\u200F', r == '\u061C':
		case r >= 0xE0100 && r <= 0xE01EF:
			return "", ErrNameInvalidCharacters
		case unicode.Is(unicode.Cf, r):
			return "", ErrNameInvalidCharacters
		}
	}
	if depth != 0 {
		return "", ErrNameInvalidCharacters
	}
	return s, nil
}
