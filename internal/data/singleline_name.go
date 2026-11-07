package data

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
// (ut-docs#5858). After trimming it refuses invalid UTF-8, every control
// rune (Cc), U+2028/U+2029 and every format rune (Cf) except ZWNJ, ZWJ,
// LRM, RLM, ALM and the isolates U+2066-2069, which RTL names need. Isolates
// must balance so a name cannot carry its direction into the text written
// after it. A blank result is ("", nil): blank stays each caller's own check.
func CheckSingleLineName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", ErrNameInvalidCharacters
	}
	depth := 0
	for _, r := range s {
		switch {
		case unicode.IsControl(r), r == ' ', r == ' ':
			return "", ErrNameInvalidCharacters
		case r == '⁦', r == '⁧', r == '⁨':
			depth++
		case r == '⁩':
			if depth == 0 {
				return "", ErrNameInvalidCharacters
			}
			depth--
		case r == '‌', r == '‍', r == '‎', r == '‏', r == '؜':
		case unicode.Is(unicode.Cf, r):
			return "", ErrNameInvalidCharacters
		}
	}
	if depth != 0 {
		return "", ErrNameInvalidCharacters
	}
	return s, nil
}
