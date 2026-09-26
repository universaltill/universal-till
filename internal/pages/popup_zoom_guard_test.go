package pages

// ut-docs#2944 / ADR-0122 §5, §6, §8: every popup -- each <dialog>, sheet
// and picker -- grows from the tapped element and shrinks back into it on
// close through ONE shared mechanism in base.html's first script. These are
// static guards over the shipped source (the same pattern as
// transitions_test.go); e2e/tests/popup-zoom-2944.spec.ts proves the
// behaviour in a real Chromium.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// popupBlock returns base.html's shared popup-motion code: from the
// HTMLDialogElement wrapper to the end of the popup section marker.
func popupBlock(t *testing.T, html string) string {
	t.Helper()
	i := strings.Index(html, "// ADR-0122 §5 (ut-docs#2944): the ONE popup motion")
	if i < 0 {
		t.Fatalf("base.html has no shared popup-motion section (ADR-0122 §5)")
	}
	j := strings.Index(html[i:], "// end ADR-0122 §5 popup motion")
	if j < 0 {
		t.Fatalf("base.html's popup-motion section has no end marker")
	}
	return html[i : i+j]
}

// webFiles walks web/ui and web/public (never vendor/) and returns every
// .html/.js/.css file's text keyed by its repo-relative path.
func webFiles(t *testing.T) map[string]string {
	t.Helper()
	chdirRoot(t)
	out := map[string]string{}
	for _, root := range []string{"web/ui", "web/public"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(p) {
			case ".html", ".js", ".css":
			default:
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(p)] = string(b)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

// Consequences: wrapping HTMLDialogElement.prototype is a global patch, so
// it is installed exactly once, in base.html, and nowhere else in web/.
func TestDialogPrototypeIsWrappedOnceOnlyInBaseHTML(t *testing.T) {
	files := webFiles(t)
	for p, src := range files {
		n := strings.Count(src, "HTMLDialogElement.prototype[m] = ")
		switch p {
		case "web/ui/layouts/base.html":
			if n != 1 {
				t.Errorf("base.html must assign the showModal/show wrapper exactly once, found %d", n)
			}
		default:
			if strings.Contains(src, "HTMLDialogElement.prototype") {
				t.Errorf("%s patches HTMLDialogElement.prototype -- the ONE wrapper lives in base.html (ADR-0122 §5)", p)
			}
		}
	}
}

// ADR-0122 §5/§8: no partial or script animates a dialog itself -- the only
// Element.animate() callers are base.html's shared popup code and app.js's
// tree-pane zoom (ADR-0122 §4). A new .animate( anywhere else must justify
// itself here.
func TestOnlyTheSharedZoomsCallAnimate(t *testing.T) {
	allowed := map[string]int{
		"web/ui/layouts/base.html": 3, // popup open, its ::backdrop, the closing shrink
		"web/public/app.js":        1, // zoomPane (tree panes, #2943)
	}
	re := regexp.MustCompile(`\.animate\(`)
	for p, src := range webFiles(t) {
		n := len(re.FindAllStringIndex(stripCodeComments(src), -1))
		if n != allowed[p] {
			t.Errorf("%s calls .animate( %d times, want %d -- per-popup motion must go through base.html's shared zoom (ADR-0122 §5)", p, n, allowed[p])
		}
	}
	// base.html's three are all inside the shared popup section.
	html := readBaseHTML(t)
	if n := strings.Count(stripCodeComments(popupBlock(t, html)), ".animate("); n != 3 {
		t.Errorf("base.html's .animate( calls must all live in the shared popup section, found %d there", n)
	}
}

// ADR-0122 §5: record-dialog.js's own open ease (#2010/#2338) is replaced by
// the shared zoom, not layered on it.
func TestPerDialogOpenEaseIsGone(t *testing.T) {
	for p, src := range webFiles(t) {
		for _, gone := range []string{"ut-dialog-fx", "ut-dialog-in"} {
			if strings.Contains(src, gone) {
				t.Errorf("%s still references %s -- ADR-0122 §5 replaces it with the shared popup zoom", p, gone)
			}
		}
	}
}

func TestBaseHTMLSharedPopupZoom(t *testing.T) {
	html := readBaseHTML(t)
	b := popupBlock(t, html)

	// Opening: the wrapper runs the native method FIRST and returns its
	// value, then starts the motion inside a try (never throws into the
	// caller), and finishes a running pane zoom before native.
	w := b[strings.Index(b, "['showModal', 'show'].forEach"):]
	if e := strings.Index(w, "HTMLDialogElement.prototype[m] = wrapped;"); e > 0 {
		w = w[:e]
	} else {
		t.Fatalf("popup wrapper not found in the shared section")
	}
	nat := strings.Index(w, "var ret = native.apply(this, arguments);")
	fin := strings.Index(w, "UT.finishZooms()")
	open := strings.Index(w, "popupOpen(this")
	if nat < 0 || fin < 0 || open < 0 {
		t.Fatalf("wrapper must finish zooms, run native, then open the motion; got: %s", w)
	}
	if fin > nat || open < nat {
		t.Errorf("wrapper order must be finishZooms -> native -> motion (native runs first and unchanged)")
	}
	if !strings.Contains(w, "return ret;") || strings.Count(w, "try {") < 2 {
		t.Errorf("wrapper must return native's value and guard the motion with try/catch, got: %s", w)
	}

	for _, want := range []string{
		"UT.popupOpened = function",
		"UT.popupClosed = function",
		// Closing: the dialog close EVENT (covers close(), Escape, form
		// method=dialog, requestClose), capture phase on document.
		"document.addEventListener('close', function",
		"UT.takeOrigin()",
		"UT.trackZoom(",
		"UT.motionOff()",
		"ResizeObserver",
		"'data-ut-closing'",
		"'inert'",
		"'aria-hidden'",
		"fill: 'none'",
		"cubic-bezier(.32, .72, 0, 1)",
		"--ut-zoom-small-ms",
		"opacity: 0.4",
		"Math.min(1, Math.max(0.1,",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("shared popup section must contain %q", want)
		}
	}
	if !regexp.MustCompile(`document\.addEventListener\('close', function \(e\) \{[\s\S]*?\}, true\);`).MatchString(b) {
		t.Errorf("the close listener must be capture phase on document (close does not bubble)")
	}
	if strings.Contains(b, "scaleX") || strings.Contains(b, "scaleY") || strings.Contains(b, "clip-path") || strings.Contains(b, "cloneNode") {
		t.Errorf("popup zoom must be one uniform scale on the SAME element: no scaleX/scaleY/clip-path/clone (ADR-0122 §1, §5)")
	}
	// Never a wrapper of .close(): ADR-0097's rule, never delay close().
	if strings.Contains(b, "prototype.close") || strings.Contains(b, "'close'].forEach") {
		t.Errorf("the close path must be the close EVENT, never a wrapper of .close() (ADR-0122 §5)")
	}
	// ADR-0122 §6: both motion entry points bail under Light/reduced motion
	// before any animate() or re-show.
	for _, fn := range []string{"function popupOpen(", "function popupClose("} {
		i := strings.Index(b, fn)
		if i < 0 {
			t.Fatalf("shared popup section has no %s", fn)
		}
		head := b[i:]
		if len(head) > 400 {
			head = head[:400]
		}
		if !strings.Contains(head, "if (UT.motionOff()) return;") {
			t.Errorf("%s must bail on UT.motionOff() first (ADR-0122 §6), got: %s", fn, head)
		}
	}
}

func TestAppCSSClosingPopupRule(t *testing.T) {
	css := readAppCSS(t)
	rule := extractBlock(t, css, "html [data-ut-closing] {")
	for _, want := range []string{
		"display: var(--ut-closing-display, block) !important",
		"position: fixed !important",
		"pointer-events: none !important",
		"margin: 0 !important",
		"var(--ut-closing-t)",
		"var(--ut-closing-is)",
		"var(--ut-closing-w)",
		"var(--ut-closing-h)",
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("[data-ut-closing] rule must contain %q, got: %s", want, rule)
		}
	}
	// Above the rail (100) and the on-screen keyboard (1000).
	m := regexp.MustCompile(`z-index: (\d+) !important`).FindStringSubmatch(rule)
	if z, _ := strconv.Atoi(firstSub(m)); z <= 1000 {
		t.Errorf("[data-ut-closing] must sit above #osk (z-index 1000), got: %v", m)
	}
	// Logical properties only (RTL), never left/right.
	if regexp.MustCompile(`(^|[^-])(left|right)\s*:`).MatchString(rule) {
		t.Errorf("[data-ut-closing] must use logical properties, not left/right: %s", rule)
	}
	// ADR-0122 §6 CSS backstop: Light and reduced motion never paint a
	// closing popup.
	if !strings.Contains(css, "html.fx-light [data-ut-closing] { display: none !important; }") {
		t.Errorf("html.fx-light must hide [data-ut-closing] (ADR-0122 §6 backstop)")
	}
	rm := extractBlock(t, css, "@media (prefers-reduced-motion: reduce) {")
	if !strings.Contains(rm, "[data-ut-closing] { display: none !important; }") {
		t.Errorf("the reduced-motion block must hide [data-ut-closing] (ADR-0122 §6 backstop)")
	}
}

