package catimport

import "testing"

// ut-docs#6043 / ADR-0160 §12: head-noun-last matching for compounds,
// switched on per language by the i18n key catalog.icon_match.head_last.

var headFixture = map[string]string{
	"coffee": "Kaffee", "beer": "Bier", "tea": "Tee", "cake": "Kuchen", "bread": "Brot",
	"drink": "Wasser", "ice-cream": "Eis", "noodles": "Reis", "juice": "Saft", "salad": "Salat",
	"soup": "Suppe", "meat": "Fleisch", "cheese": "Käse", "hot-dog": "Hotdog / Wurst",
	"wine": "Wein", "bag": "Tasche", "bowl": "Schüssel",
}

// headLookup serves headFixture under every locale in langs, with the
// setting key set to setting ("" = key absent).
func headLookup(setting string, langs ...string) LabelLookup {
	return func(locale, key string) (string, bool) {
		ok := false
		for _, l := range langs {
			if l == locale {
				ok = true
			}
		}
		if !ok {
			return "", false
		}
		if key == "catalog.icon_match.head_last" {
			return setting, setting != ""
		}
		const prefix = "catalog.builtin_icon."
		if len(key) > len(prefix) {
			v, found := headFixture[key[len(prefix):]]
			return v, found
		}
		return "", false
	}
}

var headMustHit = []struct{ name, want string }{
	{"Milchkaffee", "coffee"}, {"Apfelsaft", "juice"}, {"Orangensaft", "juice"},
	{"Weißbier", "beer"}, {"Rindfleisch", "meat"}, {"Kartoffelsalat", "salad"},
	{"Tomatensuppe", "soup"}, {"Käsekuchen", "cake"}, {"Apfelkuchen", "cake"},
	{"Vollkornbrot", "bread"}, {"Frischkäse", "cheese"}, {"Currywurst", "hot-dog"},
	{"Grüntee", "tea"}, {"Vanilleeis", "ice-cream"},
}

// Names that must keep their key-off result when the key is on.
var headTraps = []string{
	"Bierglas", "Bierkrug", "Bierdeckel", "Eistee", "Kaffeetasse", "Kaffeefilter",
	"Kaffeemaschine", "Teebeutel", "Rotwein", "Schwein", "Schweinebraten", "Preis", "Kreis", "Steak",
}

func TestHeadLast_OffByDefault(t *testing.T) {
	for _, setting := range []string{"", "0", "yes", "11", "true"} {
		m := NewIconMatcher([]string{"de", "en"}, headLookup(setting, "de"))
		for _, c := range headMustHit {
			if c.name == "Currywurst" || c.name == "Frischkäse" {
				continue // hit today by prefix/other rules; not a head-noun case
			}
			if got := m.Icon(c.name, ""); got != "generic" {
				t.Errorf("setting %q: Icon(%q) = %q, want generic (rule off)", setting, c.name, got)
			}
		}
	}
}

