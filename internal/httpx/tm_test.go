package httpx

import (
	"html/template"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/config"
)

func initTMTranslator(t *testing.T) {
	t.Helper()
	tr, err := config.NewI18nFS(fstest.MapFS{
		"en.json": &fstest.MapFile{Data: []byte(`{"cart.items":"{count, plural, one {# item} other {# items}}"}`)},
		"fa.json": &fstest.MapFile{Data: []byte(`{"cart.items":"{count, plural, one {# کالا} other {# کالا}}"}`)},
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	InitI18n(tr, "en")
	t.Cleanup(func() { InitI18n(nil, "en") })
}

// ut-docs#4373: {{ TM "key" "name" value … }} formats a MessageFormat key
// for the request locale, numbers grouped and digit-shaped like {{ qty }}.
func TestTemplateTM_FormatsPluralsInRequestLocale(t *testing.T) {
	initTMTranslator(t)
	for _, tc := range []struct {
		loc  string
		n    any
		want string
	}{
		{"en", 1, "1 item"},
		{"en", 1234, "1,234 items"},
		{"fa", 3, "۳ کالا"},
	} {
		tpl := template.Must(template.New("x").Funcs(FuncsFor(tc.loc)).Parse(`{{ TM "cart.items" "count" .N }}`))
		var b strings.Builder
		if err := tpl.Execute(&b, map[string]any{"N": tc.n}); err != nil {
			t.Fatal(err)
		}
		if b.String() != tc.want {
			t.Errorf("%s %v: got %q, want %q", tc.loc, tc.n, b.String(), tc.want)
		}
	}
}

func TestTM_GoSideAndNoTranslator(t *testing.T) {
	initTMTranslator(t)
	if got := TM("en", "cart.items", map[string]any{"count": 2}); got != "2 items" {
		t.Errorf("got %q", got)
	}
	InitI18n(nil, "en")
	if got := TM("en", "cart.items", map[string]any{"count": 2}); got != "cart.items" {
		t.Errorf("no translator: got %q, want the key", got)
	}
	tm := FuncsFor("en")["TM"].(func(string, ...any) string)
	if got := tm("cart.items", "count"); got != "cart.items" {
		t.Errorf("no translator, dangling pair: got %q", got)
	}
}

func TestArgPairs_SkipsMalformedPairs(t *testing.T) {
	got := argPairs([]any{"a", 1, 2, "x", "b"})
	if len(got) != 1 || got["a"] != 1 {
		t.Errorf("argPairs = %v, want only a=1", got)
	}
}
