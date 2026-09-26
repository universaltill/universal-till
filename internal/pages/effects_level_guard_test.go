package pages

// ADR-0119 (ut-docs#2859) static and render guards for the visual effects
// level: one fx-* class on <html>, the @view-transition opt-in omitted
// under Light, an html.fx-light block in app.css that zeroes motion, and
// every JS motion path going through the one UT.motionOff() predicate.
// e2e/tests/effects-level-2859.spec.ts proves the browser half.

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

func renderSettingsAt(t *testing.T, level string) string {
	t.Helper()
	mux, _, _ := newFullAuthDeps(t)
	httpx.InitEffectsLevel(level)
	t.Cleanup(func() { httpx.InitEffectsLevel("full") })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	return rec.Body.String()
}

var htmlOpenTag = regexp.MustCompile(`(?s)<html\b[^>]*>`)

func TestBaseHTMLRendersExactlyOneFxClass(t *testing.T) {
	for _, level := range []string{"full", "balanced", "light"} {
		body := renderSettingsAt(t, level)
		tag := htmlOpenTag.FindString(body)
		if tag == "" {
			t.Fatalf("%s: no <html> tag rendered", level)
		}
		if n := strings.Count(tag, "class="); n != 1 {
			t.Fatalf("%s: <html> has %d class attributes: %s", level, n, tag)
		}
		fx := regexp.MustCompile(`\bfx-[a-z]+\b`).FindAllString(tag, -1)
		if len(fx) != 1 || fx[0] != "fx-"+level {
			t.Errorf("%s: <html> fx classes = %v, want exactly [fx-%s]: %s", level, fx, level, tag)
		}
		if strings.Contains(tag, "fx-auto") {
			t.Errorf("%s: fx-auto rendered — auto must be resolved on the server", level)
		}
		if !strings.Contains(body, "UT.fxLevel = '"+level+"';") {
			t.Errorf("%s: window.UT.fxLevel not rendered as %q", level, level)
		}
	}
}

// ADR-0119 §3 (server layer): the inline opt-in is rendered only when the
// level is not Light — the at-rule cannot be scoped by a class.
func TestBaseHTMLOmitsViewTransitionOptInUnderLight(t *testing.T) {
	const optIn = "<style>@view-transition{navigation:auto}"
	for _, level := range []string{"full", "balanced"} {
		if body := renderSettingsAt(t, level); !strings.Contains(body, optIn) {
			t.Errorf("%s: the inline @view-transition opt-in is missing", level)
		}
	}
	if body := renderSettingsAt(t, "light"); strings.Contains(body, "@view-transition") {
		t.Errorf("light: an @view-transition rule was rendered — cross-document navigations would still snapshot the page")
	}
	src := readBaseHTML(t)
	if !strings.Contains(src, `{{ if ne fxlevel "light" }}<style>@view-transition{navigation:auto}`) {
		t.Errorf(`base.html: the inline opt-in must be wrapped in {{ if ne fxlevel "light" }}`)
	}
}

