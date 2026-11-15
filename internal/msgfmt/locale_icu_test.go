package msgfmt

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/web/locales"
)

// icuArg finds a complex argument in a raw value: a key whose English value
// has one is a MessageFormat key (ADR-0160 §8).
var icuArg = regexp.MustCompile(`\{\s*[\w-]+\s*,\s*(?:plural|select|number)\b`)

// localeICUProblems reports every translation of a MessageFormat key that the
// till's own parser refuses. TM falls back to English on a parse error
// without a word, so guard-i18n.sh's argument check alone would let a
// missing `other` branch or a misspelt category ship (ut-docs#4480).
func localeICUProblems(en map[string]string, locs map[string]map[string]string) []string {
	var out []string
	for name, loc := range locs {
		for k, enVal := range en {
			if !icuArg.MatchString(enVal) {
				continue
			}
			v, ok := loc[k]
			if !ok {
				continue // a missing key is guard-i18n.sh's check
			}
			if _, err := Parse(v); err != nil {
				out = append(out, fmt.Sprintf("%s: %s: %v — fix the translation; the till shows English while it does not parse", name, k, err))
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestLocaleICUProblems_RefusesWhatTheParserRefuses(t *testing.T) {
	en := map[string]string{"x.count": "{count, plural, one {# item} other {# items}}", "x.plain": "%d item(s)"}
	got := localeICUProblems(en, map[string]map[string]string{
		"ar.json": {"x.count": "{count, plural, zero {a} one {b} two {c} few {d} many {e}}", "x.plain": "%d صنف"},
		"fa.json": {"x.count": "{count, plural, one {# کالا} other {# کالا}}"},
	})
	if len(got) != 1 || !strings.HasPrefix(got[0], "ar.json: x.count: ") {
		t.Fatalf("want one ar.json x.count problem, got %q", got)
	}
}

// Every shipped translation of a MessageFormat key parses with the parser
// the till formats it with (ut-docs#4480).
func TestRealLocaleBundle_ICUValuesParseInEveryLocale(t *testing.T) {
	read := func(name string) map[string]string {
		data, err := fs.ReadFile(locales.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return m
	}
	names, err := fs.Glob(locales.FS, "*.json")
	if err != nil {
		t.Fatal(err)
	}
	locs := map[string]map[string]string{}
	for _, n := range names {
		if n != "en.json" {
			locs[n] = read(n)
		}
	}
	for _, p := range localeICUProblems(read("en.json"), locs) {
		t.Error(p)
	}
}
