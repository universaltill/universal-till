package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// scanSrc scans one fixture file as internal/x/a.go and returns its
// findings' keys ("func|literal") plus any marker problems.
func scanSrc(t *testing.T, src string) ([]string, []string) {
	t.Helper()
	fs, probs, err := scanPackage(map[string][]byte{"internal/x/a.go": []byte(src)})
	if err != nil {
		t.Fatalf("scanPackage: %v", err)
	}
	var keys []string
	for _, f := range fs {
		keys = append(keys, f.Func+"|"+f.Literal)
	}
	sort.Strings(keys)
	return dedupe(keys), probs
}

func dedupe(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func TestDetector(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string // "func|literal"; nil = no finding
	}{
		// --- country codes: comparisons ---
		{"eq literal", `package x
func h(cc string) bool { return cc == "DE" }`, []string{"h|DE"}},
		{"literal on left, neq", `package x
func h(cc string) bool { return "TR" != cc }`, []string{"h|TR"}},
		{"raw string", "package x\nfunc h(cc string) bool { return cc == `DE` }", []string{"h|DE"}},
		{"lower case with country-ish operand", `package x
func h(country string) bool { return country == "tr" }`, []string{"h|tr"}},
		{"lower case needs country-ish context", `package x
func h(loc string) bool { return loc == "tr" }`, nil},
		{"lower case via call on country var", `package x
import "strings"
func h(country string) bool { return strings.ToLower(country) == "de" }`, []string{"h|de"}},
		{"lower case cc token", `package x
func h(cc string) bool { return cc == "de" }`, []string{"h|de"}},
		{"cc inside another word is not context", `package x
func h(accent string) bool { return accent == "de" }`, nil},
		{"isOpen is not iso", `package x
func h(isOpen string) bool { return isOpen == "de" }`, nil},
		{"lower case locale tag stays unconditional", `package x
func h(loc string) bool { return loc == "de-de" }`, []string{"h|de-de"}},
		{"lower case switch on lang not flagged", `package x
func h(lang string) { switch lang { case "de", "tr": case "fr": } }`, nil},
		{"lower case switch on region flagged", `package x
func h(region string) { switch region { case "de": } }`, []string{"h|de"}},
		{"lower case map index on json object not flagged", `package x
func h(obj map[string]string) { obj["at"] = "x" }`, nil},
		{"lower case set of languages not flagged", `package x
var langs = map[string]bool{"de": true, "tr": true}
func h(l string) bool { return langs[l] }`, nil},
		{"lower case set indexed by country flagged", `package x
var tbl = map[string]bool{"de": true}
func h(country string) bool { return tbl[country] }`, []string{"<file>|de"}},
		{"lower case EqualFold with market arg", `package x
import "strings"
func h(market string) bool { return strings.EqualFold(market, "tr") }`, []string{"h|tr"}},
		// --- constants / single-assignment vars ---
		{"package const compared", `package x
const de = "DE"
func h(cc string) bool { return cc == de }`, []string{"<file>|DE"}},
		{"package const in case", `package x
const de = "DE"
func h(cc string) { switch cc { case de: } }`, []string{"<file>|DE"}},
		{"package const in EqualFold", `package x
import "strings"
const tseProvisionCountry = "DE"
func h(country string) bool { return strings.EqualFold(country, tseProvisionCountry) }
func g(country string) bool { return country == tseProvisionCountry }`, []string{"<file>|DE"}},
		{"grouped const", `package x
const (
	a = 1
	tr = "TR"
)
func h(cc string) bool { return cc != tr }`, []string{"<file>|TR"}},
		{"func-level const keyed on its func", `package x
func h(cc string) bool { const at = "AT"; return cc == at }`, []string{"h|AT"}},
		{"single-assignment var", `package x
var home = "GB"
func h(cc string) bool { return cc == home }`, []string{"<file>|GB"}},
		{"reassigned var not resolved", `package x
var home = "GB"
func set(v string) { home = v }
func h(cc string) bool { return cc == home }`, nil},
		{"lower case const named country is context", `package x
const defaultCountry = "de"
func h(v string) bool { return v == defaultCountry }`, []string{"<file>|de"}},
		{"lower case const without context", `package x
const lang = "de"
func h(v string) bool { return v == lang }`, nil},
		{"const not in testing position", `package x
const de = "DE"
func h() string { return de }`, nil},
		{"non-country const", `package x
const us = "US"
func h(cc string) bool { return cc == us }`, nil},
		// --- named map types ---
		{"named set type", `package x
type set map[string]bool
var s = set{"DE": true}`, []string{"<file>|DE"}},
		{"named struct{} set type", `package x
type set map[string]struct{}
var countries = set{"tr": {}}`, []string{"<file>|tr"}},
		{"second operand of ||", `package x
func h(cc string) bool { return cc == "US" || cc == "DE" }`, []string{"h|DE"}},
		{"slice expr operand", `package x
func h(cc string) bool { return cc[:2] == "DE" }`, []string{"h|DE"}},
		{"no space before brace", `package x
func h(countryCode string) { if countryCode == "CH"{} }`, []string{"h|CH"}},
		{"underscore locale", `package x
func h(loc string) bool { return loc == "de_DE" }`, []string{"h|de_DE"}},
		{"hyphen locale", `package x
func h(loc string) bool { return loc == "tr-TR" }`, []string{"h|tr-TR"}},
		{"country not in curated list", `package x
func h(cc string) bool { return cc == "US" }`, nil},
		{"parenthesised", `package x
func h(cc string) bool { return cc == ("GB") }`, []string{"h|GB"}},
		// --- switch ---
		{"every value in a case list", `package x
func h(cc string) { switch cc { case "US", "DE", "XX": } }`, []string{"h|DE"}},
		{"two codes in one case", `package x
func h(cc string) { switch cc { case "DE", "TR": } }`, []string{"h|DE", "h|TR"}},
		{"switch on literal", `package x
func h(a bool) { switch "DE" { case "x": } }`, []string{"h|DE"}},
		// --- calls ---
		{"EqualFold", `package x
import "strings"
func h(cc string) bool { return strings.EqualFold(cc, "TR") }`, []string{"h|TR"}},
		{"HasPrefix", `package x
import "strings"
func h(cc string) bool { return strings.HasPrefix(cc, "DE") }`, []string{"h|DE"}},
		{"HasSuffix", `package x
import "strings"
func h(cc string) bool { return strings.HasSuffix(cc, "-AT") }`, nil},
		{"Contains", `package x
import "strings"
func h(cc string) bool { return strings.Contains(cc, "de-CH") }`, []string{"h|de-CH"}},
		{"slices.Contains composite", `package x
import "slices"
func h(cc string) bool { return slices.Contains([]string{"DE", "AT"}, cc) }`, []string{"h|AT", "h|DE"}},
		{"slices.Index", `package x
import "slices"
func h(l []string) int { return slices.Index(l, "PT") }`, []string{"h|PT"}},
		{"unrelated call arg", `package x
func h() { use("DE") }
func use(string) {}`, nil},
		// --- map keys ---
		{"set map bool", `package x
var s = map[string]bool{"DE": true, "XX": true}`, []string{"<file>|DE"}},
		{"set map struct{}", `package x
var countries = map[string]struct{}{"tr": {}}`, []string{"<file>|tr"}},
		{"indexed table", `package x
var tbl = map[string]string{"DE": "de"}
func h(cc string) string { return tbl[cc] }`, []string{"<file>|DE"}},
		{"indexed table of slices", `package x
type spec struct{ L string }
var tbl = map[string][]spec{"ES": {{L: "es"}}}
func h(cc string) []spec { return tbl[cc] }`, []string{"<file>|ES"}},
		{"local indexed table", `package x
func h(cc string) int { m := map[string]int{"FR": 1}; return m[cc] }`, []string{"h|FR"}},
		{"index by literal", `package x
var m map[string]int
func h() int { return m["NL"] }`, []string{"h|NL"}},
		{"name table not flagged even when indexed", `package x
var names = map[string]string{"DE": "Germany", "TR": "Türkiye"}
func h(cc string) string { return names[cc] }`, nil},
		{"unindexed map not flagged", `package x
var m = map[string]int{"DE": 1}
func h() { for range m {} }`, nil},
		// --- values are not flagged ---
		{"struct field value", `package x
type C struct{ Code string }
var cs = []C{{Code: "GB"}}`, nil},
		{"map value", `package x
var tz = map[string]string{"Europe/Berlin": "DE"}
func h(z string) string { return tz[z] }`, nil},
		{"assignment", `package x
func h() string { cc := "DE"; return cc }`, nil},
		// --- comments & string literals (the awk blocker) ---
		{"slash-star inside string then real code", `package x
var r = "/static/*"
func z(cc string) { if cc == "DE" {} }`, []string{"z|DE"}},
		{"URL double slash inside string", `package x
func z(cc string) bool { u := "https://x"; return cc == "DE" && u != "" }`, []string{"z|DE"}},
		{"code in comment ignored", `package x
// if cc == "DE" { stripe }
/* case "TR": sumup */
func z() {}`, nil},
		// --- vendors ---
		{"vendor literal capitalised", `package x
var fisk = "Fiskaly"`, []string{"<file>|Fiskaly"}},
		{"vendor in host", `package x
var u = "api.sumup.com"`, []string{"<file>|api.sumup.com"}},
		{"vendor in struct tag", "package x\ntype T struct { K string `json:\"stripe_id\"` }", []string{"<file>|json:\"stripe_id\""}},
		{"vendor identifier not scanned", `package x
func square(n int) int { return n * n }
var qrPayload = 1`, nil},
		{"vendor substring not token", `package x
var s = "squared stripes"`, nil},
		{"square token in string", `package x
func f() string { return "square" }`, []string{"f|square"}},
		{"speedy alone not flagged", `package x
var s = "speedy delivery"`, nil},
		{"speedy-kasse flagged", `package x
func f(s string) bool { return s == "speedy-kasse" }`, []string{"f|speedy-kasse"}},
		{"speedykasse flagged", `package x
var s = "SpeedyKasse"`, []string{"<file>|SpeedyKasse"}},
		// --- plugin ids ---
		{"plugin id const", `package x
const P = "com.universaltill.tax-tr"`, []string{"<file>|com.universaltill.tax-tr"}},
		{"ut-plugin repo", `package x
var u = "https://github.com/universaltill/ut-plugin-tax-de"`, []string{"<file>|https://github.com/universaltill/ut-plugin-tax-de"}},
		{"bare namespace prefix not flagged", `package x
func f(id string) bool { return len(id) > 0 && id[:18] == "com.universaltill." }`, nil},
		// --- enclosing func naming ---
		{"method receiver", `package x
type S struct{}
func (s *S) M(cc string) bool { return cc == "IT" }`, []string{"S.M|IT"}},
		{"closure inside func", `package x
func h() func(string) bool { return func(cc string) bool { return cc == "IR" } }`, []string{"h|IR"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, probs := scanSrc(t, tc.src)
			if len(probs) != 0 {
				t.Fatalf("unexpected problems: %v", probs)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInlineAllow(t *testing.T) {
	got, probs := scanSrc(t, `package x
func h(cc string) bool {
	return cc == "DE" // core-neutral:allow ADR-0049 hosting region
}`)
	if len(got) != 0 || len(probs) != 0 {
		t.Fatalf("marker should suppress: %v %v", got, probs)
	}

	// Marker inside a string literal is not a comment: no suppression.
	got, _ = scanSrc(t, `package x
func h(cc string) bool { return cc == "DE" && "// core-neutral:allow x" != "" }`)
	if len(got) != 1 {
		t.Fatalf("string-literal marker must not suppress, got %v", got)
	}

	// Marker on a different line does not suppress.
	got, _ = scanSrc(t, `package x
// core-neutral:allow reason
func h(cc string) bool { return cc == "DE" }`)
	if len(got) != 1 {
		t.Fatalf("marker on another line must not suppress, got %v", got)
	}

	// Empty reason is a problem, and does not suppress.
	got, probs = scanSrc(t, `package x
func h(cc string) bool { return cc == "DE" } // core-neutral:allow
`)
	if len(got) != 1 || len(probs) != 1 {
		t.Fatalf("empty-reason marker: got %v probs %v", got, probs)
	}
}

func TestInlineAllowConst(t *testing.T) {
	got, probs := scanSrc(t, `package x
const de = "DE" // core-neutral:allow hosting region
func h(cc string) bool { return cc == de }`)
	if len(got) != 0 || len(probs) != 0 {
		t.Fatalf("marker on const decl should suppress: %v %v", got, probs)
	}
}

func TestConstAcrossFiles(t *testing.T) {
	fs, _, err := scanPackage(map[string][]byte{
		"internal/x/a.go": []byte("package x\nconst de = \"DE\"\n"),
		"internal/x/b.go": []byte("package x\nfunc h(cc string) bool { return cc == de }\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Key() != "internal/x/a.go|<file>|DE" {
		t.Fatalf("want one finding keyed on the const's file, got %+v", fs)
	}
}

func TestScanTreeExclusions(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bad := "package x\nfunc h(c string) bool { return c == \"DE\" }\n"
	write("internal/x/a_test.go", bad)
	write("internal/x/testdata/b.go", bad)
	write("internal/db/migrations/c.go", bad)
	write("cmd/app/main.go", "package main\nfunc main() {}\n")
	write("internal/y/y.go", bad)
	write("mobile/m.go", bad)
	fs, _, n, err := scanTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("files scanned = %d, want 3", n)
	}
	if len(fs) != 2 || fs[0].Path != "internal/y/y.go" || fs[1].Path != "mobile/m.go" {
		t.Fatalf("findings = %+v", fs)
	}
}

func TestParseAllowlist(t *testing.T) {
	good := `# header

internal/a.go|h|"DE" # #2879 fiscal hard gate
internal/a.go|<file>|"x # y|z" # #2850 class a, kept by audit
`
	al, errs := parseAllowlist(strings.NewReader(good))
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if _, ok := al["internal/a.go|h|DE"]; !ok {
		t.Fatalf("missing entry: %v", al)
	}
	if e := al["internal/a.go|<file>|x # y|z"]; e.Card != "2850" {
		t.Fatalf("quoted literal with # and | not parsed: %+v", al)
	}

	for _, bad := range []string{
		`internal/a.go|h|"DE"`,              // no reason
		`internal/a.go|h|"DE" # #2879`,      // card but empty reason
		`internal/a.go|h|"DE" # no card`,    // no card
		`internal/a.go|h|DE # #2879 reason`, // unquoted literal
		`internal/a.go|"DE" # #2879 reason`, // missing func
		"internal/a.go|h|\"DE\" # #2879 r\ninternal/a.go|h|\"DE\" # #2879 dup",
	} {
		if _, errs := parseAllowlist(strings.NewReader(bad)); len(errs) == 0 {
			t.Errorf("expected parse error for %q", bad)
		}
	}
}

func TestCheck(t *testing.T) {
	f := []Finding{
		{Path: "internal/a.go", Func: "h", Literal: "DE", Line: 3, Kind: "country code"},
		{Path: "internal/a.go", Func: "h", Literal: "DE", Line: 9, Kind: "country code"},
		{Path: "internal/b.go", Func: "g", Literal: "TR", Line: 1, Kind: "country code"},
	}
	parse := func(s string) Allowlist {
		al, errs := parseAllowlist(strings.NewReader(s))
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		return al
	}
	cur := parse(`internal/a.go|h|"DE" # #2879 r
internal/b.go|g|"TR" # #2884 r
`)
	if p := check(f, cur, nil); len(p) != 0 {
		t.Fatalf("all allow-listed (one key covers both lines): %v", p)
	}

	// New offender.
	if p := check(f, parse(`internal/a.go|h|"DE" # #2879 r`), nil); len(p) != 1 || !strings.Contains(p[0], "internal/b.go:1") {
		t.Fatalf("new offender: %v", p)
	}

	// Stale entry.
	stale := parse(`internal/a.go|h|"DE" # #2879 r
internal/b.go|g|"TR" # #2884 r
internal/c.go|k|"AT" # #2879 gone
`)
	if p := check(f, stale, nil); len(p) != 1 || !strings.Contains(p[0], "stale") {
		t.Fatalf("stale: %v", p)
	}

	// Shrink-only: an entry absent from the base list fails.
	base := parse(`internal/a.go|h|"DE" # #2879 r`)
	if p := check(f, cur, &base); len(p) != 1 || !strings.Contains(p[0], "shrink") {
		t.Fatalf("growth vs base: %v", p)
	}
	// Same keys as base (reason reworded) is fine.
	base2 := parse(`internal/a.go|h|"DE" # #2879 other words
internal/b.go|g|"TR" # #2884 r
internal/z.go|q|"FR" # #2879 already removed here`)
	if p := check(f, cur, &base2); len(p) != 0 {
		t.Fatalf("shrunk list must pass: %v", p)
	}
}