// ADR-0119 §3 (CSS layer): an html.fx-light wildcard next to the
// reduced-motion block zeroes transitions and animations, removes shadow,
// blur and filter, and keeps the progress spinners turning.
func TestAppCSSHasFxLightBlockZeroingMotion(t *testing.T) {
	css := readAppCSS(t)
	block := extractBlock(t, css, "html.fx-light *, html.fx-light *::before, html.fx-light *::after")
	for _, want := range []string{
		"transition-duration: 0s !important",
		"animation: none !important",
		"box-shadow: none !important",
		"text-shadow: none !important",
		"filter: none !important",
		"backdrop-filter: none !important",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("html.fx-light wildcard lacks %q: %s", want, block)
		}
	}
	if strings.Contains(block, "background-image") {
		t.Errorf("html.fx-light wildcard must not touch background-image (icons can be background images)")
	}
	if !strings.Contains(css, "html.fx-light::view-transition-group(*), html.fx-light::view-transition-old(*), html.fx-light::view-transition-new(*) { animation: none !important; }") {
		t.Errorf("app.css lacks the html.fx-light ::view-transition-* override")
	}
	if !strings.Contains(css, "html.fx-light #refresh-indicator, html.fx-light .animate-spin { animation: spin 1s linear infinite !important; }") {
		t.Errorf("html.fx-light must keep the progress spinners turning (essential motion)")
	}
	// Next to the reduced-motion block, so the two floors are read together.
	rm := strings.Index(css, "@media (prefers-reduced-motion: reduce)")
	fx := strings.Index(css, "html.fx-light *, html.fx-light *::before")
	if rm < 0 || fx < rm || fx-rm > 6000 {
		t.Errorf("the html.fx-light block must sit right after the reduced-motion block (rm=%d fx=%d)", rm, fx)
	}
	// Balanced: the ADR-0118 push shortened to 200ms.
	if !strings.Contains(css, "html.fx-balanced { --ut-motion-ms: 200ms;") {
		t.Errorf("html.fx-balanced must shorten --ut-motion-ms to 200ms")
	}
	// No physical direction in the new blocks (RTL).
	start := strings.Index(css, "ADR-0119 (ut-docs#2859)")
	end := strings.Index(css[start:], "/* ====")
	for _, bad := range []string{"margin-left", "margin-right", "padding-left", "padding-right", "text-align: left", "text-align: right", " left:", " right:"} {
		if strings.Contains(css[start:start+end], bad) {
			t.Errorf("effects-level CSS uses a physical property %q — logical properties only", bad)
		}
	}
}

// ADR-0119 §3 (JS layer): one shared predicate, defined in the first
// script of the document, reduced motion as its floor.
func TestBaseHTMLMotionOffPredicate(t *testing.T) {
	html := readBaseHTML(t)
	def := strings.Index(html, "UT.motionOff = function () {")
	if def < 0 {
		t.Fatalf("base.html must define UT.motionOff")
	}
	if !strings.Contains(html[def:def+200], "!!(reduce && reduce.matches) || UT.fxLevel === 'light'") {
		t.Errorf("UT.motionOff must be reduced motion OR the Light level: %s", html[def:def+200])
	}
	if first := strings.Index(html, "<script>"); first < 0 || def < first || strings.Index(html[first+1:], "<script") < def-first-1 {
		t.Errorf("UT.motionOff must be defined in the FIRST <script> of base.html, before every other script uses it")
	}
	if app := strings.Index(html, `src="/public/app.js`); app >= 0 && app < def {
		t.Errorf("UT.motionOff must be defined before app.js is loaded")
	}
}

// Every motion path that used to check prefers-reduced-motion alone now
// goes through UT.motionOff() — so Light switches it off too.
func TestEveryMotionPathUsesMotionOff(t *testing.T) {
	html := readBaseHTML(t)
	// pageswap, pagereveal, htmx:beforeTransition.
	for _, want := range []string{
		"if (UT.motionOff()) e.viewTransition.skipTransition();",
		"if (UT.motionOff()) { if (vt.skipTransition) vt.skipTransition(); return; }",
		"if (UT.motionOff() || !document.startViewTransition) e.preventDefault();",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("base.html motion path does not use UT.motionOff(): missing %q", want)
		}
	}
	// The media query is read exactly once: inside the predicate.
	if n := strings.Count(html, "matchMedia('(prefers-reduced-motion: reduce)')"); n != 1 {
		t.Errorf("base.html reads prefers-reduced-motion %d times — every path must go through UT.motionOff()", n)
	}
	if strings.Contains(html, "reduce && reduce.matches) ||") && !strings.Contains(html, "!!(reduce && reduce.matches) || UT.fxLevel") {
		t.Errorf("a bare reduced-motion check is back in base.html")
	}
	app := readAppJS(t)
	if !strings.Contains(app, "window.UT.motionOff()") || !strings.Contains(app, "    if (motionOff()) return;") {
		t.Errorf("app.js's swap/panel ease must skip via UT.motionOff()")
	}
	if strings.Contains(app, "if (mq && mq.matches) return;") {
		t.Errorf("app.js still gates the swap ease on the bare media query")
	}
	rd := readRecordDialogJS(t)
	if !strings.Contains(rd, "window.UT.motionOff()") || !strings.Contains(rd, "if (!motionOff()) dialog.classList.add('ut-dialog-fx')") {
		t.Errorf("record-dialog.js's open ease must skip via UT.motionOff()")
	}
}
