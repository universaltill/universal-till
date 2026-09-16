package pages

// ut-docs#2223: page/menu transitions "like an app" — cross-document CSS
// View Transitions (push/pop direction), named groups so the fixed nav
// rail/statusbar don't animate, and a reduced-motion kill-switch that
// covers both the new transition rules AND every pre-existing
// `transition:` declaration in app.css (e.g. .menu-tile).
//
// These are cheap static guards over the actual shipped source, same
// pattern as TestPairingNoticeMount_KeepsPollingAndUsesADistinctID
// (asserts on base.html's real text) and
// TestLoginAndSetupUseFluidUIScaleCSSVariable (asserts a CSS-variable
// mechanism landed, not just "some CSS changed somewhere") — a browser is
// what actually proves the animation LOOKS right; e2e/tests/
// page-transitions-2223.spec.ts covers that half.

import (
	"os"
	"strings"
	"testing"
)

func readAppCSS(t *testing.T) string {
	t.Helper()
	chdirRoot(t)
	b, err := os.ReadFile("web/public/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	return string(b)
}

func readBaseHTML(t *testing.T) string {
	t.Helper()
	chdirRoot(t)
	b, err := os.ReadFile("web/ui/layouts/base.html")
	if err != nil {
		t.Fatalf("read base.html: %v", err)
	}
	return string(b)
}

// extractBlock returns the text between the first "{" after `marker` and
// its matching "}" (naive brace counting — good enough for a single-level
// CSS at-rule/media block with no nested string literals containing braces,
// which is everything in this file).
func extractBlock(t *testing.T, src, marker string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("marker %q not found", marker)
	}
	open := strings.Index(src[i:], "{")
	if open < 0 {
		t.Fatalf("no '{' after marker %q", marker)
	}
	start := i + open
	depth := 0
	for p := start; p < len(src); p++ {
		switch src[p] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : p+1]
			}
		}
	}
	t.Fatalf("unbalanced braces scanning block for marker %q", marker)
	return ""
}

func TestAppCSSDeclaresCrossDocumentViewTransition(t *testing.T) {
	css := readAppCSS(t)
	if !strings.Contains(css, "@view-transition { navigation: auto; }") &&
		!strings.Contains(css, "@view-transition {\n  navigation: auto;\n}") &&
		!strings.Contains(css, "@view-transition{navigation:auto;}") {
		// Be liberal about exact whitespace but strict about the two
		// tokens actually being adjacent in one rule.
		block := extractBlock(t, css, "@view-transition")
		if !strings.Contains(block, "navigation: auto") && !strings.Contains(block, "navigation:auto") {
			t.Fatalf("expected `@view-transition { navigation: auto; }` in app.css (cross-document navigation opt-in), got block: %s", block)
		}
	}
}

func TestAppCSSNamesTheFixedRailAndPageAsViewTransitionGroups(t *testing.T) {
	css := readAppCSS(t)
	for _, want := range []string{
		"view-transition-name: ut-rail",
		"view-transition-name: ut-statusbar",
		"view-transition-name: ut-page",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q — the nav rail/statusbar/page must each be a named view-transition group so the fixed furniture doesn't cross-fade with the page", want)
		}
	}
	// The rail/statusbar groups must be pinned to no animation — that's
	// the whole point of naming them (card AC: fixed furniture reads as
	// fixed, never slides/cross-fades).
	if !strings.Contains(css, "::view-transition-old(ut-rail)") || !strings.Contains(css, "::view-transition-new(ut-rail)") {
		t.Errorf("app.css must suppress the default cross-fade on the ut-rail group (::view-transition-old/new(ut-rail) { animation: none })")
	}
}

