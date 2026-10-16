package config

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/msgfmt"
	"github.com/universaltill/universal-till/web/locales"
)

func tmFixture(t *testing.T) *I18n {
	t.Helper()
	fsys := fstest.MapFS{
		"en.json": &fstest.MapFile{Data: []byte(`{
			"cart.items": "{count, plural, one {# item} other {# items}}",
			"plain": "No plurals here",
			"en.only": "{count, plural, one {# thing} other {# things}}"
		}`)},
		"ar.json": &fstest.MapFile{Data: []byte(`{
			"cart.items": "{count, plural, zero {لا عناصر} one {عنصر واحد} two {عنصران} few {# عناصر} many {# عنصرًا} other {# عنصر}}"
		}`)},
		"ru.json": &fstest.MapFile{Data: []byte(`{
			"cart.items": "{count, plural, one {# товар} few {# товара} many {# товаров} other {# товара}"
		}`)},
		"de.json": &fstest.MapFile{Data: []byte(`{
			"cart.items": "{anzahl, plural, one {# Artikel} other {# Artikel}}"
		}`)},
	}
	i, err := NewI18nFS(fsys, "en")
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// ut-docs#4373: the translation's own language picks the plural category.
func TestTM_UsesTheTranslationsPluralRules(t *testing.T) {
	i := tmFixture(t)
	for _, tc := range []struct {
		loc  string
		n    int
		want string
	}{
		{"en", 1, "1 item"}, {"en", 2, "2 items"},
		{"ar", 0, "لا عناصر"}, {"ar", 2, "عنصران"}, {"ar", 5, "5 عناصر"}, {"ar", 11, "11 عنصرًا"}, {"ar", 100, "100 عنصر"},
		{"ar-EG", 3, "3 عناصر"},
	} {
		if got := i.TM(tc.loc, "cart.items", map[string]any{"count": tc.n}, msgfmt.Options{}); got != tc.want {
			t.Errorf("TM(%s, %d) = %q, want %q", tc.loc, tc.n, got, tc.want)
		}
	}
}

// ut-docs#4373 acceptance: a malformed ICU value falls back to English —
// both a syntax error (ru: unbalanced brace) and a translation that renames
// the argument (de: {anzahl} for {count}).
func TestTM_MalformedTranslationFallsBackToEnglish(t *testing.T) {
	i := tmFixture(t)
	args := map[string]any{"count": 3}
	for _, loc := range []string{"ru", "de"} {
		if got := i.TM(loc, "cart.items", args, msgfmt.Options{}); got != "3 items" {
			t.Errorf("TM(%s) = %q, want the English %q", loc, got, "3 items")
		}
	}
}

func TestTM_EdgeCases(t *testing.T) {
	i := tmFixture(t)
	if got := i.TM("ar", "en.only", map[string]any{"count": 2}, msgfmt.Options{}); got != "2 things" {
		t.Errorf("English text uses English categories on an ar till: got %q", got)
	}
	if got := i.TM("en", "plain", nil, msgfmt.Options{}); got != "No plurals here" {
		t.Errorf("plain value: got %q", got)
	}
	if got := i.TM("en", "no.such.key", nil, msgfmt.Options{}); got != "no.such.key" {
		t.Errorf("unknown key: got %q", got)
	}
	// English itself can't format (missing argument): its raw text renders.
	if got := i.TM("en", "cart.items", nil, msgfmt.Options{}); got != "{count, plural, one {# item} other {# items}}" {
		t.Errorf("English failing too: got %q", got)
	}
	shape := msgfmt.Options{Number: func(s string) string { return "[" + s + "]" }}
	if got := i.TM("en", "cart.items", map[string]any{"count": 7}, shape); got != "[7] items" {
		t.Errorf("Number option: got %q", got)
	}
}

// A shop override that breaks the ICU falls back to English rather than
// rendering the broken text.
func TestTM_BrokenShopOverrideFallsBackToEnglish(t *testing.T) {
	i := tmFixture(t)
	i.SetShopOverrides(map[string]map[string]string{"ar": {"cart.items": "{count, plural, one {x}}"}})
	if got := i.TM("ar", "cart.items", map[string]any{"count": 2}, msgfmt.Options{}); got != "2 items" {
		t.Errorf("got %q", got)
	}
}

var icuArg = regexp.MustCompile(`\{\s*[\w-]+\s*,\s*(plural|select)\s*,`)

// Every shipped value of a MessageFormat key parses, in every core locale.
// A broken translation would otherwise ship silently as English.
func TestRealLocaleBundle_MessageFormatValuesParse(t *testing.T) {
	files, err := fs.Glob(locales.FS, "*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("locale files: %v %v", files, err)
	}
	all := map[string]map[string]string{}
	for _, f := range files {
		data, err := fs.ReadFile(locales.FS, f)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		all[strings.TrimSuffix(f, ".json")] = m
	}
	for key, en := range all["en"] {
		if !icuArg.MatchString(en) {
			continue
		}
		for loc, m := range all {
			v, ok := m[key]
			if !ok {
				continue
			}
			if _, err := msgfmt.Parse(v); err != nil {
				t.Errorf("web/locales/%s.json %s: %v", loc, key, err)
			}
		}
	}
}
