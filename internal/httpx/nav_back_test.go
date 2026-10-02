package httpx

import (
	"bytes"
	"net/http/httptest"
	"regexp"
	"testing"
)

// renderNavAt renders nav.html exactly as a whole-page render of path does.
func renderNavAt(t *testing.T, path string) string {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	tpl, err := ClonedTemplate("nav-back-test:"+t.Name(), "base.html", withHelpHref(FuncsFor("en"), r),
		"ui/layouts/base.html", "ui/partials/nav.html", "ui/partials/bugreport_panel.html")
	if err != nil {
		t.Fatalf("ClonedTemplate: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "nav", nil); err != nil {
		t.Fatalf("execute nav: %v", err)
	}
	return buf.String()
}

var navBackRe = regexp.MustCompile(`<a [^>]*data-testid="nav-back"[^>]*>`)

// ut-docs#3352 / ADR-0137: every shell page but Sell carries one Back link
// whose href is the page's declared parent — the no-JS and no-history
// fallback; app.js upgrades a tap to history.back() when there is an in-app
// page to return to.
func TestNav_BackLinkHrefIsTheDeclaredParent(t *testing.T) {
	cases := map[string]string{
		"/menu":              "/",
		"/items":             "/menu",
		"/inventory":         "/items",
		"/settings/printers": "/settings",
		"/registers":         "/admin",
	}
	for path, want := range cases {
		nav := renderNavAt(t, path)
		tag := navBackRe.FindString(nav)
		if tag == "" {
			t.Errorf("%s: nav has no Back link", path)
			continue
		}
		if !regexp.MustCompile(`href="` + regexp.QuoteMeta(want) + `"`).MatchString(tag) {
			t.Errorf("%s: Back link %s, want href=%q", path, tag, want)
		}
		if !regexp.MustCompile(`aria-label="[^"]+"`).MatchString(tag) {
			t.Errorf("%s: Back link %s has no accessible name", path, tag)
		}
	}
}

func TestNav_NoBackLinkOnSell(t *testing.T) {
	if tag := navBackRe.FindString(renderNavAt(t, "/")); tag != "" {
		t.Errorf("Sell is the root and must have no Back link, got %s", tag)
	}
}