func TestAppCSSHasPageTransitionKeyframes(t *testing.T) {
	css := readAppCSS(t)
	for _, want := range []string{"@keyframes ut-page-in", "@keyframes ut-page-out"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}

func TestAppCSSNavDirectionVariableMirrorsUnderRTL(t *testing.T) {
	css := readAppCSS(t)
	if !strings.Contains(css, "--ut-nav-dir: 1") {
		t.Errorf("app.css must define a default --ut-nav-dir: 1 on :root")
	}
	// Must be scoped under an RTL selector, not just present anywhere.
	// app.css already has PRE-EXISTING selectors like `html[dir="rtl"]
	// .foo {...}` elsewhere (those contain the substring `[dir="rtl"]`
	// too), so anchor on the variable itself and look at what selector
	// immediately precedes its declaration block, rather than the first
	// `[dir="rtl"]` match anywhere in the file.
	varIdx := strings.Index(css, "--ut-nav-dir: -1")
	if varIdx < 0 {
		t.Fatalf(`app.css missing "--ut-nav-dir: -1"`)
	}
	openBrace := strings.LastIndex(css[:varIdx], "{")
	if openBrace < 0 {
		t.Fatalf("could not find the rule's opening brace before --ut-nav-dir: -1")
	}
	selector := css[strings.LastIndex(css[:openBrace], "}")+1 : openBrace]
	if !strings.Contains(selector, `[dir="rtl"]`) {
		t.Errorf(`expected --ut-nav-dir: -1 to be declared under a selector containing [dir="rtl"], got selector: %q`, selector)
	}
}

func TestAppCSSReducedMotionBlockCoversEverything(t *testing.T) {
	css := readAppCSS(t)
	block := extractBlock(t, css, "@media (prefers-reduced-motion: reduce)")

	if !strings.Contains(block, "navigation: none") {
		t.Errorf("reduced-motion block must set `@view-transition { navigation: none; }` (or equivalent), got: %s", block)
	}
	if !strings.Contains(block, "animation-duration: 0s !important") {
		t.Errorf("reduced-motion block must force animation-duration: 0s !important, got: %s", block)
	}
	if !strings.Contains(block, "transition-duration: 0s !important") {
		t.Errorf("reduced-motion block must force transition-duration: 0s !important — this is the guard that also kills every PRE-EXISTING `transition:` rule (.menu-tile, .tab, etc.), not just the new view-transition ones, got: %s", block)
	}
	// The wildcard selector is what makes this guard resilient to someone
	// deleting it without noticing it covered .menu-tile/.tab too — assert
	// it's a real universal selector, not scoped to a handful of classes.
	if !strings.Contains(block, "*, *::before, *::after") && !strings.Contains(block, "*,\n  *::before") && !strings.Contains(block, "*, *::before, *::after {") {
		if !strings.Contains(block, "*::before") || !strings.Contains(block, "*::after") {
			t.Errorf("reduced-motion block must include a `*, *::before, *::after` wildcard rule so it also covers every pre-existing transition/animation in app.css (e.g. .menu-tile, .tab), got: %s", block)
		}
	}
}

// Regression guard for the specific pre-existing rules the card named:
// .menu-tile's transition and .tab/.tab-active's transition must still
// exist (untouched — the design says don't touch their rules, only add
// the wildcard reduced-motion override), so this also proves the guard
// above is covering something real, not a hypothetical.
func TestAppCSSPreExistingMotionRulesAreUntouchedAndCoveredByWildcard(t *testing.T) {
	css := readAppCSS(t)
	if !strings.Contains(css, ".menu-tile {") {
		t.Fatalf("expected .menu-tile rule to still exist in app.css")
	}
	tab := extractBlock(t, css, ".tab, .tab-active {")
	if !strings.Contains(tab, "transition:") {
		t.Fatalf("expected the pre-existing .tab, .tab-active rule to still carry a transition in app.css, got %q", tab)
	}
}

func TestAppCSSHasSwapEaseAnimation(t *testing.T) {
	css := readAppCSS(t)
	if !strings.Contains(css, ".ut-swap-fx") {
		t.Errorf("app.css missing .ut-swap-fx (the in-page htmx-swap ease class)")
	}
	if !strings.Contains(css, "@keyframes ut-swap-in") {
		t.Errorf("app.css missing @keyframes ut-swap-in")
	}
}

func TestBaseHTMLHasPageRevealDirectionScript(t *testing.T) {
	html := readBaseHTML(t)
	if !strings.Contains(html, "pagereveal") {
		t.Fatalf("base.html must register a 'pagereveal' listener inline in <head> to compute push/pop direction before first paint")
	}
	if !strings.Contains(html, "data-nav-dir") {
		t.Fatalf("base.html's pagereveal script must set data-nav-dir on <html> (testable direction hook, and a CSS fallback selector if `types` is unsupported)")
	}
	// Must be inline (not a defer src="...") and must appear before app.js's
	// own <script defer src=".../app.js"> tag, since app.js is deferred and
	// may run after the first render opportunity/pagereveal fires.
	pagerevealIdx := strings.Index(html, "addEventListener('pagereveal'")
	appJSIdx := strings.Index(html, `src="/public/app.js`)
	if pagerevealIdx < 0 || appJSIdx < 0 || pagerevealIdx > appJSIdx {
		t.Fatalf("the pagereveal script must appear in <head> BEFORE app.js's <script> tag")
	}
	// Confirm it's inline, not another external file: there must be an
	// unbroken <script>...pagereveal...</script> with no src attribute
	// wrapping the pagereveal text.
	tagStart := strings.LastIndex(html[:pagerevealIdx], "<script>")
	if tagStart < 0 {
		t.Fatalf("expected the pagereveal listener inside a bare inline <script> tag (no src=), not an external file")
	}
}

func TestBaseHTMLPageRevealScriptIsFeatureChecked(t *testing.T) {
	html := readBaseHTML(t)
	// The whole IIFE must feature-check before touching `navigation`/CSS.
	revealIdx := strings.Index(html, "addEventListener('pagereveal'")
	if revealIdx < 0 {
		t.Fatalf("pagereveal not found in base.html")
	}
	i := strings.LastIndex(html[:revealIdx], "<script>")
	if i < 0 {
		t.Fatalf("could not find the opening <script> tag before pagereveal")
	}
	end := strings.Index(html[i:], "</script>")
	if end < 0 {
		t.Fatalf("could not find the closing </script> tag after pagereveal")
	}
	script := html[i : i+end]
	if !strings.Contains(script, "'navigation' in window") {
		t.Errorf("pagereveal script must feature-check `'navigation' in window` before use (progressive enhancement — engines without support must fall back to an instant swap), got: %s", script)
	}
	if !strings.Contains(script, "CSS.supports") {
		t.Errorf("pagereveal script must feature-check CSS.supports('view-transition-name: x'), got: %s", script)
	}
}

// TestBaseHTMLCarriesTheViewTransitionOptInInline pins the fix for a bug
// found on the pilot tablet, not in a browser on a laptop: Chrome (Android
// 153 — and by construction any Chromium) evaluates the INCOMING document's
// `@view-transition` opt-in as soon as its <head> is parsed. If the opt-in
// only lives in the render-blocking app.css (244K, competing with nine
// deferred scripts for the tablet's Wi-Fi), that read can land before the
// stylesheet has arrived, and Chrome aborts the navigation's transition
// with "ViewTransition opt-in disabled" — silently, and racily (3 of 3
// runs with the opt-in in app.css only, 0 of 3 with the inline block). The
// same page as the OUTGOING document was never affected, which is exactly
// why it looked like "till pages cannot be transitioned INTO" until
// Chrome's own console named the state. So the opt-in (and the
// reduced-motion override, for the same timing reason) must also be an
// inline <style> in <head>, ahead of the app.css <link>.
func TestBaseHTMLCarriesTheViewTransitionOptInInline(t *testing.T) {
	html := readBaseHTML(t)
	optIn := strings.Index(html, "<style>@view-transition{navigation:auto}")
	if optIn < 0 {
		t.Fatalf("base.html <head> must carry an inline <style>@view-transition{navigation:auto} ... block — the app.css copy alone is read too late by Chrome on the pilot tablet (ut-docs#2223)")
	}
	styleEnd := strings.Index(html[optIn:], "</style>")
	if styleEnd < 0 {
		t.Fatalf("unterminated inline <style> after the @view-transition opt-in")
	}
	block := html[optIn : optIn+styleEnd]
	if !strings.Contains(block, "prefers-reduced-motion") || !strings.Contains(block, "navigation:none") {
		t.Errorf("the inline opt-in must carry the prefers-reduced-motion override (navigation:none) next to it, for the same timing reason; got %q", block)
	}
	cssLink := strings.Index(html, `href="/public/app.css`)
	if cssLink < 0 || optIn > cssLink {
		t.Fatalf("the inline @view-transition opt-in must precede the app.css <link> in <head>")
	}
}

// TestAppCSSDirectionSelectorsHaveNoDescendantCombinator: every
// ::view-transition-* pseudo originates on <html> itself, so a selector
// like `:root:active-view-transition-type(back) ::view-transition-new(x)`
// (with a space) asks for a pseudo on a DESCENDANT of :root and can never
// match — the pop direction would silently never apply. Caught in review
// of the first cut of this change; pinned here so it cannot come back.
func TestAppCSSDirectionSelectorsHaveNoDescendantCombinator(t *testing.T) {
	css := readAppCSS(t)
	for _, bad := range []string{
		":active-view-transition-type(back) ::view-transition",
		`[data-nav-dir="pop"] ::view-transition`,
	} {
		if strings.Contains(css, bad) {
			t.Errorf("app.css contains %q — a descendant combinator before a ::view-transition-* pseudo never matches; write it as one compound selector", bad)
		}
	}
	for _, good := range []string{
		":root:active-view-transition-type(back)::view-transition-new(ut-page)",
		`html[data-nav-dir="pop"]::view-transition-new(ut-page)`,
	} {
		if !strings.Contains(css, good) {
			t.Errorf("app.css must contain %q (the pop direction selector)", good)
		}
	}
	// And the fixed furniture must not be blended additively: with
	// `animation: none` on both images, the UA's plus-lighter blend adds two
	// fully-opaque snapshots and the background flashes brighter for 200ms.
	fixed := extractBlock(t, css, "::view-transition-old(ut-statusbar), ::view-transition-new(ut-statusbar)")
	if !strings.Contains(fixed, "mix-blend-mode: normal") {
		t.Errorf("the rail/statusbar/root old+new images need mix-blend-mode: normal alongside animation: none; got %q", fixed)
	}
}

// TestAppCSSReducedMotionKeepsSpinnersTurning: the reduced-motion wildcard
// zeroes every animation-duration, which would freeze the htmx progress
// spinners (#refresh-indicator, .animate-spin) mid-frame and make a
// request in flight read as "hung". Essential progress motion is exempt
// (WCAG 2.3.3) — the block must re-enable exactly those. (Independent
// review finding, 2026-09-16.)
func TestAppCSSReducedMotionKeepsSpinnersTurning(t *testing.T) {
	css := readAppCSS(t)
	block := extractBlock(t, css, "@media (prefers-reduced-motion: reduce)")
	if !strings.Contains(block, "#refresh-indicator, .animate-spin { animation-duration: 1s !important; }") {
		t.Errorf("reduced-motion block must exempt the progress spinners; got %q", block)
	}
}

// TestBaseHTMLPageRevealSkipsReloads: a reload has from === entry, so the
// depth heuristic reads it as a push and the SAME page would slide in from
// the side on pull-to-refresh / location.reload() / HX-Refresh. The script
// must skip the transition for navigationType === 'reload'. (Independent
// review finding, 2026-09-16.)
func TestBaseHTMLPageRevealSkipsReloads(t *testing.T) {
	html := readBaseHTML(t)
	if !strings.Contains(html, "act.navigationType === 'reload'") || !strings.Contains(html, "skipTransition") {
		t.Fatalf("base.html's pagereveal script must skipTransition() on a reload navigation")
	}
}
