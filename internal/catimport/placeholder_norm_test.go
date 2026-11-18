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
		// ut-docs#6057: the remaining default-ignorables are dropped too.
		{"a\u00adb", "ab"}, {"a\u200bb", "ab"}, {"a\u200eb", "ab"}, {"a\u200fb", "ab"},
		{"a\u061cb", "ab"}, {"a\u2060b", "ab"}, {"a\u2066b", "ab"}, {"a\u2067b", "ab"},
		{"a\u2068b", "ab"}, {"a\u2069b", "ab"}, {"a\ufeffb", "ab"},
		{"\u0642\u0647\u200e\u0648\u0647", "قهوه"},
		{"\ufeffKAHVE", "kahve"},
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
// on it too, and hasWordJoiner reports it, so "کیک‍ها" still stems to cake
// (ut-docs#6052 review L1).
func TestNormalizedWords_ZWJSplits(t *testing.T) {
	got := normalizedWords("کیک\u200dها")
	want := []string{normalizeWord("کیک"), "ها"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizedWords = %q, want %q", got, want)
	}
	if !hasWordJoiner("کیک\u200dها") {
		t.Error("hasWordJoiner(ZWJ word) = false, want true")
	}
}

func TestJoinedWords_ZWNJJoins(t *testing.T) {
	got := joinedWords("کیک\u200cها")
	want := []string{normalizeWord("کیکها")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("joinedWords = %q, want %q", got, want)
	}
	if !hasWordJoiner("a\u200cb") || hasWordJoiner("ab") {
		t.Error("hasWordJoiner wrong")
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

// ut-docs#6057: ZWSP is an invisible word boundary; it splits, the joined
// form drops it.
func TestNormalizedWords_ZWSPSplits(t *testing.T) {
	if got := normalizedWords("egg\u200bsalad"); !reflect.DeepEqual(got, []string{"egg", "salad"}) {
		t.Errorf("normalizedWords = %q", got)
	}
	if got := joinedWords("egg\u200bsalad"); !reflect.DeepEqual(got, []string{"eggsalad"}) {
		t.Errorf("joinedWords = %q", got)
	}
	if !hasWordJoiner("egg\u200bsalad") {
		t.Error("hasWordJoiner(ZWSP) = false")
	}
}

// ut-docs#6057: SHY, WJ and FEFF only join; they never split a word.
func TestNormalizedWords_JoinersDoNotSplit(t *testing.T) {
	for _, c := range []string{"\u00ad", "\u2060", "\ufeff"} {
		in := "Kaf" + c + "fee"
		if got := normalizedWords(in); !reflect.DeepEqual(got, []string{"kaffee"}) {
			t.Errorf("normalizedWords(%+q) = %q", in, got)
		}
		if hasWordJoiner(in) {
			t.Errorf("hasWordJoiner(%+q) = true", in)
		}
	}
}

// ut-docs#6057: bidi marks/isolates split in the split form, join in the joined form.
func TestNormalizedWords_BidiMarksSplit(t *testing.T) {
	for _, c := range []string{"\u200e", "\u200f", "\u061c", "\u2066", "\u2069"} {
		in := "Kaf" + c + "fee"
		if got := normalizedWords(in); !reflect.DeepEqual(got, []string{"kaf", "fee"}) {
			t.Errorf("normalizedWords(%+q) = %q", in, got)
		}
		if got := joinedWords(in); !reflect.DeepEqual(got, []string{"kaffee"}) {
			t.Errorf("joinedWords(%+q) = %q", in, got)
		}
		if !hasWordJoiner(in) {
			t.Errorf("hasWordJoiner(%+q) = false", in)
		}
	}
}
