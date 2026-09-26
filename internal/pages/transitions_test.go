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
	"regexp"
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

func readAppJS(t *testing.T) string {
	t.Helper()
	chdirRoot(t)
	b, err := os.ReadFile("web/public/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	return string(b)
}

func readRecordDialogJS(t *testing.T) string {
	t.Helper()
	chdirRoot(t)
	b, err := os.ReadFile("web/public/record-dialog.js")
	if err != nil {
		t.Fatalf("read record-dialog.js: %v", err)
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

func TestAppCSSDoesNotOptInStandalonePages(t *testing.T) {
	css := readAppCSS(t)
	// The opt-in lives ONLY in base.html's inline head block: app.css is
	// also loaded by login/setup/self-order/tracking, which have none of
	// the script's guards (reduced-motion skip, watchdog, settled promises)
	// — an opt-in here gave them transitions that surfaced as uncaught
	// "Transition was skipped" errors and, in headless Chromium, never
	// revealed at all (CI auth project, 2026-09-16).
	// Match the RULE (at-rule followed by its block), not the words in a
	// comment explaining why it is absent.
	if regexp.MustCompile(`(?m)^\s*@view-transition\s*\{`).MatchString(css) {
		t.Fatalf("app.css must not contain an @view-transition rule — the opt-in (and its reduced-motion override) belong in base.html's <head> only")
	}
	for _, want := range []string{"--ut-motion-ms", "::view-transition-new(root)", "@keyframes ut-page-zoom-in"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css must still carry the transition animation rules (%q)", want)
		}
	}
}

func TestAppCSSNamesOnlyTheFixedRailAndStatusbar(t *testing.T) {
	css := readAppCSS(t)
	for _, want := range []string{
		"view-transition-name: ut-rail",
		"view-transition-name: ut-statusbar",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q — the nav rail/statusbar must each be a named view-transition group so the fixed furniture doesn't slide with the page", want)
		}
	}
	// The rail/statusbar groups must be pinned to no animation — that's
	// the whole point of naming them (card AC: fixed furniture reads as
	// fixed, never slides/cross-fades).
	if !strings.Contains(css, "::view-transition-old(ut-rail)") || !strings.Contains(css, "::view-transition-new(ut-rail)") {
		t.Errorf("app.css must suppress the default cross-fade on the ut-rail group (::view-transition-old/new(ut-rail) { animation: none })")
	}
	// It is the ROOT pair that zooms — the whole document minus the two
	// named groups — never a named <main> (ADR-0122 §3).
	if !strings.Contains(css, "::view-transition-new(root) { animation-name: ut-page-zoom-in; }") {
		t.Errorf("app.css must animate ::view-transition-new(root) with the page zoom (ADR-0122)")
	}
	// A `view-transition-name` gives its element a stacking context with
	// layout containment, which makes it the containing block for every
	// `position: fixed` descendant. <main> hosts the payment overlay,
	// #hold-modal, #elevation-modal and .item-form-modal —
	// naming it made every one of them unreachable, with no transition
	// running (e2e bugreport-panel.spec.ts caught it; confirmed by toggling
	// the single declaration). Only the rail and statusbar — which host no
	// fixed descendants — may ever carry a name.
	named := regexp.MustCompile(`(?m)^([^/\n{]+)\{[^}]*view-transition-name:\s*ut-`).FindAllStringSubmatch(css, -1)
	allowed := map[string]bool{".nav": true, ".statusbar": true}
	for _, m := range named {
		sel := strings.TrimSpace(m[1])
		if !allowed[sel] {
			t.Errorf("app.css names %q as a view-transition group — only .nav and .statusbar may be named (a named element becomes the containing block for its fixed-position dialogs)", sel)
		}
	}
	if len(named) != 2 {
		t.Errorf("expected exactly 2 named view-transition groups (.nav, .statusbar), found %d: %v", len(named), named)
	}
}

func TestAppCSSHasPageTransitionKeyframes(t *testing.T) {
	css := readAppCSS(t)
	for _, want := range []string{"@keyframes ut-page-zoom-in", "@keyframes ut-page-recede", "@keyframes ut-page-zoom-out", "@keyframes ut-page-return"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}

// TestAppCSSPageMotionIsTheZoomFromTheOrigin pins ADR-0122 §1/§3
// (superseding ADR-0118 §2's push, ut-docs#2939/#2942): 400ms on the iOS
// cubic-bezier(.32,.72,0,1). Forward = the incoming page grows OUT of the
// tapped element (uniform scale --ut-zoom-s + translate to the source centre
// --ut-zoom-x/--ut-zoom-y, starting at opacity .4, never blank) over the
// outgoing one, which recedes as before (scale .92, dims) on the black
// backdrop. Back = the page being left shrinks INTO the stored source and
// fades out on top; the previous page rises from the receded state.
// Transform + opacity only, no clip-path, no side travel (so RTL needs no
// variable at all).
func TestAppCSSPageMotionIsTheZoomFromTheOrigin(t *testing.T) {
	css := readAppCSS(t)
	const origin = "translate(calc(var(--ut-zoom-x) - 50%), calc(var(--ut-zoom-y) - 50%)) scale(var(--ut-zoom-s))"
	for _, want := range []string{
		"--ut-motion-ms: 400ms",
		"--ut-motion-ease: cubic-bezier(.32,.72,0,1)",
		// No origin (and no script) = bottom centre of the viewport.
		"--ut-zoom-x: 50vw; --ut-zoom-y: 100vh; --ut-zoom-s: .3;",
		"animation-duration: var(--ut-motion-ms);",
		"animation-timing-function: var(--ut-motion-ease);",
		"@keyframes ut-page-zoom-in  { from { transform: " + origin + "; opacity: .4; } to { transform: none; opacity: 1; } }",
		"@keyframes ut-page-recede   { from { transform: none; opacity: 1; } to { transform: translateY(3%) scale(.92); opacity: .55; } }",
		"@keyframes ut-page-zoom-out { from { transform: none; opacity: 1; } to { transform: " + origin + "; opacity: 0; } }",
		"@keyframes ut-page-return   { from { transform: translateY(3%) scale(.92); opacity: .55; } to { transform: none; opacity: 1; } }",
		"::view-transition-old(root) { animation-name: ut-page-recede; }",
		// Back: the page being left must paint ABOVE the returning one.
		":root:active-view-transition-type(back)::view-transition-old(root) { animation-name: ut-page-zoom-out; z-index: 1; }",
		`html[data-nav-dir="pop"]::view-transition-old(root) { animation-name: ut-page-zoom-out; z-index: 1; }`,
		":root:active-view-transition-type(back)::view-transition-new(root) { animation-name: ut-page-return; }",
		`html[data-nav-dir="pop"]::view-transition-new(root) { animation-name: ut-page-return; }`,
		// The black backdrop of ADR-0118 §2 is kept (ADR-0122 Supersedes).
		"::view-transition { background: #000; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css page zoom (ADR-0122) missing %q", want)
		}
	}
	// The push is gone, and so is every earlier motion.
	for _, gone := range []string{
		":root { --ut-nav-dir: 1; --ut-motion-ms: 200ms", "@keyframes ut-page-in ", "@keyframes ut-page-out ", "* 4%)",
		"ut-page-slide-in", "ut-page-slide-out",
	} {
		if strings.Contains(css, gone) {
			t.Errorf("app.css still carries a superseded page motion (%q) — ADR-0122 replaced the push with the zoom", gone)
		}
	}
	// Compositor-only: the page keyframes animate transform/opacity and
	// nothing else (no layout property, no filter, no clip-path).
	kf := regexp.MustCompile(`@keyframes (ut-page-[a-z-]+)\s*\{(.*)\}\s*$`)
	prop := regexp.MustCompile(`([a-z-]+)\s*:`)
	n := 0
	for _, line := range strings.Split(css, "\n") {
		m := kf.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n++
		for _, p := range prop.FindAllStringSubmatch(m[2], -1) {
			if p[1] != "transform" && p[1] != "opacity" {
				t.Errorf("@keyframes %s animates %q — page motion is transform + opacity only (ADR-0122 §1)", m[1], p[1])
			}
		}
	}
	if n != 4 {
		t.Errorf("expected exactly the 4 page keyframes (zoom-in, recede, zoom-out, return), found %d", n)
	}
	block := extractMotionRules(css)
	if block == "" {
		t.Fatalf("app.css: could not locate the ADR-0122 root-pair motion rules")
	}
	// Uniform scale + translate only: no side travel, no squash, no clip.
	for _, lit := range []string{"translateX(", "scaleX(", "scaleY(", "clip-path", "left:", "right:"} {
		if strings.Contains(block, lit) {
			t.Errorf("page zoom must be a uniform scale plus translate (ADR-0122 §1), found %q", lit)
		}
	}
}