// ADR-0122 §5: a popup that is not a <dialog> calls the same two functions.
// The bug-report panel is the product's one non-dialog popup.
func TestBugreportPanelUsesSharedPopupMotion(t *testing.T) {
	chdirRoot(t)
	b, err := os.ReadFile("web/ui/partials/bugreport_panel.html")
	if err != nil {
		t.Fatalf("read bugreport_panel.html: %v", err)
	}
	src := string(b)
	for _, want := range []string{"UT.popupOpened(panel)", "UT.popupClosed(panel)"} {
		if !strings.Contains(src, want) {
			t.Errorf("bugreport_panel.html must call %s (ADR-0122 §5)", want)
		}
	}
}

var (
	blockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// A // comment starts a line or follows whitespace or punctuation --
	// never a URL's "https://" (preceded by ':').
	lineCommentRE = regexp.MustCompile(`(?m)(^|[\s;{}(),])//.*$`)
)

// stripCodeComments drops /* */ (also {{/* */}}) and // comment text from
// JS/HTML/CSS source, so a guard counts code only.
func stripCodeComments(src string) string {
	src = blockCommentRE.ReplaceAllString(src, "")
	return lineCommentRE.ReplaceAllString(src, "$1")
}

func firstSub(m []string) string {
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ut-docs#2944 review (major): the dialog `close` event is a queued task, so
// a close site that empties the dialog right after .close() hands the shared
// shrink an already-empty element -- a blank card shrinks into the tile. No
// page under base.html's shared motion may mutate a dialog at its close
// site. (The self-order kiosk partials are not under base.html; a backlog
// card covers them.)
func TestNoDialogIsEmptiedAtItsCloseSite(t *testing.T) {
	re := regexp.MustCompile(`\.close\(\)[;,]?\s*[A-Za-z_.$()']*\.innerHTML\s*=`)
	for p, src := range webFiles(t) {
		if strings.HasPrefix(filepath.Base(p), "self_order_") {
			continue
		}
		if loc := re.FindStringIndex(src); loc != nil {
			t.Errorf("%s empties a dialog right after .close() (%q) -- the close event is queued, so the shared shrink would show a blank frame (ADR-0122 §5)", p, src[loc[0]:loc[1]])
		}
	}
}

// ut-docs#2944 review: popupClose's backstops and the closing target rule.
func TestPopupCloseGuardsAndCloseTarget(t *testing.T) {
	b := popupBlock(t, readBaseHTML(t))
	i := strings.Index(b, "function popupClose(")
	if i < 0 {
		t.Fatalf("no popupClose in the shared section")
	}
	pc := b[i:]
	if e := strings.Index(pc, "UT.popupOpened = function"); e > 0 {
		pc = pc[:e]
	}
	guard := strings.Index(pc, "if (!box || !el.isConnected || el.open === true) return;")
	empty := strings.Index(pc, "if (!el.childElementCount) return;")
	show := strings.Index(pc, "el.setAttribute('data-ut-closing', '')")
	if guard < 0 || empty < 0 || show < 0 || !(guard < empty && empty < show) {
		t.Errorf("popupClose must return on an empty element (no blank frame) right after its early-return guard, before re-showing it")
	}
	// A tap during a closing shrink hit-tests nothing moving (the element is
	// inert, pointer-events:none), so it must not arm the §7 re-dispatch.
	if !strings.Contains(pc, "UT.trackZoom(c.anim, { passive: true });") {
		t.Errorf("the closing shrink must be tracked as passive (no §7 click re-dispatch)")
	}
	// closeTarget: the live rect only when the source is connected,
	// rendered and on screen; otherwise the bottom centre -- never the
	// stale rect recorded at the tap (ADR-0122 §2/§5).
	j := strings.Index(b, "function closeTarget(")
	if j < 0 {
		t.Fatalf("no closeTarget in the shared section")
	}
	ct := b[j:]
	if e := strings.Index(ct, "function popupOpen("); e > 0 {
		ct = ct[:e]
	}
	if !strings.Contains(ct, "getClientRects().length") || strings.Contains(ct, "o.rect") {
		t.Errorf("closeTarget must use the live rect only when rendered and on screen, never the stale o.rect: %s", ct)
	}
}

// ADR-0122 §7: UT.finishZooms() reports only NON-passive zooms, so a
// passive one (the closing shrink) never arms the click re-dispatch.
func TestFinishZoomsIgnoresPassiveForReDispatch(t *testing.T) {
	html := readBaseHTML(t)
	i := strings.Index(html, "UT.trackZoom = function (anim, opts)")
	if i < 0 {
		t.Fatalf("UT.trackZoom must take (anim, opts) with opts.passive")
	}
	j := strings.Index(html[i:], "var activeVT")
	if j < 0 {
		t.Fatalf("trackZoom/finishZooms block not found")
	}
	blk := html[i : i+j]
	for _, want := range []string{"opts.passive", "passive"} {
		if !strings.Contains(blk, want) {
			t.Errorf("trackZoom/finishZooms must handle %q", want)
		}
	}
}

// The .animate( counter must not be fooled by comment text (a comment that
// mentions .animate( must not fail CI), while code stays counted exactly.
func TestStripCodeComments(t *testing.T) {
	src := "a.animate(x); // was b.animate(y)\n" +
		"/* old: c.animate(z) */ d.animate(w);\n" +
		"{{/* e.animate( */}}\n" +
		"<a href=\"https://x.test/\">f</a> g.animate(v);\n" +
		"    // h.animate(\n"
	if n := strings.Count(stripCodeComments(src), ".animate("); n != 3 {
		t.Errorf("want 3 code .animate( calls after stripping comments, got %d: %q", n, stripCodeComments(src))
	}
}
