package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
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

// pluralArg finds a plural argument in a raw value (ut-docs#4479).
var pluralArg = regexp.MustCompile(`\{\s*[\w-]+\s*,\s*plural\s*,`)

// pluralMetaProblems ties each `plural` in en.meta.json to the real English
// value, both ways (ADR-0160 §2, ut-docs#4479): a `plural` must name a plural
// argument the value parses to contain, and every English value with a plural
// argument needs a meta entry whose `plural` names one of them. A `plural` on
// a key absent from en is guard-i18n-meta.sh's check 3, not reported here.
// Problems are sorted and name the key, the file and the fix.
func pluralMetaProblems(en map[string]string, meta map[string]json.RawMessage) []string {
	var out []string
	declared := map[string]string{}
	for key, raw := range meta {
		var e struct {
			Plural *string `json:"plural"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Plural != nil {
			declared[key] = *e.Plural
		}
	}
	for key, v := range en {
		arg, has := declared[key]
		if !has && !pluralArg.MatchString(v) {
			continue
		}
		m, err := msgfmt.Parse(v)
		if err != nil {
			out = append(out, fmt.Sprintf("web/locales/en.json %s: the value has a plural argument or a meta `plural` but does not parse as ICU (%v); fix the value", key, err))
			continue
		}
		args := m.PluralArgs()
		switch {
		case has && slices.Contains(args, arg):
		case has:
			out = append(out, fmt.Sprintf("web/i18n-meta/en.meta.json %s: plural %q is not a plural argument of the en.json value (plural args: %v); set `plural` to a real {arg, plural, ...} argument or remove it", key, arg, args))
		case len(args) == 0:
		default:
			if _, ok := meta[key]; !ok {
				out = append(out, fmt.Sprintf("web/i18n-meta/en.meta.json %s: the en.json value has a plural argument (%s) but the key has no meta entry; add one with \"plural\": %q (and a description and review; drop its key-meta-baseline.txt line)", key, args[0], args[0]))
			} else {
				out = append(out, fmt.Sprintf("web/i18n-meta/en.meta.json %s: the en.json value has a plural argument (%s) but the meta entry has no `plural`; add \"plural\": %q", key, args[0], args[0]))
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestPluralMetaProblems(t *testing.T) {
	const (
		pl  = "{n, plural, one {# item} other {# items}}"
		sel = "{kind, select, a {x} other {y}}"
	)
	meta := func(plural string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"k": json.RawMessage(`{"description":"d","plural":"` + plural + `"}`)}
	}
	cases := []struct {
		name string
		en   map[string]string
		meta map[string]json.RawMessage
		want []string
	}{
		{"plural set, value not ICU", map[string]string{"k": "Items"}, meta("n"),
			[]string{`web/i18n-meta/en.meta.json k: plural "n" is not a plural argument of the en.json value (plural args: []); set ` + "`plural`" + ` to a real {arg, plural, ...} argument or remove it`}},
		{"plural set, arg names a different variable", map[string]string{"k": pl}, meta("count"),
			[]string{`web/i18n-meta/en.meta.json k: plural "count" is not a plural argument of the en.json value (plural args: [n]); set ` + "`plural`" + ` to a real {arg, plural, ...} argument or remove it`}},
		{"plural set, arg is a select", map[string]string{"k": sel}, meta("kind"),
			[]string{`web/i18n-meta/en.meta.json k: plural "kind" is not a plural argument of the en.json value (plural args: []); set ` + "`plural`" + ` to a real {arg, plural, ...} argument or remove it`}},
		{"plural set, value fails to parse", map[string]string{"k": "{n, plural, one {x}}"}, meta("n"),
			[]string{`web/locales/en.json k: the value has a plural argument or a meta ` + "`plural`" + ` but does not parse as ICU (msgfmt: "n" has no other branch); fix the value`}},
		{"plural nested in a select branch", map[string]string{"k": "{kind, select, a {" + pl + "} other {y}}"}, meta("n"), nil},
		{"value has plural, no meta entry", map[string]string{"k": pl}, map[string]json.RawMessage{},
			[]string{"web/i18n-meta/en.meta.json k: the en.json value has a plural argument (n) but the key has no meta entry; add one with \"plural\": \"n\" (and a description and review; drop its key-meta-baseline.txt line)"}},
		{"value has plural, meta without plural", map[string]string{"k": pl},
			map[string]json.RawMessage{"k": json.RawMessage(`{"description":"d"}`)},
			[]string{"web/i18n-meta/en.meta.json k: the en.json value has a plural argument (n) but the meta entry has no `plural`; add \"plural\": \"n\""}},
		{"value has plural, meta names wrong arg", map[string]string{"k": pl}, meta("kind"),
			[]string{`web/i18n-meta/en.meta.json k: plural "kind" is not a plural argument of the en.json value (plural args: [n]); set ` + "`plural`" + ` to a real {arg, plural, ...} argument or remove it`}},
		{"value has broken plural, no meta entry", map[string]string{"k": "{n, plural, one {x}}"}, map[string]json.RawMessage{},
			[]string{`web/locales/en.json k: the value has a plural argument or a meta ` + "`plural`" + ` but does not parse as ICU (msgfmt: "n" has no other branch); fix the value`}},
		{"apostrophe-quoted plural is literal text", map[string]string{"k": "'{n, plural, one {x} other {y}}'"}, map[string]json.RawMessage{}, nil},
		{"plain key", map[string]string{"k": "Hello {name}"}, map[string]json.RawMessage{}, nil},
		{"meta plural on key absent from en", map[string]string{}, meta("n"), nil},
	}
	for _, c := range cases {
		got := pluralMetaProblems(c.en, c.meta)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

// Every `plural` in en.meta.json names a real plural argument of the English
// value, and every English plural carries one (ADR-0160 §2, ut-docs#4479).
func TestRealLocaleBundle_PluralMetadataMatchesEnglish(t *testing.T) {
	data, err := fs.ReadFile(locales.FS, "en.json")
	if err != nil {
		t.Fatal(err)
	}
	en := map[string]string{}
	if err := json.Unmarshal(data, &en); err != nil {
		t.Fatalf("en.json: %v", err)
	}
	raw, err := os.ReadFile("../../web/i18n-meta/en.meta.json")
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("en.meta.json: %v", err)
	}
	for _, p := range pluralMetaProblems(en, meta) {
		t.Error(p)
	}
}
