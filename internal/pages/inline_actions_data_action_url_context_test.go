package pages

import (
	"bytes"
	"html/template"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/web"
)

// ut-docs#3325 review. Go's html/template strips a leading "data-" before
// deciding an attribute's escaping context (html/template/attr.go
// attrType). The tester pass found the "on" prefix trap (data-on-* was
// JS-escaped; every hook was renamed). The SAME strip has a second trap
// that the rename does not cover: "data-action" strips to "action", which
// attrTypeMap lists as a URL attribute (<form action=>), so every {{ }}
// inside a data-action value is URL-escaped, not plain-attribute-escaped:
//
//   - a {{ }} that is the FIRST thing in the value goes through the
//     URL-scheme filter: "close:x" has a "close" scheme, so it renders as
//     "#ZgotmplZ" and the step never runs;
//   - a {{ }} after a `#` or `?` in the value is in the query/fragment part
//     and gets FULLY percent-encoded: `,` -> %2c, `/` -> %2f, `:` -> %3a,
//     which breaks inline-actions.js's name:a,b step syntax.
//
// Elsewhere in the value (today: receipt numbers after `go:/refund/`,
// `go:/journal/`, `receipt-print:` and `receipt-ask-print:`) the value is
// only URL-normalised, which leaves the server-generated `T<n>-NNNNNNNNN`
// receipt numbers byte-identical. These tests pin both breaking shapes out
// of every shipped template, and prove (against the real html/template)
// that they really do break.
var tplDataActionAttr = regexp.MustCompile(`data-action="([^"]*)"`)

// tplOutputAction matches a {{ }} that prints something, as opposed to a
// control action ({{ if }}, {{ else }}, {{ end }}, {{ range }}, {{ with }},
// {{/* */}}) which only chooses between static texts.
var tplOutputAction = regexp.MustCompile(`\{\{-?\s*(?:if|else|end|range|with|define|template|block|/\*)\b`)

func dataActionURLPartViolations(path, src string) []string {
	var out []string
	for _, m := range tplDataActionAttr.FindAllStringSubmatch(src, -1) {
		v := m[1]
		idx := 0
		for {
			rel := strings.Index(v[idx:], "{{")
			if rel < 0 {
				break
			}
			at := idx + rel
			idx = at + 2
			if tplOutputAction.MatchString(v[at:]) {
				continue
			}
			if strings.TrimSpace(v[:at]) == "" {
				out = append(out, path+": data-action starts with a template value (html/template URL-scheme filter -> #ZgotmplZ): "+v)
				continue
			}
			if strings.ContainsAny(v[:at], "#?") {
				out = append(out, path+": data-action has a template value after # or ? (html/template percent-encodes , / : there): "+v)
			}
		}
	}
	return out
}

func TestTemplatesDataActionTemplateValuesStayOutOfURLParts(t *testing.T) {
	var found []string
	err := fs.WalkDir(web.FS, "ui", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		b, err := fs.ReadFile(web.FS, p)
		if err != nil {
			return err
		}
		found = append(found, dataActionURLPartViolations("web/"+p, string(b))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("data-action is a URL-context attribute to html/template (see web/public/inline-actions.js's header); move the templated value into a plain data-* attribute the step reads instead:\n  %s", strings.Join(found, "\n  "))
	}
}

// The lint above is only worth having if html/template really does what it
// claims, so this executes the two shapes for real, plus the shape every
// shipped template uses, against the installed Go.
func TestDataActionURLContextRepro(t *testing.T) {
	render := func(src, v string) string {
		t.Helper()
		var buf bytes.Buffer
		if err := template.Must(template.New("x").Parse(src)).Execute(&buf, map[string]string{"V": v}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if got := render(`<b data-action="{{ .V }}">`, "close:x"); !strings.Contains(got, "#ZgotmplZ") {
		t.Fatalf("expected the URL-scheme filter to reject a leading step value, got %s", got)
	}
	if got := render(`<b data-action="ajax-get:/x,#t show:{{ .V }}">`, "a,b/c"); !strings.Contains(got, "a%2cb%2fc") {
		t.Fatalf("expected full percent-encoding after #, got %s", got)
	}
	if got := render(`<b data-action="go:/refund/{{ .V }}">`, "T2-000000042"); !strings.Contains(got, `data-action="go:/refund/T2-000000042"`) {
		t.Fatalf("a receipt number after a path must survive untouched, got %s", got)
	}

	// And the lint itself sees both breaking shapes, and nothing else.
	bad := `<b data-action="{{ .V }}"><b data-action="ajax-get:/x,#t show:{{ .V }}"><b data-action="x?y={{ .V }}">`
	if got := dataActionURLPartViolations("f", bad); len(got) != 3 {
		t.Fatalf("expected 3 violations, got %d: %v", len(got), got)
	}
	good := `<b data-action="go:/refund/{{ .V }}"><b data-action="{{ if .V }}close:a{{ else }}close:b{{ end }}"><b data-action="ok show:x">`
	if got := dataActionURLPartViolations("f", good); len(got) != 0 {
		t.Fatalf("expected no violations, got %v", got)
	}
}
