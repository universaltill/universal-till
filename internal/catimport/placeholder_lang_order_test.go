package catimport

import "testing"

// ut-docs#2654 review (Tester D1): English is checked at the position of
// "en" in the shop-language order — an English-default shop with a Turkish
// staff language keeps English first ("Pasta" = bowl), a Turkish-default
// shop with English staff checks Turkish first ("Pasta" = cake), and a list
// without "en" still falls back to English last.
func TestIconMatcher_EnglishAtItsPosition(t *testing.T) {
	cases := []struct {
		name    string
		locales []string
		in      string
		want    string
	}{
		{"en default, tr staff", []string{"en", "tr"}, "Pasta", "bowl"},
		{"en-GB default, tr staff", []string{"en-GB", "tr"}, "Pasta", "bowl"},
		{"en default, tr-only word", []string{"en", "tr"}, "Çay", "tea"},
		{"tr default, en staff", []string{"tr", "en"}, "Pasta", "cake"},
		{"tr only: English last", []string{"tr"}, "Pasta", "cake"},
		{"tr only: English still reached", []string{"tr"}, "Cappuccino", "coffee"},
		{"en default: English miss falls to tr", []string{"en", "tr"}, "Türk Kahvesi", "coffee"},
		{"en listed twice", []string{"en", "tr", "en"}, "Pasta", "bowl"},
		{"en default, nothing matches", []string{"en", "tr"}, "Xyzzy", "generic"},
	}
	for _, c := range cases {
		if got := NewIconMatcher(c.locales, fixtureLookup).Icon(c.in, ""); got != c.want {
			t.Errorf("%s: Icon(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
