package plugins

import (
	"strings"
	"testing"
)

func TestAskedChoiceValueOK(t *testing.T) {
	cases := []struct {
		name string
		v    string
		want bool
	}{
		{"plain", "gpt-4o", true},
		{"one byte", "a", true},
		{"64 bytes", strings.Repeat("a", 64), true},
		{"empty", "", false},
		{"65 bytes", strings.Repeat("a", 65), false},
		{"comma", "a,b", false},
		{"leading space", " a", false},
		{"trailing space", "a ", false},
		{"newline", "a\n", false},
		{"nul", "a\x00", false},
		{"bad utf8", "a\xff", false},
		{"unicode ok", "модель-1", true},
	}
	for _, c := range cases {
		if got := AskedChoiceValueOK(c.v); got != c.want {
			t.Errorf("%s: AskedChoiceValueOK(%q) = %v, want %v", c.name, c.v, got, c.want)
		}
	}
}
