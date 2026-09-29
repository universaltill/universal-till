package httpx

import (
	"strings"
	"unicode"
)

// TileInitials returns up to two initials for a label: the first letter or digit
// of each of its first two words (words split on white space and hyphens),
// upper-cased. The phone sale screen shows them on an item tile that has no
// picture, like an app icon (ut-docs#3059). Rune-based, so accented, Turkish
// and Persian letters come through whole.
func TileInitials(label string) string {
	words := strings.FieldsFunc(label, func(r rune) bool {
		return unicode.IsSpace(r) || r == '-' || r == '‐' || r == '–'
	})
	var out []rune
	for _, w := range words {
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}
