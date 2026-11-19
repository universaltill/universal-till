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

// ut-docs#6148: every JS UT.confirm( caller names its verb (ok) instead of the
// generic "Confirm", and the three Update-now confirms share one tone.

var reUTConfirm = regexp.MustCompile(`\bUT\.confirm\s*\(`)

// blankJSComments blanks // and /* */ comments, respecting string literals.
// Newlines are kept so line numbers stay true.
func blankJSComments(s string) (string, []bool) {
	b := []byte(s)
	inStr := make([]bool, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '{' && i+1 < len(b) && b[i+1] == '{':
			if e := strings.Index(s[i:], "}}"); e >= 0 {
				i += e + 2
			} else {
				i += 2
			}
		case c == '\'' || c == '"' || c == '`':
			q := c
			i++
			for i < len(b) && b[i] != q {
				inStr[i] = true
				if b[i] == '\\' {
					i++
				}
				if q != '`' && i < len(b) && b[i] == '\n' {
					break
				}
				i++
			}
			i++
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				b[i] = ' '
				i++
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			for i < len(b) && !(b[i] == '*' && i+1 < len(b) && b[i+1] == '/') {
				if b[i] != '\n' {
					b[i] = ' '
				}
				i++
			}
			if i+1 < len(b) {
				b[i], b[i+1] = ' ', ' '
				i += 2
			}
		default:
			i++
		}
	}
	return string(b), inStr
}

var reScript = regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)

// jsOnly keeps only <script> bodies of an HTML file (everything else blanked,
// newlines kept) so prose apostrophes cannot derail the string scanner.
func jsOnly(src string) string {
	keep := make([]bool, len(src))
	for _, m := range reScript.FindAllStringSubmatchIndex(src, -1) {
		for i := m[2]; i < m[3]; i++ {
			keep[i] = true
		}
	}
	b := []byte(src)
	for i := range b {
		if !keep[i] && b[i] != '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}

// callArgs returns the top-level args of the call whose "(" is at src[open],
// or ok=false if unbalanced.
func callArgs(src string, open int) (args []string, ok bool) {
	depth := 0
	start := open + 1
	for i := open; i < len(src); i++ {
		c := src[i]
		switch {
		case strings.HasPrefix(src[i:], "{{"):
			e := strings.Index(src[i:], "}}")
			if e < 0 {
				return nil, false
			}
			i += e + 1
		case c == '\'' || c == '"' || c == '`':
			q := c
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' {
					i++
				}
				i++
			}
		case c == '(' || c == '{' || c == '[':
			depth++
		case c == ')' || c == '}' || c == ']':
			depth--
			if depth == 0 {
				args = append(args, strings.TrimSpace(src[start:i]))
				return args, true
			}
		case c == ',' && depth == 1:
			args = append(args, strings.TrimSpace(src[start:i]))
			start = i + 1
		}
	}
	return nil, false
}

var reOkKey = regexp.MustCompile(`^ok\s*:\s*(.*)$`)

// topLevelOk reports whether the object literal obj has a depth-1 key ok with
// a non-empty value.
func topLevelOk(obj string) bool {
	if !strings.HasPrefix(obj, "{") || !strings.HasSuffix(obj, "}") {
		return false
	}
	inner := obj[1 : len(obj)-1]
	depth, start := 0, 0
	var parts []string
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case strings.HasPrefix(inner[i:], "{{"):
			if e := strings.Index(inner[i:], "}}"); e >= 0 {
				i += e + 1
			}
		case c == '\'' || c == '"' || c == '`':
			q := c
			i++
			for i < len(inner) && inner[i] != q {
				if inner[i] == '\\' {
					i++
				}
				i++
			}
		case c == '(' || c == '{' || c == '[':
			depth++
		case c == ')' || c == '}' || c == ']':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, strings.TrimSpace(inner[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(inner[start:]))
	for _, p := range parts {
		if m := reOkKey.FindStringSubmatch(p); m != nil {
			v := strings.TrimSpace(m[1])
			return v != "" && v != "''" && v != `""`
		}
	}
	return false
}

// utConfirmMisses returns "file:line: why" per UT.confirm( caller without a
// non-empty ok, and the number of calls seen.
func utConfirmMisses(name, src string) ([]string, int) {
	allow := map[int]bool{}
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, "confirm-verb:allow") {
			allow[i+1] = true
		}
	}
	if strings.HasSuffix(name, ".html") {
		src = jsOnly(blankSpans(blankSpans(src, reTplComment), reHTMLComment))
	}
	src, inStr := blankJSComments(src)
	var out []string
	n := 0
	for _, m := range reUTConfirm.FindAllStringIndex(src, -1) {
		if inStr[m[0]] {
			continue
		}
		n++
		line := 1 + strings.Count(src[:m[0]], "\n")
		if allow[line] {
			continue
		}
		args, ok := callArgs(src, m[1]-1)
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s:%d: unbalanced UT.confirm( call", name, line))
		case len(args) < 2:
			out = append(out, fmt.Sprintf("%s:%d: UT.confirm( without an options object naming ok", name, line))
		case !topLevelOk(args[1]):
			out = append(out, fmt.Sprintf("%s:%d: UT.confirm( options lack a non-empty top-level ok", name, line))
		}
	}
	return out, n
}

