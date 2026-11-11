package catimport

import "testing"

// labelFixture is a small map of locale -> icon key -> translated tile
// label, holding real values from the shipped locales / language packs.
var labelFixture = map[string]map[string]string{
	"de": {"coffee": "Kaffee", "beer": "Bier", "tea": "Tee", "cake": "Kuchen", "bread": "Brot",
		"drink": "Wasser", "ice-cream": "Eis", "hot-chocolate": "Heiße Schokolade", "chocolate": "Schokolade"},
	"es": {"coffee": "Café", "beer": "Cerveza", "tea": "Té", "ice-cream": "Helado", "bread": "Pan",
		"hot-chocolate": "Chocolate caliente", "coffee-to-go": "Café para llevar"},
	"tr": {"coffee": "Kahve", "tea": "Çay", "chocolate": "Çikolata", "beer": "Bira", "drink": "Su",
		"ice-cream": "Dondurma", "cake": "Pasta", "noodles": "Erişte / pilav"},
	"fa": {"coffee": "قهوه", "tea": "چای", "cake": "کیک", "drink": "آب"},
	"ar": {"coffee": "قهوة", "tea": "شاي", "cake": "كعكة", "drink": "ماء"},
}

func fixtureLookup(locale, key string) (string, bool) {
	const prefix = "catalog.builtin_icon."
	if len(key) <= len(prefix) {
		return "", false
	}
	v, ok := labelFixture[locale][key[len(prefix):]]
	return v, ok
}

func TestIconMatcher_ShopLanguages(t *testing.T) {
	cases := []struct {
		lang, name, category, want string
	}{
		{"de", "Kaffee", "", "coffee"}, {"de", "Bier", "", "beer"}, {"de", "Tee", "", "tea"},
		{"de", "Kuchen", "", "cake"}, {"de", "Brot", "", "bread"}, {"de", "Wasser", "", "drink"},
		{"de", "Eis", "", "ice-cream"}, {"de", "Heiße Schokolade", "", "hot-chocolate"},
		{"de", "HEISSE SCHOKOLADE", "", "hot-chocolate"}, {"de", "Schokolade", "", "chocolate"},
		{"es", "Café con leche", "", "coffee"}, {"es", "Cafe", "", "coffee"}, {"es", "Cerveza", "", "beer"},
		{"es", "Té verde", "", "tea"}, {"es", "Helado", "", "ice-cream"}, {"es", "Pan", "", "bread"},
		{"es", "Café para llevar", "", "coffee-to-go"},
		{"tr", "Türk Kahvesi", "", "coffee"}, {"tr", "TÜRK KAHVESİ", "", "coffee"}, {"tr", "Çay", "", "tea"},
		{"tr", "ÇİKOLATA", "", "chocolate"}, {"tr", "Bira", "", "beer"}, {"tr", "Su", "", "drink"},
		{"tr", "Dondurma", "", "ice-cream"}, {"tr", "Pasta", "", "cake"}, {"tr", "Pilav", "", "noodles"},
		{"fa", "قهوه", "", "coffee"}, {"fa", "چای", "", "tea"}, {"fa", "چاي", "", "tea"},
		{"fa", "کیک", "", "cake"}, {"fa", "كيك", "", "cake"}, {"fa", "آب", "", "drink"},
		{"ar", "قهوة", "", "coffee"}, {"ar", "القهوة", "", "coffee"}, {"ar", "قَهْوَة", "", "coffee"},
		{"ar", "شاي", "", "tea"}, {"ar", "كعكة", "", "cake"},
		// Category fallback.
		{"de", "Hausmischung 3", "Kaffee", "coffee"},
		// Compounds are not matched.
		{"de", "Milchkaffee", "", "generic"},
		// Stem prefix hits.
		{"tr", "Kahvesi", "", "coffee"}, {"de", "Biere", "", "beer"}, {"es", "Helados", "", "ice-cream"},
		// Stem prefix misses.
		{"de", "Eistee", "", "generic"}, {"tr", "Sucuk", "", "generic"},
		{"de", "Bierglas", "", "generic"},
		// English still works under a shop language.
		{"de", "Cappuccino", "", "coffee"}, {"tr", "Cheese Burger", "", "burger"},
	}
	for _, c := range cases {
		m := NewIconMatcher([]string{c.lang, "en"}, fixtureLookup)
		if got := m.Icon(c.name, c.category); got != c.want {
			t.Errorf("[%s] Icon(%q,%q) = %q, want %q", c.lang, c.name, c.category, got, c.want)
		}
	}
}

func TestIconMatcher_AguacateMiss(t *testing.T) {
	m := NewIconMatcher([]string{"es"}, func(l, k string) (string, bool) {
		if k == "catalog.builtin_icon.avocado" {
			return "Agua", true
		}
		return "", false
	})
	if got := m.Icon("Aguacate", ""); got == "avocado" {
		t.Errorf("Aguacate matched Agua label")
	}
}

func TestIconMatcher_ShopLanguageBeatsEnglish(t *testing.T) {
	m := NewIconMatcher([]string{"tr", "en"}, fixtureLookup)
	if got := m.Icon("Pasta", ""); got != "cake" {
		t.Errorf("tr shop Pasta = %q, want cake", got)
	}
}

func TestIconMatcher_EnglishOnlyUnchanged(t *testing.T) {
	for name, m := range map[string]*IconMatcher{
		"nil": nil, "en": NewIconMatcher([]string{"en"}, fixtureLookup), "empty": NewIconMatcher(nil, fixtureLookup),
	} {
		if got := m.Icon("Pasta", ""); got != "bowl" {
			t.Errorf("%s: Pasta = %q, want bowl", name, got)
		}
		if got := m.Icon("Kaffee", ""); got != "generic" {
			t.Errorf("%s: Kaffee = %q, want generic", name, got)
		}
		if got := m.Icon("Pan Fried Noodles", ""); got == "bread" {
			t.Errorf("%s: Pan Fried Noodles = bread", name)
		}
	}
}

func TestIconMatcher_MissingLabelSkipped(t *testing.T) {
	m := NewIconMatcher([]string{"de", "en"}, func(string, string) (string, bool) { return "", false })
	if got := m.Icon("Cappuccino", ""); got != "coffee" {
		t.Errorf("got %q, want coffee via English path", got)
	}
}

func TestIconMatcher_GenericNotMatchable(t *testing.T) {
	m := NewIconMatcher([]string{"de"}, func(l, k string) (string, bool) {
		if k == "catalog.builtin_icon.generic" {
			return "Allgemein", true
		}
		return "", false
	})
	if got := m.Icon("Allgemein", ""); got != "generic" {
		t.Errorf("got %q", got)
	}
}

func TestIconMatcher_LanguageOrder(t *testing.T) {
	lk := func(l, k string) (string, bool) {
		switch {
		case l == "aa" && k == "catalog.builtin_icon.beer":
			return "Foo", true
		case l == "bb" && k == "catalog.builtin_icon.tea":
			return "Foo", true
		}
		return "", false
	}
	if got := NewIconMatcher([]string{"aa", "bb"}, lk).Icon("Foo", ""); got != "beer" {
		t.Errorf("aa first: %q", got)
	}
	if got := NewIconMatcher([]string{"bb", "aa"}, lk).Icon("Foo", ""); got != "tea" {
		t.Errorf("bb first: %q", got)
	}
}

func TestIconMatcher_IconPath(t *testing.T) {
	m := NewIconMatcher([]string{"tr"}, fixtureLookup)
	if got := m.IconPath("Çay", ""); got != "/public/assets/category-icons/tea.svg" {
		t.Errorf("IconPath = %q", got)
	}
}
