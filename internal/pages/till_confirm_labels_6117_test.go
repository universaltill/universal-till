package pages

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#6117: every hx-confirm opener names its action on the dialog's
// confirm button (data-confirm-ok) instead of the generic "Confirm", and the
// held/parked-order cancel openers rename Cancel to "Keep order".

// blankSpans replaces every match of re in s with spaces, keeping newlines so
// line numbers stay true.
func blankSpans(s string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Map(func(r rune) rune {
			if r == '\n' {
				return '\n'
			}
			return ' '
		}, m)
	})
}

var (
	reTplComment  = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reTagStart    = regexp.MustCompile(`<[A-Za-z]`)
	reOkTpl       = regexp.MustCompile(`data-confirm-ok="\{\{\s*T\s+"([^"]+)"\s*\}\}"`)
)

// hxConfirmMisses returns one "file:line: why" per hx-confirm opener in src
// whose own tag lacks a verb label. isGo selects the Go-emitter form
// (data-confirm-ok="%s") over the template form ({{ T "key" }}, key in en).
func hxConfirmMisses(name, src string, isGo bool, en map[string]string) []string {
	if isGo {
		var kept []string
		for _, l := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "//") {
				l = ""
			}
			kept = append(kept, l)
		}
		// An interpreted string writes the attribute as hx-confirm=\"%s\";
		// unescape so it is found like a raw (backtick) literal.
		src = strings.ReplaceAll(strings.Join(kept, "\n"), `\"`, `"`)
	} else {
		src = blankSpans(blankSpans(src, reTplComment), reHTMLComment)
	}
	var out []string
	const needle = `hx-confirm="`
	for from := 0; ; {
		i := strings.Index(src[from:], needle)
		if i < 0 {
			break
		}
		i += from
		from = i + len(needle)
		line := 1 + strings.Count(src[:i], "\n")
		// opener tag: nearest preceding "<x" ... first ">" outside {{ }}
		start := -1
		for _, m := range reTagStart.FindAllStringIndex(src[:i], -1) {
			start = m[0]
		}
		if start < 0 {
			out = append(out, fmt.Sprintf("%s:%d: hx-confirm outside any tag", name, line))
			continue
		}
		end := len(src)
		for k := i; k < len(src); k++ {
			if strings.HasPrefix(src[k:], "{{") {
				if e := strings.Index(src[k:], "}}"); e >= 0 {
					k += e + 1
					continue
				}
			}
			if src[k] == '>' {
				end = k
				break
			}
		}
		tag := src[start:end]
		switch {
		case !strings.Contains(tag, `data-confirm-ok="`):
			out = append(out, fmt.Sprintf("%s:%d: hx-confirm opener has no data-confirm-ok on the same tag", name, line))
		case isGo && !strings.Contains(tag, `data-confirm-ok="%s"`):
			out = append(out, fmt.Sprintf("%s:%d: Go emitter must write data-confirm-ok=\"%%s\" (escaped, translated)", name, line))
		case !isGo:
			m := reOkTpl.FindStringSubmatch(tag)
			if m == nil {
				out = append(out, fmt.Sprintf("%s:%d: data-confirm-ok must be {{ T \"key\" }}", name, line))
			} else if en[m[1]] == "" {
				out = append(out, fmt.Sprintf("%s:%d: data-confirm-ok key %q is not in en.json", name, line, m[1]))
			}
		}
	}
	return out
}

func TestHxConfirm_EveryOpenerNamesItsVerb(t *testing.T) {
	en := enLocale(t)
	root := "../.."
	if _, err := os.Stat("web/ui"); err == nil {
		root = "."
	}
	var misses []string
	scanned := 0
	walk := func(dir string, isGo bool) {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := ".html"
			if isGo {
				ext = ".go"
			}
			if !strings.HasSuffix(p, ext) || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			scanned++
			rel, _ := filepath.Rel(root, p)
			misses = append(misses, hxConfirmMisses(rel, string(b), isGo, en)...)
			return nil
		})
	}
	walk("web/ui", false)
	walk("internal", true)
	walk("cmd", true)
	if scanned < 50 {
		t.Fatalf("only scanned %d files; the walk is broken", scanned)
	}
	for _, m := range misses {
		t.Error(m)
	}
}