func TestUTConfirm_EveryCallerNamesItsVerb(t *testing.T) {
	root := "../.."
	if _, err := os.Stat("web/ui"); err == nil {
		root = "."
	}
	var misses []string
	calls := 0
	for _, dir := range []string{"web/public", "web/ui"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(p)
			if (ext != ".js" && ext != ".html") || (ext == ".js" && !strings.Contains(p, "web/public")) || d.Name() == "till-confirm.js" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			m, n := utConfirmMisses(rel, string(b))
			misses = append(misses, m...)
			calls += n
			return nil
		})
	}
	if calls < 7 {
		t.Fatalf("found only %d UT.confirm( calls; the walk is broken (want >= 7)", calls)
	}
	for _, m := range misses {
		t.Error(m)
	}
}

func TestUTConfirm_LabelCheckerCatchesMissing(t *testing.T) {
	cases := []struct {
		name, src string
		want      int
	}{
		{"no opts", `UT.confirm(q)`, 1},
		{"opts without ok", `UT.confirm(q, { opener: b })`, 1},
		{"multiline ok", "UT.confirm(q, {\n opener: b,\n ok: T.x\n})", 0},
		{"non-literal opts", `UT.confirm(q, opts)`, 1},
		{"allow marker", `UT.confirm(q, opts) // confirm-verb:allow reason`, 0},
		{"line comment", `// UT.confirm(q)`, 0},
		{"inside string", `var s = "UT.confirm(" ;`, 0},
		{"nested ok only", `UT.confirm(q, { opener: { ok: 1 } })`, 1},
		{"empty ok", `UT.confirm(q, { ok: '' })`, 1},
		{"template action with paren", `UT.confirm(q, { ok: '{{ T "a)b" }}' })`, 0},
		{"nested call in first arg", `UT.confirm(f(a, b), { ok: x })`, 0},
		{"url in string not a comment", "var u = 'http://x'; UT.confirm(q)", 1},
	}
	for _, c := range cases {
		if got, _ := utConfirmMisses("f.js", c.src); len(got) != c.want {
			t.Errorf("%s: want %d misses, got %v", c.name, c.want, got)
		}
	}
}

// All three Update-now confirms (tills roster, setup wizard banner, Settings
// banner) use the neutral tone.
func TestUpdateNow_ConfirmToneConsistent(t *testing.T) {
	cases := []struct{ file, marker string }{
		{"web/ui/partials/tills_roster.html", `tills.update_now.confirm`},
		{"internal/pages/setup_update_check.go", `hx-post="/api/setup/update-apply"`},
		{"internal/pages/update_api.go", `hx-post="/api/update/apply"`},
	}
	for _, c := range cases {
		src := readRepoFile(t, c.file)
		i := strings.Index(src, c.marker)
		if i < 0 {
			t.Errorf("%s: opener marker %q not found", c.file, c.marker)
			continue
		}
		start := strings.LastIndex(src[:i], "<")
		end := i + strings.Index(src[i:], "\n")
		if !strings.Contains(src[start:end], `data-confirm-tone="neutral"`) {
			t.Errorf("%s: Update-now opener lacks data-confirm-tone=\"neutral\"", c.file)
		}
	}
}