// extractMotionRules returns the root-pair rules (from the pointer-events
// rule through the last page keyframe).
func extractMotionRules(css string) string {
	a := strings.Index(css, "::view-transition { pointer-events: none; }")
	b := strings.Index(css, "@keyframes ut-page-return")
	if a < 0 || b < a {
		return ""
	}
	e := strings.Index(css[b:], "\n")
	if e < 0 {
		return ""
	}
	return css[a : b+e]
}

// TestAppCSSViewTransitionNeverBlocksInput: the ::view-transition overlay
// covers the real (already swapped) DOM for the whole 400ms; it must not
// take pointer events (ADR-0118). Chromium still retargets to <html> during
// a transition, so base.html also ends the transition on pointerdown —
// both halves are pinned (the behaviour is e2e
// page-snapshot-no-jump-2496.spec.ts).
func TestAppCSSViewTransitionNeverBlocksInput(t *testing.T) {
	if !strings.Contains(readAppCSS(t), "::view-transition { pointer-events: none; }") {
		t.Errorf("app.css must set ::view-transition { pointer-events: none; }")
	}
	html := readBaseHTML(t)
	for _, want := range []string{
		"window.addEventListener('pointerdown', function () {",
		"try { vt.skipTransition(); } catch (e) { /* already done */ }",
		"var el = document.elementFromPoint(e.clientX, e.clientY);",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("base.html must end a running transition on pointerdown and hand the click on (missing %q)", want)
		}
	}
}

