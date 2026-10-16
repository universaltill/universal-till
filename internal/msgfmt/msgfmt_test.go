package msgfmt

import (
	"strings"
	"testing"
)

const items = "{count, plural, one {# item} other {# items}}"

func mustFormat(t *testing.T, locale, pattern string, args map[string]any) string {
	t.Helper()
	got, err := Format(locale, pattern, args, Options{})
	if err != nil {
		t.Fatalf("Format(%q, %q, %v): %v", locale, pattern, args, err)
	}
	return got
}

// English has two cardinal forms: one (exactly 1, no visible decimals) and
// other.
func TestPlural_English_TwoForms(t *testing.T) {
	for _, tc := range []struct {
		n    any
		want string
	}{
		{0, "0 items"}, {1, "1 item"}, {2, "2 items"}, {21, "21 items"},
		{int64(1), "1 item"}, {1.5, "1.5 items"}, {"1.0", "1.0 items"}, {"1", "1 item"},
	} {
		if got := mustFormat(t, "en", items, map[string]any{"count": tc.n}); got != tc.want {
			t.Errorf("en %v: got %q, want %q", tc.n, got, tc.want)
		}
	}
}

// Russian has three integer forms: one (1, 21, 101…), few (2–4, 22–24…)
// and many (0, 5–20, 25…, 11–14).
func TestPlural_Russian_ThreeForms(t *testing.T) {
	ru := "{count, plural, one {# товар} few {# товара} many {# товаров} other {# товара}}"
	for n, want := range map[int]string{
		1: "1 товар", 21: "21 товар", 101: "101 товар",
		2: "2 товара", 4: "4 товара", 22: "22 товара",
		0: "0 товаров", 5: "5 товаров", 11: "11 товаров", 12: "12 товаров", 14: "14 товаров", 25: "25 товаров",
	} {
		if got := mustFormat(t, "ru", ru, map[string]any{"count": n}); got != want {
			t.Errorf("ru %d: got %q, want %q", n, got, want)
		}
	}
}

// Arabic uses all six CLDR categories: zero, one, two, few (3–10, 103…),
// many (11–99, 111…) and other (100, 102…).
func TestPlural_Arabic_SixForms(t *testing.T) {
	ar := "{count, plural, zero {zero #} one {one #} two {two #} few {few #} many {many #} other {other #}}"
	for n, want := range map[int]string{
		0: "zero 0", 1: "one 1", 2: "two 2",
		3: "few 3", 10: "few 10", 103: "few 103",
		11: "many 11", 99: "many 99", 111: "many 111",
		100: "other 100", 102: "other 102",
	} {
		if got := mustFormat(t, "ar", ar, map[string]any{"count": n}); got != want {
			t.Errorf("ar %d: got %q, want %q", n, got, want)
		}
	}
}

// A region subtag still picks its language's rules.
func TestPlural_RegionTagUsesLanguageRules(t *testing.T) {
	if got := mustFormat(t, "ru-RU", "{n, plural, one {a} few {b} other {c}}", map[string]any{"n": 3}); got != "b" {
		t.Errorf("ru-RU 3: got %q, want b", got)
	}
	if got := mustFormat(t, "not a tag!", items, map[string]any{"count": 1}); got != "1 item" {
		t.Errorf("unparseable locale falls back to English rules: got %q", got)
	}
}

// A missing category branch uses other.
func TestPlural_MissingCategoryUsesOther(t *testing.T) {
	if got := mustFormat(t, "ru", items, map[string]any{"count": 3}); got != "3 items" {
		t.Errorf("got %q", got)
	}
}

func TestPlural_ExactAndOffset(t *testing.T) {
	p := "{n, plural, offset:1 =0 {nobody} =1 {{who}} one {{who} and # other} other {{who} and # others}}"
	for n, want := range map[int]string{
		0: "nobody", 1: "Ana", 2: "Ana and 1 other", 3: "Ana and 2 others",
	} {
		if got := mustFormat(t, "en", p, map[string]any{"n": n, "who": "Ana"}); got != want {
			t.Errorf("n=%d: got %q, want %q", n, got, want)
		}
	}
}

