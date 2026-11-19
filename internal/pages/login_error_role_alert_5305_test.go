package pages

// ut-docs#5305: a `.login-error` banner that appears in response to an
// action (a failed submit re-rendered by the server, or a JS-filled status
// line) must carry role="alert" so screen readers announce it. Static
// warnings rendered with the page may opt out with a same-line
// `login-error-role:allow <reason>`.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// loginErrorTagRe matches a whole opening tag whose class attribute contains
// the login-error token. Go-template actions inside a tag contain no '>'.
var loginErrorTagRe = regexp.MustCompile(`(?s)<[a-zA-Z][a-zA-Z0-9]*\b[^>]*?\sclass="([^"]*)"[^>]*>`)

var (
	loginErrorClassRe = regexp.MustCompile(`(?:^|\s)login-error(?:\s|$)`)
	roleAlertRe       = regexp.MustCompile(`\srole="alert"`)
	jsRoleAlertRe     = regexp.MustCompile(`setAttribute\(\s*['"]role['"]\s*,\s*['"]alert['"]\s*\)`)
	roleAllowRe       = regexp.MustCompile(`login-error-role:allow\s+[^\s-]`)
)

// loginErrorRoleViolations returns "file:line: ..." messages for every
// login-error tag in an HTML template that lacks role="alert" and a reviewed
// same-line allow marker with a reason.
func loginErrorRoleViolations(path, src string) []string {
	var out []string
	for _, m := range loginErrorTagRe.FindAllStringSubmatchIndex(src, -1) {
		tag := src[m[0]:m[1]]
		class := src[m[2]:m[3]]
		if !loginErrorClassRe.MatchString(class) {
			continue
		}
		if roleAlertRe.MatchString(tag) {
			continue
		}
		start := strings.LastIndex(src[:m[0]], "\n") + 1
		end := strings.Index(src[m[0]:], "\n")
		if end < 0 {
			end = len(src)
		} else {
			end += m[0]
		}
		if roleAllowRe.MatchString(src[start:end]) {
			continue
		}
		line := strings.Count(src[:m[0]], "\n") + 1
		out = append(out, fmt.Sprintf("%s:%d: .login-error without role=\"alert\" -- add role=\"alert\" to the tag so screen readers announce it, "+
			"or (static warning rendered with the page) add a same-line `<!-- login-error-role:allow <reason> -->` (ut-docs#5305)", path, line))
	}
	return out
}

// loginErrorJSViolation flags a shipped (non-vendor) script that builds or
// toggles a login-error element but never sets role=alert.
func loginErrorJSViolation(path, src string) string {
	if !strings.Contains(src, "login-error") {
		return ""
	}
	if jsRoleAlertRe.MatchString(src) {
		return ""
	}
	line := strings.Count(src[:strings.Index(src, "login-error")], "\n") + 1
	return fmt.Sprintf("%s:%d: script uses login-error but never calls setAttribute('role', 'alert') -- set it on the created element (ut-docs#5305)", path, line)
}

func TestLoginErrorRoleChecker_5305(t *testing.T) {
	cases := []struct {
		name string
		src  string
		bad  int
	}{
		{"bare p", `{{ if .errKey }}<p class="login-error">x</p>{{ end }}`, 1},
		{"role last", `<p class="login-error" role="alert">x</p>`, 0},
		{"role first", `<p role="alert" class="login-error">x</p>`, 0},
		{"after hidden and template action", `<p id="a" class="login-error" role="alert"{{ if not .E }} hidden{{ end }}>x</p>`, 0},
		{"template action but no role", `<p id="a" class="login-error"{{ if not .E }} hidden{{ end }}>x</p>`, 1},
		{"extra classes", `<p class="save login-error x">x</p>`, 1},
		{"allow marker with reason", `<p class="login-error" data-x>x</p> <!-- login-error-role:allow static warning -->`, 0},
		{"allow marker without reason", `<p class="login-error">x</p> <!-- login-error-role:allow -->`, 1},
		{"allow marker on another line", "<!-- login-error-role:allow reason -->\n<p class=\"login-error\">x</p>", 1},
		{"multi-line good", "<p\n  class=\"login-error\"\n  role=\"alert\">\nx</p>", 0},
		{"multi-line bad", "<p\n  class=\"login-error\"\n  id=\"a\">\nx</p>", 1},
		{"other class token", `<p class="login-errors">x</p><p class="login-error-x">y</p>`, 0},
		{"data-role is not role", `<p class="login-error" data-role="alert">x</p>`, 1},
		{"data-class is not class", `<p data-class="login-error">x</p>`, 0},
		{"two bad", "<p class=\"login-error\">a</p>\n<p class=\"login-error\">b</p>", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := loginErrorRoleViolations("t.html", c.src)
			if len(got) != c.bad {
				t.Fatalf("violations = %d, want %d: %v", len(got), c.bad, got)
			}
		})
	}
	v := loginErrorRoleViolations("t.html", "a\nb\n<p class=\"login-error\">x</p>")
	if len(v) != 1 || !strings.HasPrefix(v[0], "t.html:3:") {
		t.Fatalf("want file:line t.html:3, got %v", v)
	}

	if loginErrorJSViolation("a.js", "var l; l.className = 'login-error'; l.setAttribute('role', 'alert');") != "" {
		t.Error("js with role=alert flagged")
	}
	if loginErrorJSViolation("a.js", "x\nl.className = 'login-error';") == "" {
		t.Error("js without role=alert not flagged")
	}
	if loginErrorJSViolation("a.js", "nothing here") != "" {
		t.Error("unrelated js flagged")
	}
}

func TestLoginErrorBannersAnnounce_5305(t *testing.T) {
	files := webFiles(t)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var bad []string
	for _, p := range paths {
		switch {
		case strings.HasPrefix(p, "web/ui/") && strings.HasSuffix(p, ".html"):
			bad = append(bad, loginErrorRoleViolations(p, files[p])...)
		case strings.HasPrefix(p, "web/public/") && strings.HasSuffix(p, ".js"):
			if v := loginErrorJSViolation(p, files[p]); v != "" {
				bad = append(bad, v)
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("%d login-error banner(s) are not announced to screen readers:\n%s", len(bad), strings.Join(bad, "\n"))
	}
}
