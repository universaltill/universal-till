package msgfmt

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"testing"

	"github.com/universaltill/universal-till/web/locales"
)

// The plural metadata check lives in this package's tests, not the guard's
// Python, because a value must parse with the parser the till formats with;
// and it stays test-only so no production code exists just for a check
// (guard-deadcode-baseline.sh).

// pluralArgs returns the name of every plural argument in m, at any nesting
// depth (inside select/plural branches and exact =N branches too), sorted and
// without duplicates. A select-only or plain message has none (ut-docs#4479).
func pluralArgs(m *Message) []string {
	set := map[string]struct{}{}
	var walk func(*Message)
	walk = func(m *Message) {
		for i := range m.parts {
			p := &m.parts[i]
			if p.kind == partPlural {
				set[p.arg] = struct{}{}
			}
			for _, e := range p.exact {
				walk(e.msg)
			}
			for _, b := range p.branches {
				walk(b)
			}
		}
	}
	walk(m)
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
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
		m, err := Parse(v)
		if err != nil {
			out = append(out, fmt.Sprintf("web/locales/en.json %s: the value has a plural argument or a meta `plural` but does not parse as ICU (%v); fix the value", key, err))
			continue
		}
		args := pluralArgs(m)
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