// TestAppCSSPageZoomNeedsNoDirectionVariable: the push travelled sideways
// and mirrored under RTL through --ut-nav-dir. The zoom has no side: it
// grows from the tapped element's own (already mirrored) box, so RTL needs
// nothing extra (ADR-0122 §1) and the variable is gone rather than left as
// dead code that a later rule might start trusting.
func TestAppCSSPageZoomNeedsNoDirectionVariable(t *testing.T) {
	if css := readAppCSS(t); strings.Contains(css, "--ut-nav-dir") {
		t.Errorf("app.css still defines/uses --ut-nav-dir — the zoom has no travel direction (ADR-0122 §1)")
	}
}

// TestBaseHTMLRecordsTheMotionOrigin pins ADR-0122 §2: ONE origin recorder,
// in the first script of the document (where UT.motionOff lives), capture
// phase, pointerdown plus keyboard activation, reading the nearest
// interactive ancestor; UT.takeOrigin() hands it out once; it expires after
// 5 s; an off-screen or zero-size box is no origin; nothing throws.
func TestBaseHTMLRecordsTheMotionOrigin(t *testing.T) {
	html := readBaseHTML(t)
	first := strings.Index(html, "<script>")
	firstEnd := strings.Index(html[first:], "</script>")
	if first < 0 || firstEnd < 0 {
		t.Fatalf("base.html: first inline <script> not found")
	}
	script := html[first : first+firstEnd]
	if !strings.Contains(script, "UT.motionOff = function () {") {
		t.Fatalf("the origin recorder must live in the same first script as UT.motionOff")
	}
	for _, want := range []string{
		"var ORIGIN_SEL = 'a, button, [hx-get], [hx-post], [role=treeitem], [data-zoom-origin]';",
		"window.addEventListener('pointerdown', function (e) { recordOrigin(e, e.target); }, true);",
		"window.addEventListener('keydown', function (e) {",
		"if (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar') return;",
		"UT.motionOrigin = { rect: r, el: el, at: Date.now() };",
		"UT.takeOrigin = function () {",
		"UT.motionOrigin = null;",
		"Date.now() - o.at > ORIGIN_TTL_MS",
		"var ORIGIN_TTL_MS = 5000;",
		// Off-screen / zero-size = no origin.
		"function onScreen(r) {",
		"r.width > 0 && r.height > 0",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("base.html's first script must carry the ADR-0122 origin recorder (missing %q)", want)
		}
	}
	// Recorder and zoom helpers are defined before the View-Transition
	// feature check returns early, so the shell script (and the tree-pane /
	// popup consumers, #2943/#2944) can always call them.
	check := strings.Index(script, "if (!('navigation' in window) || !window.CSS || !CSS.supports('view-transition-name: x')) return;")
	for _, def := range []string{"UT.takeOrigin = function () {", "UT.zoomTo = function (rect) {", "UT.zoomRemember = function (", "UT.zoomRecall = function ("} {
		i := strings.Index(script, def)
		if i < 0 || check < 0 || i > check {
			t.Errorf("%q must be defined before the feature-check early return", def)
		}
	}
	// The recorder must never throw into the page.
	rec := extractBlock(t, script, "function recordOrigin(e, target) {")
	if !strings.Contains(rec, "try {") || !strings.Contains(rec, "catch (err)") {
		t.Errorf("recordOrigin must be wrapped in try/catch, got %s", rec)
	}
}