func TestSelectAndNesting(t *testing.T) {
	p := "{kind, select, table {{count, plural, one {# table} other {# tables}}} other {{count} seats}}"
	if got := mustFormat(t, "en", p, map[string]any{"kind": "table", "count": 1}); got != "1 table" {
		t.Errorf("got %q", got)
	}
	if got := mustFormat(t, "en", p, map[string]any{"kind": "bar", "count": 4}); got != "4 seats" {
		t.Errorf("got %q", got)
	}
}

func TestNumberArgs(t *testing.T) {
	if got := mustFormat(t, "en", "{n, number} / {n, number, integer}", map[string]any{"n": 2.6}); got != "2.6 / 3" {
		t.Errorf("got %q", got)
	}
}

// The Number option shapes every rendered number (#, {n}, {n, number}) —
// the till passes its locale digit shaping here.
func TestNumberOption(t *testing.T) {
	opts := Options{Number: func(s string) string { return "<" + s + ">" }}
	got, err := Format("en", "{n} {n, number} {n, plural, other {#}}", map[string]any{"n": 7}, opts)
	if err != nil || got != "<7> <7> <7>" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestQuoting(t *testing.T) {
	for _, tc := range []struct{ p, want string }{
		{"l'article", "l'article"},
		{"it''s", "it's"},
		{"'{literal}' {n}", "{literal} 5"},
		{"{n, plural, other {'#' is #}}", "# is 5"},
		{"# outside plural", "# outside plural"},
		{"'{it''s}'", "{it's}"},
	} {
		if got := mustFormat(t, "en", tc.p, map[string]any{"n": 5}); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestErrors(t *testing.T) {
	for _, p := range []string{
		"{count, plural, one {# item}}",                // no other
		"{count, plural, one {# item} other {# items}", // unterminated
		"{count, plural, uno {x} other {y}}",           // unknown category
		"{count, plural, one {a} one {b} other {c}}",   // duplicate
		"{count, selectordinal, other {x}}",            // unsupported type
		"{count, number, currency}",                    // unsupported style
		"{}",                                           // no name
		"stray }",                                      // unbalanced
		"'{unclosed",                                   // unterminated quote
		"{count plural}",                               // missing comma
	} {
		if _, err := Parse(p); err == nil {
			t.Errorf("Parse(%q): want an error", p)
		}
	}
	if _, err := Format("en", items, map[string]any{}, Options{}); err == nil || !strings.Contains(err.Error(), "missing argument") {
		t.Errorf("missing argument: got %v", err)
	}
	if _, err := Format("en", items, map[string]any{"count": "lots"}, Options{}); err == nil {
		t.Error("non-numeric plural argument: want an error")
	}
}

// Review finding: big counts must not lose their magnitude (10000001 is
// "other" in English, not "one"), and a float32 renders its own digits.
func TestPlural_LargeAndFloat32(t *testing.T) {
	for _, tc := range []struct {
		loc  string
		n    any
		want string
	}{
		{"en", 10000001, "10000001 items"},
		{"en", "100000000000000000000001", "100000000000000000000001 items"},
		{"en", float32(0.1), "0.1 items"},
	} {
		if got := mustFormat(t, tc.loc, items, map[string]any{"count": tc.n}); got != tc.want {
			t.Errorf("%s %v: got %q, want %q", tc.loc, tc.n, got, tc.want)
		}
	}
	// fr: 0 and 1 are "one"; a truncated 10000001 would also read as one.
	// (x/text's CLDR tables predate fr "many" for exact millions, which
	// therefore render the other branch.)
	fr := "{n, plural, one {un} other {autre}}"
	for n, want := range map[int]string{0: "un", 1: "un", 2: "autre", 10000001: "autre"} {
		if got := mustFormat(t, "fr", fr, map[string]any{"n": n}); got != want {
			t.Errorf("fr %d: got %q, want %q", n, got, want)
		}
	}
}