func TestHeadLast_MustHit(t *testing.T) {
	m := NewIconMatcher([]string{"de", "en"}, headLookup("1", "de"))
	for _, c := range headMustHit {
		if got := m.Icon(c.name, ""); got != c.want {
			t.Errorf("Icon(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHeadLast_Traps(t *testing.T) {
	off := NewIconMatcher([]string{"de", "en"}, headLookup("0", "de"))
	on := NewIconMatcher([]string{"de", "en"}, headLookup("1", "de"))
	for _, n := range headTraps {
		if a, b := off.Icon(n, ""), on.Icon(n, ""); a != b {
			t.Errorf("Icon(%q): key off %q, key on %q; want identical", n, a, b)
		}
	}
	for _, n := range []string{"Bierglas", "Eistee", "Rotwein", "Schwein", "Preis"} {
		if got := on.Icon(n, ""); got != "generic" {
			t.Errorf("Icon(%q) = %q, want generic", n, got)
		}
	}
}

func TestHeadLast_LocaleNeutral(t *testing.T) {
	m := NewIconMatcher([]string{"xx", "en"}, headLookup("1", "xx"))
	for _, c := range headMustHit {
		if got := m.Icon(c.name, ""); got != c.want {
			t.Errorf("[xx] Icon(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	for setting, on := range map[string]bool{"1": true, " 1 ": true, "0": false, "yes": false, "11": false} {
		got := NewIconMatcher([]string{"xx"}, headLookup(setting, "xx")).Icon("Milchkaffee", "") == "coffee"
		if got != on {
			t.Errorf("setting %q: head rule on = %v, want %v", setting, got, on)
		}
	}
}

func TestHeadLast_ExistingHitWins(t *testing.T) {
	// Labels "abcd" (icon a) and "cdefgh" (icon b). Token "abcdefgh" ... the
	// prefix rule (abcd + 4 runes is too long) misses, so use "abcdef":
	// prefix hit for abcd (2 extra runes) and no head hit; and "xxxxabcd"
	// head-hits abcd only. Check step 1 beats step 2 with a token that both
	// prefix-matches "abcd" and head-matches "cdef" ("abcdef" ends in "cdef").
	look := func(loc, key string) (string, bool) {
		switch key {
		case "catalog.icon_match.head_last":
			return "1", true
		case "catalog.builtin_icon.coffee":
			return "abcd", true
		case "catalog.builtin_icon.tea":
			return "cdef", true
		}
		return "", false
	}
	m := NewIconMatcher([]string{"xx"}, look)
	if got := m.Icon("abcdef", ""); got != "coffee" {
		t.Errorf("Icon(abcdef) = %q, want coffee (prefix hit beats head step)", got)
	}
	// Name hit beats a category hit.
	d := NewIconMatcher([]string{"de", "en"}, headLookup("1", "de"))
	if got := d.Icon("Milchkaffee", "Getränke"); got != "coffee" {
		t.Errorf("Icon(Milchkaffee, Getränke) = %q, want coffee", got)
	}
	// Category still matches when the name does not.
	if got := d.Icon("Hausmischung", "Kaffee"); got != "coffee" {
		t.Errorf("category fallback = %q, want coffee", got)
	}
}

func TestHeadLast_LongestLabelWins(t *testing.T) {
	m := NewIconMatcher([]string{"de", "en"}, headLookup("1", "de"))
	if got := m.Icon("Basmatireis", ""); got != "noodles" {
		t.Errorf("Icon(Basmatireis) = %q, want noodles (reis over eis)", got)
	}
}

func TestHeadLast_EnglishUnaffected(t *testing.T) {
	// The key under an English-first shop changes nothing.
	for _, n := range []string{"Milchkaffee", "Cappuccino", "Cheese Burger", "Bierglas"} {
		a := NewIconMatcher([]string{"en", "de"}, headLookup("0", "de")).Icon(n, "")
		b := NewIconMatcher([]string{"en", "de"}, headLookup("1", "en")).Icon(n, "")
		if a != b {
			t.Errorf("Icon(%q): %q vs %q", n, a, b)
		}
	}
	// A pass with the key but no label rows is still skipped.
	if got := NewIconMatcher([]string{"xx", "en"}, func(l, k string) (string, bool) {
		if k == "catalog.icon_match.head_last" {
			return "1", true
		}
		return "", false
	}).Icon("Cappuccino", ""); got != "coffee" {
		t.Errorf("rowless pass: %q, want coffee", got)
	}
}

// Review 6043 finding 1: a weak head-noun hit must not beat a strict hit from
// the English pass, a later language, or the category.
func TestHeadLast_StrictBeatsHead(t *testing.T) {
	de := NewIconMatcher([]string{"de", "en"}, headLookup("1", "de"))
	for _, c := range []struct{ name, cat, want string }{
		{"Apfeltasche", "Kuchen", "cake"},   // category strict beats name head (bag)
		{"Kinderpreis Cola", "", "can"},     // English keyword beats the head hit "reis" (noodles)
		{"Kinderpreis", "Kaffee", "coffee"}, // category strict beats name head "reis" (noodles)
	} {
		if got := de.Icon(c.name, c.cat); got != c.want {
			t.Errorf("Icon(%q,%q) = %q, want %q", c.name, c.cat, got, c.want)
		}
	}
	// Second language's exact hit beats the first language's head hit.
	look := func(loc, key string) (string, bool) {
		if loc == "yy" {
			if key == "catalog.builtin_icon.tea" {
				return "Milchkaffee", true // contrived: exact word for tea
			}
			return "", false
		}
		return headLookup("1", "de")(loc, key)
	}
	m := NewIconMatcher([]string{"de", "yy", "en"}, look)
	if got := m.Icon("Milchkaffee", ""); got != "tea" {
		t.Errorf("second-language exact hit: Icon(Milchkaffee) = %q, want tea", got)
	}
}