// TestBaseHTMLZoomOriginAndBackStore pins ADR-0122 §2/§3's plumbing: the
// three custom properties are written on <html> (layout-neutral) from the
// boosted htmx:beforeTransition and the cross-document pagereveal, and the
// back store is a bounded (20) sessionStorage list of a path and four
// numbers, every access in try/catch.
func TestBaseHTMLZoomOriginAndBackStore(t *testing.T) {
	html := readBaseHTML(t)
	for _, want := range []string{
		"var ZOOM_KEY = 'ut-zoom-back', ZOOM_MAX = 20;",
		"s.setProperty('--ut-zoom-x', x + 'px');",
		"s.setProperty('--ut-zoom-y', y + 'px');",
		"s.setProperty('--ut-zoom-s', String(sc));",
		"Math.min(1, Math.max(0.1,",
		"while (list.length > ZOOM_MAX) list.shift();",
		// A pop only shrinks into the stored box when it lands on its `from`.
		"return (e && e[1] === landing) ? { left: e[2], top: e[3], width: e[4], height: e[5] } : null;",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("base.html zoom origin/back store (ADR-0122) missing %q", want)
		}
	}
	for _, fn := range []string{"function zoomLoad() {", "function zoomSave(list) {"} {
		body := extractBlock(t, html, fn)
		if !strings.Contains(body, "try {") || !strings.Contains(body, "sessionStorage") {
			t.Errorf("%s must touch sessionStorage only inside try/catch, got %s", fn, body)
		}
	}
	// Boosted path: set in beforeTransition, after the motion-off cancel.
	bt := extractBlock(t, html, "document.addEventListener('htmx:beforeTransition', function (e) {")
	off := strings.Index(bt, "if (UT.motionOff() || !document.startViewTransition) { e.preventDefault(); return; }")
	zoom := strings.Index(bt, "UT.zoomTo(")
	if off < 0 || zoom < 0 || zoom < off {
		t.Errorf("htmx:beforeTransition must cancel under motionOff first, then set the zoom origin (UT.zoomTo); got %s", bt)
	}
	if !strings.Contains(bt, "UT.zoomRemember(") || !strings.Contains(bt, "UT.zoomRecall(") {
		t.Errorf("htmx:beforeTransition must store the forward origin and recall it on a pop")
	}
	// ADR-0122 §2: a redirect is "no origin" -- the boosted path applies the
	// same rule as the cross-document pageswap (#2942 review).
	if !strings.Contains(bt, "redirected = !!pi.requestPath && norm(pi.requestPath) !== norm(to);") ||
		!strings.Contains(bt, "if (!redirected && o && o.el") {
		t.Errorf("htmx:beforeTransition must drop the press origin when the response was redirected")
	}
	// Typing in a field is not an activation of a source view.
	if !strings.Contains(html, "/^(INPUT|TEXTAREA|SELECT)$/.test(tg.tagName || '')") {
		t.Errorf("the keydown origin recorder must skip Enter/Space typed into a field")
	}
	// Cross-document path: pageswap stores, pagereveal reads and sets.
	ps := extractBlock(t, html, "window.addEventListener('pageswap', function (e) {")
	if !strings.Contains(ps, "UT.zoomRemember(") {
		t.Errorf("pageswap must store the forward origin for the incoming document")
	}
	pr := extractBlock(t, html, "window.addEventListener('pagereveal', function (e) {")
	if !strings.Contains(pr, "UT.zoomRecall(") || !strings.Contains(pr, "UT.zoomTo(") {
		t.Errorf("pagereveal must read the stored origin and set the zoom properties")
	}
	// Never before the snapshot in beforeSwap's body (ADR-0118 §1 guard is
	// TestPersistentShell_BeforeSwapDefersTheShellSync); the zoom lives in
	// beforeTransition only.
	bs := extractBlock(t, html, "document.addEventListener('htmx:beforeSwap', function (e) {")
	if strings.Contains(bs, "zoomTo") || strings.Contains(bs, "--ut-zoom") {
		t.Errorf("htmx:beforeSwap must not set the zoom origin; that is htmx:beforeTransition's job")
	}
}