func TestHxConfirm_LabelCheckerCatchesMissing(t *testing.T) {
	en := map[string]string{"k.ok": "Go"}
	cases := []struct {
		name, src string
		isGo      bool
		want      int
	}{
		{"button missing attr", `<button hx-post="/x" hx-confirm="{{ T "q" }}">x</button>`, false, 1},
		{"attr on parent only", `<div data-confirm-ok="{{ T "k.ok" }}"><button hx-confirm="{{ T "q" }}">x</button></div>`, false, 1},
		{"form carrier ok", `<form hx-post="/x" hx-confirm="{{ T "q" }}" data-confirm-ok="{{ T "k.ok" }}"><button>x</button></form>`, false, 0},
		{"attr before hx-confirm ok", `<button data-confirm-ok="{{ T "k.ok" }}" hx-confirm="{{ T "q" }}">x</button>`, false, 0},
		{"unknown key", `<button hx-confirm="{{ T "q" }}" data-confirm-ok="{{ T "nope" }}">x</button>`, false, 1},
		{"literal text not T", `<button hx-confirm="{{ T "q" }}" data-confirm-ok="Delete">x</button>`, false, 1},
		{"if with > inside braces", `<button {{ if gt .N 0 }}hx-confirm="{{ T "q" }}"{{ end }} data-confirm-ok="{{ T "k.ok" }}">x</button>`, false, 0},
		{"if with > inside braces, missing", `<button {{ if gt .N 0 }}hx-confirm="{{ T "q" }}"{{ end }}>x</button>`, false, 1},
		{"template comment ignored", `{{/* <button hx-confirm="x"> */}}<p>hi</p>`, false, 0},
		{"html comment ignored", `<!-- <button hx-confirm="x"> --><p>hi</p>`, false, 0},
		{"go concatenated literal ok", "\"<button hx-post=\\\"/x\\\" hx-confirm=\\\"%s\\\" \" +\n\"data-confirm-ok=\\\"%s\\\">%s</button>\"", true, 0},
		{"go interpreted string missing", "\"<button hx-post=\\\"/x\\\" hx-confirm=\\\"%s\\\">%s</button>\"", true, 1},
		{"go missing", "`<button class=\"btn\" hx-confirm=\"%s\">%s</button>`", true, 1},
		{"go comment line ignored", "// <button hx-confirm=\"%s\">\nx := 1", true, 0},
	}
	for _, c := range cases {
		if got := hxConfirmMisses("f", c.src, c.isGo, en); len(got) != c.want {
			t.Errorf("%s: want %d misses, got %v", c.name, c.want, got)
		}
	}
}

func TestHxConfirm_OrderCancelDismiss(t *testing.T) {
	for _, f := range []string{"web/ui/pages/open_orders.html", "web/ui/partials/parked_orders.html"} {
		src := readRepoFile(t, f)
		i := strings.Index(src, `hx-confirm="{{ printf (T "open_orders.cancel.confirm_named")`)
		if i < 0 {
			t.Fatalf("%s: cancel opener not found", f)
		}
		start := strings.LastIndex(src[:i], "<button")
		end := i + strings.Index(src[i:], ">")
		tag := src[start:end]
		for _, want := range []string{
			`data-confirm-dismiss="{{ T "confirm.keep_order" }}"`,
			`data-confirm-ok="{{ T "open_orders.cancel.action" }}"`,
		} {
			if !strings.Contains(tag, want) {
				t.Errorf("%s: cancel opener lacks %s", f, want)
			}
		}
	}
	en := enLocale(t)
	for k, v := range map[string]string{"confirm.keep_order": "Keep order", "confirm.remove": "Remove", "confirm.delete": "Delete"} {
		if en[k] != v {
			t.Errorf("en.json %s = %q, want %q", k, en[k], v)
		}
	}
	meta := readRepoFile(t, "web/i18n-meta/en.meta.json")
	for _, k := range []string{"confirm.keep_order", "confirm.remove", "confirm.delete"} {
		if !strings.Contains(meta, `"`+k+`"`) {
			t.Errorf("en.meta.json lacks %s", k)
		}
	}
}

func TestTillConfirmJS_DismissReset(t *testing.T) {
	js := readRepoFile(t, "web/public/till-confirm.js")
	for _, want := range []string{
		`closest('[data-confirm-dismiss]')`,
		`cancelBtn.textContent = opts.dismiss ||`,
		`dismiss:`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("till-confirm.js lacks %q", want)
		}
	}
	app := readRepoFile(t, "web/public/app.js")
	i := strings.Index(app, `window.UT.confirm(q, {`)
	if i < 0 {
		t.Fatal("app.js tile-remove UT.confirm call not found")
	}
	call := app[i : i+strings.Index(app[i:], "})")]
	for _, want := range []string{"data-confirm-ok", "data-confirm-dismiss", "data-confirm-tone"} {
		if !strings.Contains(app[i-600:i+len(call)], want) {
			t.Errorf("app.js tile-remove confirm does not read %s", want)
		}
	}
}
