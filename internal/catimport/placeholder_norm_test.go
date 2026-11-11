package catimport

import (
	"reflect"
	"testing"
)

func TestNormalizeWord(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ÇİKOLATA", "cikolata"},
		{"çikolata", "cikolata"},
		{"Café", "cafe"},
		{"قَهْوَة", "قهوه"},
		{"قهـــوة", "قهوه"},
		{"آب", "اب"},
		{"Heiße", "heisse"},
	}
	for _, c := range cases {
		if got := normalizeWord(c.in); got != c.want {
			t.Errorf("normalizeWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	same := [][2]string{{"KAHVESİ", "kahvesi"}, {"HEISSE", "heiße"}, {"چاي", "چای"}, {"كيك", "کیک"}}
	for _, p := range same {
		if normalizeWord(p[0]) != normalizeWord(p[1]) {
			t.Errorf("normalizeWord(%q) != normalizeWord(%q)", p[0], p[1])
		}
	}
}

func TestNormalizedWords_ZWNJSplits(t *testing.T) {
	got := normalizedWords("کیک‌ها")
	want := []string{normalizeWord("کیک"), "ها"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizedWords = %q, want %q", got, want)
	}
}

func TestStripArticle(t *testing.T) {
	if got, ok := stripArticle(normalizeWord("القهوة")); !ok || got != "قهوه" {
		t.Errorf("stripArticle(القهوة) = %q,%v", got, ok)
	}
	if got, ok := stripArticle("الف"); ok {
		t.Errorf("stripArticle(الف) = %q,true; want false (3 runes)", got)
	}
}
