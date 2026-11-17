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
		// ut-docs#6052: ZWNJ / ZWJ are dropped, never a word boundary.
		{"هات\u200cداگ", "هاتداگ"},
		{"a\u200db", "ab"},
		{"a\u200c\u200db", "ab"},
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

// ZWJ (U+200D) is a word-internal joiner like ZWNJ: the split form splits
// on it too, and hasZWNJ reports it, so "کیک‍ها" still stems to cake
// (ut-docs#6052 review L1).
func TestNormalizedWords_ZWJSplits(t *testing.T) {
	got := normalizedWords("کیک\u200dها")
	want := []string{normalizeWord("کیک"), "ها"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizedWords = %q, want %q", got, want)
	}
	if !hasZWNJ("کیک\u200dها") {
		t.Error("hasZWNJ(ZWJ word) = false, want true")
	}
}

func TestJoinedWords_ZWNJJoins(t *testing.T) {
	got := joinedWords("کیک\u200cها")
	want := []string{normalizeWord("کیکها")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("joinedWords = %q, want %q", got, want)
	}
	if !hasZWNJ("a\u200cb") || hasZWNJ("ab") {
		t.Error("hasZWNJ wrong")
	}
}

func TestFoldForSearch_ZWNJIdempotent(t *testing.T) {
	for _, in := range []string{"تخم\u200cمرغ", "a\u200c\u200db"} {
		once := FoldForSearch(in)
		if FoldForSearch(once) != once {
			t.Errorf("FoldForSearch not idempotent on %q", in)
		}
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
