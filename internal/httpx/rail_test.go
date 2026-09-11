package httpx

import (
	"bytes"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/uislot"
)

// renderNav executes nav.html the way every whole-page render path does
// (base.html + nav.html + bugreport_panel.html, FuncsFor(locale) with the
// per-request helpHref bound) and returns the "nav" define's output.
func renderNav(t *testing.T, locale string) string {
	t.Helper()
	r := httptest.NewRequest("GET", "/catalog", nil)
	tpl, err := ClonedTemplate("rail-test:"+t.Name(), "base.html", withHelpHref(FuncsFor(locale), r),
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

// navPrimary extracts the .nav-primary block with whitespace normalized:
// html/template strips the HTML comments nav.html carries between its
// links, leaving indentation-only blank lines whose exact count depends on
// how many comment blocks sat between two anchors — irrelevant to the
// DOM, so collapsed here so the golden compares markup, not comment
// placement.
func navPrimary(t *testing.T, nav string) string {
	t.Helper()
	start := strings.Index(nav, `<div class="nav-primary">`)
	end := strings.Index(nav, `<div class="nav-right">`)
	if start < 0 || end < 0 || end < start {
		t.Fatalf("nav has no .nav-primary block: %s", nav)
	}
	block := nav[start:end]
	block = regexp.MustCompile(`\s+`).ReplaceAllString(block, " ")
	block = strings.ReplaceAll(block, "> <", "><")
	return strings.TrimSpace(block)
}

// zeroPluginRail returns exactly what nav.html's four hardcoded anchors
// rendered before ut-docs#1912 converted them into a uislot slot — captured
// from the pre-change template (same normalization as navPrimary), so this
// is the byte-level "zero-plugin till renders an identical rail" golden,
// not a re-reading of the new template.
func zeroPluginRail() string {
	link := func(href, cls, testid, icon, label string) string {
		return `<a href="` + href + `" class="` + cls + `" data-testid="` + testid + `">` +
			`<span class="nav-toggle-ico" aria-hidden="true">` + string(iconHTML(icon)) + `</span>` +
			`<span class="nav-toggle-label">` + label + `</span></a>`
	}
	return `<div class="nav-primary">` +
		link("/", "nav-toggle", "nav-till", "shopping-cart", "Sell") +
		link("/menu", "nav-toggle", "nav-menu", "menu", "Menu") +
		link("/inventory", "nav-toggle nav-rail-only", "kiosk-inventory-link", "package", "Inventory") +
		link("/orders", "nav-toggle nav-rail-only", "nav-orders", "bell", "Order status") +
		`</div>`
}

// resetRail leaves the process-global rail amendment source in its
// zero-plugin state after a test that installed one.
func resetRail(t *testing.T) {
	t.Helper()
	InitRailAmendments(nil)
	t.Cleanup(func() { InitRailAmendments(nil) })
}

// ut-docs#1912 AC: a till with no `layout` plugin renders the rail exactly
// as it did when nav.html hardcoded the four links — same hrefs, same
// classes (including .nav-rail-only on Inventory/Orders, the phone-width
// behaviour ut-docs#413 fought for), same data-testids (e2e specs select
// on nav-till / nav-menu / kiosk-inventory-link / nav-orders), same icons,
// same labels, same order.
func TestNavRail_ZeroPluginRendersIdenticalMarkup(t *testing.T) {
	InitI18n(realI18n(t), "en")
	resetRail(t)
	got := navPrimary(t, renderNav(t, "en"))
	if want := zeroPluginRail(); got != want {
		t.Fatalf("zero-plugin rail markup changed\n got: %s\nwant: %s", got, want)
	}
}

// Decision I on the hottest render path in the product (nav.html is on
// every page): resolving the rail with no amendments must not allocate —
// it hands back the one core view built at init.
func TestRailEntries_ZeroAmendmentsAllocatesNothing(t *testing.T) {
	resetRail(t)
	var sink []RailEntry
	allocs := testing.AllocsPerRun(1000, func() { sink = railEntriesFor("en") })
	if allocs != 0 {
		t.Fatalf("zero-amendment railEntriesFor allocated %v times per run, want 0 (ADR-0088 Decision I)", allocs)
	}
	if len(sink) != len(uislot.CoreRail) {
		t.Fatalf("want %d rail entries, got %+v", len(uislot.CoreRail), sink)
	}
}

// The view carries the render-only attributes nav.html needs per key —
// the e2e-visible data-testid and the phone-width .nav-rail-only flag —
// mapped from the key, so an amendment reordering a row never loses them.
func TestRailEntries_ZeroAmendmentsMatchesCoreRail(t *testing.T) {
	resetRail(t)
	got := railEntriesFor("en")
	want := []RailEntry{
		{Key: "/", Href: "/", LabelKey: "nav.till", Icon: "shopping-cart", TestID: "nav-till"},
		{Key: "/menu", Href: "/menu", LabelKey: "nav.menu", Icon: "menu", TestID: "nav-menu"},
		{Key: "/inventory", Href: "/inventory", LabelKey: "kiosk.inventory", Icon: "package", TestID: "kiosk-inventory-link", RailOnly: true},
		{Key: "/orders", Href: "/orders", LabelKey: "nav.orders", Icon: "bell", TestID: "nav-orders", RailOnly: true},
	}
	if len(got) != len(want) {
		t.Fatalf("want %d entries, got %+v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: want %+v, got %+v", i, want[i], got[i])
		}
	}
}

// A `layout` plugin's rail-slot reorder (the plugins/layout-salon demo:
// Orders ahead of Inventory) changes the DOM order and nothing else — the
// moved anchor keeps its testid, its .nav-rail-only class and its icon.
func TestNavRail_ReorderAmendmentMovesTheAnchor(t *testing.T) {
	InitI18n(realI18n(t), "en")
	resetRail(t)
	order := 250
	InitRailAmendments(func() []uislot.Amendment {
		return []uislot.Amendment{{PluginID: "p", Slot: uislot.RailSlot, Key: "/orders", Order: &order}}
	})
	got := navPrimary(t, renderNav(t, "en"))
	link := func(href, cls, testid, icon, label string) string {
		return `<a href="` + href + `" class="` + cls + `" data-testid="` + testid + `">` +
			`<span class="nav-toggle-ico" aria-hidden="true">` + string(iconHTML(icon)) + `</span>` +
			`<span class="nav-toggle-label">` + label + `</span></a>`
	}
	want := `<div class="nav-primary">` +
		link("/", "nav-toggle", "nav-till", "shopping-cart", "Sell") +
		link("/menu", "nav-toggle", "nav-menu", "menu", "Menu") +
		link("/orders", "nav-toggle nav-rail-only", "nav-orders", "bell", "Order status") +
		link("/inventory", "nav-toggle nav-rail-only", "kiosk-inventory-link", "package", "Inventory") +
		`</div>`
	if got != want {
		t.Fatalf("reordered rail markup\n got: %s\nwant: %s", got, want)
	}
}

// Decision G on the rail: a relabel whose key has no translation for the
// locale degrades to the CORE label ("Inventory"), never to the raw plugin
// key on a merchant's screen; a key that does resolve renders its text.
func TestNavRail_RelabelFallsBackToCoreLabelWhenUntranslated(t *testing.T) {
	InitI18n(realI18n(t), "en")
	resetRail(t)
	InitRailAmendments(func() []uislot.Amendment {
		return []uislot.Amendment{{PluginID: "p", Slot: uislot.RailSlot, Key: "/inventory", LabelKey: "layout.nope.missing"}}
	})
	nav := renderNav(t, "en")
	if strings.Contains(nav, "layout.nope.missing") {
		t.Fatalf("raw plugin key leaked into the rail: %s", nav)
	}
	if !strings.Contains(nav, `<span class="nav-toggle-label">Inventory</span>`) {
		t.Fatalf("missing translation must fall back to the core label, got: %s", nav)
	}
	// An existing, translated core key as the relabel: renders that text.
	InitRailAmendments(func() []uislot.Amendment {
		return []uislot.Amendment{{PluginID: "p", Slot: uislot.RailSlot, Key: "/inventory", LabelKey: "nav.items"}}
	})
	nav = renderNav(t, "en")
	if !strings.Contains(nav, `data-testid="kiosk-inventory-link"`) || !strings.Contains(nav, `<span class="nav-toggle-label">Items</span>`) {
		t.Fatalf("a resolvable relabel must render its text on the same anchor, got: %s", nav)
	}
}

// nav.html now renders `{{ icon .Icon }}` rather than four literal
// `{{ icon "name" }}` calls, so TestRailIconsReferencedByTemplatesExist's
// template scanner can no longer see these four names — per icons.go's
// own rule, a Go-side icon name owns its own resolution test.
func TestCoreRailIconsResolve(t *testing.T) {
	for _, e := range uislot.CoreRail {
		if iconHTML(e.Icon) == "" {
			t.Errorf("CoreRail %q names unknown icon %q (known: %v)", e.Key, e.Icon, IconNames())
		}
	}
}

// CoreRail's LabelKeys are resolved dynamically via railEntries/T, never as
// a literal `{{ T "..." }}` call nav.html's own markup carries — so
// guard-i18n.sh's template scanner cannot see them (independent review of
// ut-docs#1912 confirmed this directly: deleting "nav.till" from every
// locale file still left the guard exiting 0). This test is what actually
// pins that every CoreRail label key resolves to real translated text in
// every locale core ships, mirroring TestCoreRailIconsResolve above for the
// icon side.
func TestCoreRailLabelKeysResolve(t *testing.T) {
	InitI18n(realI18n(t), "en")
	t.Cleanup(func() { InitI18n(nil, "en") })
	for _, locale := range []string{"en", "fa", "ar", "tr"} {
		for _, e := range uislot.CoreRail {
			if got := T(locale, e.LabelKey); got == e.LabelKey {
				t.Errorf("locale %q: CoreRail %q's label key %q does not resolve (T returned the raw key)", locale, e.Key, e.LabelKey)
			}
		}
	}
}

// Every render path hands nav.html a railEntries func: the locale-bound
// one from FuncsFor, and a baseFuncs fallback for the fragment renderers
// that parse with baseFuncs only (same shape as helpHref/helpLink).
func TestFuncsForIncludesRailEntries(t *testing.T) {
	if _, ok := FuncsFor("en")["railEntries"]; !ok {
		t.Fatal("FuncsFor is missing railEntries")
	}
	if _, ok := baseFuncs["railEntries"]; !ok {
		t.Fatal("baseFuncs is missing the railEntries fallback")
	}
}

// railPresentation must cover every declared rail key — a core row added to
// uislot.CoreRail without a testid/rail-only mapping here would render an
// empty data-testid and silently break the e2e selectors.
func TestCoreRailPresentationCoversEveryKey(t *testing.T) {
	for _, e := range uislot.CoreRail {
		p, ok := railPresentation[e.Key]
		if !ok || p.testID == "" {
			t.Errorf("uislot.CoreRail %q has no railPresentation row (data-testid)", e.Key)
		}
	}
	for key := range railPresentation {
		if _, ok := uislot.CoreRailEntry(key); !ok {
			t.Errorf("railPresentation names %q, which uislot.CoreRail does not declare", key)
		}
	}
}