func TestAppCSSReducedMotionBlockCoversEverything(t *testing.T) {
	css := readAppCSS(t)
	block := extractBlock(t, css, "@media (prefers-reduced-motion: reduce)")

	if !strings.Contains(block, "animation: none !important") {
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
		":root:active-view-transition-type(back)::view-transition-new(root)",
		`html[data-nav-dir="pop"]::view-transition-new(root)`,
	} {
		if !strings.Contains(css, good) {
			t.Errorf("app.css must contain %q (the pop direction selector)", good)
		}
	}
	// And the fixed furniture must not be blended additively: with
	// `animation: none` on both images, the UA's plus-lighter blend adds two
	// fully-opaque snapshots and the background flashes brighter for the
	// whole transition. Same for the root pair (ADR-0118): an opaque page
	// sliding over a dimmed one would flash the overlap brighter.
	fixed := extractBlock(t, css, "::view-transition-old(ut-statusbar), ::view-transition-new(ut-statusbar)")
	if !strings.Contains(fixed, "mix-blend-mode: normal") {
		t.Errorf("the rail/statusbar/root old+new images need mix-blend-mode: normal alongside animation: none; got %q", fixed)
	}
	root := extractBlock(t, css, "::view-transition-old(root), ::view-transition-new(root)")
	if !strings.Contains(root, "mix-blend-mode: normal") {
		t.Errorf("the root old+new images need mix-blend-mode: normal (ADR-0118); got %q", root)
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

// TestBaseHTMLPageRevealHasASkipWatchdog: the motion is 400ms (ADR-0118); a
// transition still running long after that is jank or a stuck engine, and
// while it runs the live page takes no input. Both transition paths
// (pagereveal and the boosted same-document swap) must hand the transition
// to the one shared watchdog, which skips it 1000ms after the MOTION starts
// and clears on finish.
//
// ut-docs#2496: the post-ready timer must be armed on vt.ready, never at
// creation. A same-document transition is created before htmx swaps the
// page in; on the pilot tablet that swap ate most of a creation-time 600ms
// budget and the slide was cut short or skipped ("not like Apple anymore").
// A 2s backstop from creation covers an engine whose ready never settles.
func TestBaseHTMLPageRevealHasASkipWatchdog(t *testing.T) {
	html := readBaseHTML(t)
	for _, want := range []string{
		"UT.vtWatchdog = function (vt) {",
		"var backstop = setTimeout(skip, 2000);",
		"vt.ready.then(function () { clearTimeout(backstop); guard = setTimeout(skip, 1000); }, clear);",
		"vt.finished.then(clear, clear);",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("base.html's shared transition watchdog must contain %q", want)
		}
	}
	// Both call sites: the pagereveal listener and the startViewTransition
	// wrapper the boosted swap goes through.
	if n := strings.Count(html, "UT.vtWatchdog(vt);"); n != 2 {
		t.Fatalf("UT.vtWatchdog(vt) must be called from both the pagereveal listener and the startViewTransition wrapper, found %d call(s)", n)
	}
	// The helper must be defined BEFORE the pagereveal script's feature-check
	// early return, or an engine without the Navigation API would leave the
	// shell's startViewTransition wrapper calling an undefined function.
	def := strings.Index(html, "UT.vtWatchdog = function (vt) {")
	check := strings.Index(html, "if (!('navigation' in window) || !window.CSS || !CSS.supports('view-transition-name: x')) return;")
	if def < 0 || check < 0 || def > check {
		t.Fatalf("UT.vtWatchdog must be defined before the pagereveal script's feature-check return")
	}
	// The regression shape itself: a skip timer armed right where the
	// transition is created.
	if strings.Contains(html, "var guard = setTimeout(function () { if (vt.skipTransition) vt.skipTransition(); }, 600);") {
		t.Fatalf("a 600ms skip timer armed at transition creation is back (ut-docs#2496); arm it on vt.ready via UT.vtWatchdog")
	}
}

// TestBaseHTMLSkipsTransitionsUnderReducedMotionInJS: belt and braces
// for the CSS opt-out (`@media (prefers-reduced-motion: reduce) {
// @view-transition { navigation: none } }`) — a transition that starts
// but never reveals leaves the page dead to input, so the script must
// also skip on pageswap (outgoing) and pagereveal (incoming) when the
// media query matches.
func TestBaseHTMLSkipsTransitionsUnderReducedMotionInJS(t *testing.T) {
	html := readBaseHTML(t)
	for _, want := range []string{
		"window.addEventListener('pageswap'",
		// ADR-0119: UT.motionOff() is reduced motion OR the Light effects
		// level — reduced motion stays the floor inside it (see
		// TestBaseHTMLMotionOffPredicate in effects_level_guard_test.go).
		"if (UT.motionOff()) e.viewTransition.skipTransition();",
		"if (UT.motionOff()) { if (vt.skipTransition) vt.skipTransition(); return; }",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("base.html must skip view transitions in JS under prefers-reduced-motion; missing %q", want)
		}
	}
}

// TestBaseHTMLQuietsSkippedTransitionPromises: skipTransition() rejects
// the transition's ready/updateCallbackDone/finished promises with
// AbortError ("Transition was skipped") — by spec — and an unhandled
// rejection is a console error on every skip (CI's auth project caught
// it through watchConsole). The script must settle all three.
func TestBaseHTMLQuietsSkippedTransitionPromises(t *testing.T) {
	html := readBaseHTML(t)
	if !strings.Contains(html, "['ready', 'updateCallbackDone', 'finished'].forEach(function (k) {") ||
		!strings.Contains(html, "if (vt[k] && vt[k].catch) vt[k].catch(function () {});") {
		t.Fatalf("base.html must attach no-op catch handlers to a view transition's ready/updateCallbackDone/finished before skipping it")
	}
	if strings.Count(html, "quiet(") < 3 {
		t.Fatalf("quiet() must be applied in both the pageswap and the pagereveal handlers")
	}
}

// ut-docs#2338: extend the ADR-0097 motion vocabulary to the /items rail's
// in-panel swap (#items-panel) and to record-dialog.js's dialog open —
// deliberately plain compositor CSS animations, not the View Transition
// API: ::view-transition-new(root) (asserted above) unconditionally carries
// the full page-navigation slide, and neither of these is a page
// navigation — reusing it would slide the whole content area (or the whole
// screen behind a dialog) on every panel click / dialog open.

func TestAppCSSHasPanelSwapEaseAnimation(t *testing.T) {
	css := readAppCSS(t)
	if !strings.Contains(css, ".ut-panel-fx") {
		t.Errorf("app.css missing .ut-panel-fx (the in-panel-swap ease class)")
	}
	if !strings.Contains(css, "@keyframes ut-panel-in") {
		t.Errorf("app.css missing @keyframes ut-panel-in")
	}
	// Opacity-only, deliberately never a transform (independent review,
	// ut-docs#2338): a first cut used `translateX(var(--ut-nav-dir) * ...)`
	// for a page-slide-like feel, which both (a) violated ADR-0097 rule 5
	// ("readable from frame one, never a flash to blank" — it started from
	// opacity 0, not .55 like .ut-swap-fx) and (b) put a non-`none`
	// `transform` on an ancestor of `.record-dialog`/`.item-form-modal`
	// (`position: fixed` descendants living inside the swapped panel),
	// which makes it their containing block — the exact hazard ADR-0097
	// rule 2 already names for `view-transition-name`, just reached via a
	// different CSS property. Pin both corrections here.
	block := extractBlock(t, css, "@keyframes ut-panel-in")
	if strings.Contains(block, "transform") {
		t.Errorf("@keyframes ut-panel-in must never use `transform` — the swapped panel hosts position:fixed dialog descendants, and any non-`none` transform on an ancestor becomes their containing block (ADR-0097 rule 2's hazard), got: %s", block)
	}
	if !strings.Contains(block, "opacity: .55") {
		t.Errorf("@keyframes ut-panel-in must start from opacity: .55, never 0 (ADR-0097 rule 5: readable from frame one, never a flash to blank), got: %s", block)
	}
	if !strings.Contains(block, "opacity: 1") {
		t.Errorf("@keyframes ut-panel-in must end at opacity: 1, got: %s", block)
	}
}

func TestAppCSSHasDialogOpenEaseAnimation(t *testing.T) {
	css := readAppCSS(t)
	if !strings.Contains(css, ".ut-dialog-fx") {
		t.Errorf("app.css missing .ut-dialog-fx (the record-dialog.js open ease class)")
	}
	if !strings.Contains(css, "@keyframes ut-dialog-in") {
		t.Errorf("app.css missing @keyframes ut-dialog-in")
	}
}

// The reduced-motion wildcard block (TestAppCSSReducedMotionBlockCoversEverything
// above) already asserts `*, *::before, *::after { animation-duration: 0s
// !important }`, which covers these two new keyframes automatically — no
// separate CSS assertion needed here, only that app.js/record-dialog.js
// also carry the belt-and-braces JS-side skip, checked below.

func TestAppJSAppliesPanelEaseInsteadOfSwapEaseForItemsPanel(t *testing.T) {
	js := readAppJS(t)
	// Independent review, ut-docs#2338: the original version of this
	// assertion was `strings.Contains(js, "items-panel")`, which passes
	// even with this whole card reverted — the literal "items-panel"
	// already appears elsewhere in app.js (the X-UT-Page-Title allowlist).
	// Assert the actual expression, and all three rail-driven panel ids
	// (#items-panel, #admin-panel, #manual-panel), not just one.
	if !strings.Contains(js, "['items-panel', 'admin-panel', 'manual-panel'].indexOf(t.id) !== -1") {
		t.Fatalf("app.js's swap-ease listener must special-case all three rail-driven panel targets (#items-panel, #admin-panel, #manual-panel)")
	}
	if !strings.Contains(js, "isPanelNav ? 'ut-panel-fx' : 'ut-swap-fx'") {
		t.Fatalf("app.js must apply the 'ut-panel-fx' class for a panel-nav swap, 'ut-swap-fx' otherwise")
	}
	// The animationend cleanup must remove BOTH classes it can ever add —
	// a stale class on an element that never gets a matching animationend
	// (e.g. one class added, the id read wrong) would stick forever.
	if !strings.Contains(js, "'ut-swap-in' || e.animationName === 'ut-panel-in'") {
		t.Fatalf("app.js's animationend cleanup must match both ut-swap-in and ut-panel-in")
	}
	if !strings.Contains(js, "remove('ut-swap-fx', 'ut-panel-fx')") {
		t.Fatalf("app.js's animationend cleanup must remove both ut-swap-fx and ut-panel-fx")
	}
}

func TestRecordDialogJSAppliesOpenEaseAndSkipsUnderReducedMotion(t *testing.T) {
	js := readRecordDialogJS(t)
	if !strings.Contains(js, "prefers-reduced-motion: reduce") {
		t.Fatalf("record-dialog.js must feature-check prefers-reduced-motion before applying the open ease")
	}
	if !strings.Contains(js, "'ut-dialog-fx'") {
		t.Fatalf("record-dialog.js must apply the 'ut-dialog-fx' class on open")
	}
	// The reduced-motion check must gate adding the class (belt-and-braces
	// alongside the CSS wildcard), not just exist somewhere unrelated in
	// the file.
	openIdx := strings.Index(js, "function open(dialog, row, opener)")
	if openIdx < 0 {
		t.Fatalf("open(dialog, row, opener) not found in record-dialog.js")
	}
	closeIdx := strings.Index(js, "function close(dialog)")
	if closeIdx < 0 || closeIdx < openIdx {
		t.Fatalf("close(dialog) not found after open() in record-dialog.js")
	}
	openBody := js[openIdx:closeIdx]
	if !strings.Contains(openBody, "ut-dialog-fx") {
		t.Fatalf("open() must add the ut-dialog-fx class, got body: %s", openBody)
	}
	// ADR-0119: through motionOff() (UT.motionOff: reduced motion OR the
	// Light effects level, falling back to the bare media query).
	if !strings.Contains(openBody, "if (!motionOff()) dialog.classList.add('ut-dialog-fx')") {
		t.Fatalf("open() must consult motionOff() (reduced motion or Light) before adding ut-dialog-fx")
	}
	// Never applied to close(): delaying the native .close() for an exit
	// animation would add latency to Cancel/Save.
	closeBody := js[closeIdx:]
	closeEnd := strings.Index(closeBody, "\n  }\n")
	if closeEnd > 0 {
		closeBody = closeBody[:closeEnd]
	}
	if strings.Contains(closeBody, "ut-dialog-fx") {
		t.Fatalf("close() must NOT add/remove ut-dialog-fx — no exit animation, zero added latency on Cancel/Save, got body: %s", closeBody)
	}
}
