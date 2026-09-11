package httpx

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/uislot"
)

// stubRailAmendments overrides RailAmendmentsProvider for one test and
// restores the prior value after — the same t.Cleanup contract every other
// seam in this package (UpdateAvailable, CrossDeviceLinkActionable) uses.
func stubRailAmendments(t *testing.T, amendments []uislot.Amendment) {
	t.Helper()
	prev := RailAmendmentsProvider
	RailAmendmentsProvider = func() []uislot.Amendment { return amendments }
	t.Cleanup(func() { RailAmendmentsProvider = prev })
}

// renderNav executes web/ui/partials/nav.html's "nav" define through the
// same ClonedTemplate path Render uses, with the request-locale funcs.
func renderNav(t *testing.T, locale string) string {
	t.Helper()
	tpl, err := ClonedTemplate("httpx.rail_test:nav", "base.html", FuncsFor(locale),
		"ui/layouts/base.html", "ui/partials/nav.html", "ui/partials/bugreport_panel.html")
	if err != nil {
		t.Fatalf("parse nav: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "nav", map[string]any{}); err != nil {
		t.Fatalf("execute nav: %v", err)
	}
	return buf.String()
}

// navPrimaryBlock cuts the rail's .nav-primary <div> out of a rendered nav.
func navPrimaryBlock(t *testing.T, nav string) string {
	t.Helper()
	start := strings.Index(nav, `<div class="nav-primary">`)
	if start < 0 {
		t.Fatalf("no .nav-primary block in: %s", nav)
	}
	end := strings.Index(nav[start:], "</div>")
	if end < 0 {
		t.Fatalf("unterminated .nav-primary block in: %s", nav)
	}
	return nav[start : start+end+len("</div>")]
}

// normalizeLines trims every line and drops blank ones. html/template
// strips HTML comments but keeps the newline+indent text nodes around them,
// so the pre-#1912 hardcoded block rendered a blank line wherever one of
// its explanatory comments sat between two links (before "/", before
// "/inventory", before "/orders" — not before "/menu"); the {{ range }}
// can't reproduce that comment-shaped whitespace and shouldn't try. Inside
// a display:flex container (.nav-primary, at every breakpoint — app.css)
// inter-item whitespace text nodes have no layout effect, so this is the
// only normalization the golden comparison needs, and it is stated here
// rather than hidden.
func normalizeLines(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// legacyNavPrimary is web/ui/partials/nav.html's .nav-primary block exactly
// as it stood before ut-docs#1912 (comments elided — html/template drops
// them from output anyway): the four hardcoded <a> tags this card turned
// into a slot. Rendered through the same funcs and compared against the
// live template, so the golden is "what the hardcoded markup produced",
// not a hand-copied string that could itself be typed wrong.
const legacyNavPrimary = `{{ define "legacy_nav_primary" -}}
  <div class="nav-primary">
    <a href="/" class="nav-toggle" data-testid="nav-till">
      <span class="nav-toggle-ico" aria-hidden="true">{{ icon "shopping-cart" }}</span>
      <span class="nav-toggle-label">{{ T "nav.till" }}</span>
    </a>
    <a href="/menu" class="nav-toggle" data-testid="nav-menu">
      <span class="nav-toggle-ico" aria-hidden="true">{{ icon "menu" }}</span>
      <span class="nav-toggle-label">{{ T "nav.menu" }}</span>
    </a>
    <a href="/inventory" class="nav-toggle nav-rail-only" data-testid="kiosk-inventory-link">
      <span class="nav-toggle-ico" aria-hidden="true">{{ icon "package" }}</span>
      <span class="nav-toggle-label">{{ T "kiosk.inventory" }}</span>
    </a>
    <a href="/orders" class="nav-toggle nav-rail-only" data-testid="nav-orders">
      <span class="nav-toggle-ico" aria-hidden="true">{{ icon "bell" }}</span>
      <span class="nav-toggle-label">{{ T "nav.orders" }}</span>
    </a>
  </div>
{{- end }}`

// TestNavRail_GoldenZeroPlugin is ADR-0088 Decision I's behavioural
// guarantee for the slot that renders on every page: with no `layout`
// plugin installed, the slotted rail renders the SAME four links, in the
// same order, with the same hrefs, classes (.nav-rail-only on Inventory
// and Orders only), data-testid hooks, icons and labels the hardcoded
// markup did — the only difference being the comment-shaped blank lines
// normalizeLines explains.
func TestNavRail_GoldenZeroPlugin(t *testing.T) {
	InitI18n(realI18n(t), "en")
	stubRailAmendments(t, nil)

	legacy, err := template.New("legacy").Funcs(FuncsFor("en")).Parse(legacyNavPrimary)
	if err != nil {
		t.Fatalf("parse legacy block: %v", err)
	}
	var want bytes.Buffer
	if err := legacy.ExecuteTemplate(&want, "legacy_nav_primary", nil); err != nil {
		t.Fatalf("execute legacy block: %v", err)
	}

	got := navPrimaryBlock(t, renderNav(t, "en"))
	if normalizeLines(got) != normalizeLines(want.String()) {
		t.Fatalf("zero-plugin rail drifted from the pre-#1912 hardcoded markup:\n--- got ---\n%s\n--- want ---\n%s", got, want.String())
	}

	// And, human-readable: the four open tags verbatim, in order — so a
	// drift in the test's own legacy fixture can't mask a real one.
	wantTags := []string{
		`<a href="/" class="nav-toggle" data-testid="nav-till">`,
		`<a href="/menu" class="nav-toggle" data-testid="nav-menu">`,
		`<a href="/inventory" class="nav-toggle nav-rail-only" data-testid="kiosk-inventory-link">`,
		`<a href="/orders" class="nav-toggle nav-rail-only" data-testid="nav-orders">`,
	}
	rest := got
	for _, tag := range wantTags {
		i := strings.Index(rest, tag)
		if i < 0 {
			t.Fatalf("expected %s in order in the rail, got:\n%s", tag, got)
		}
		rest = rest[i+len(tag):]
	}
	if n := strings.Count(got, `<a `); n != len(wantTags) {
		t.Fatalf("expected exactly %d rail links, got %d:\n%s", len(wantTags), n, got)
	}
	for _, label := range []string{">Sell<", ">Menu<", ">Inventory<", ">Orders<"} {
		if !strings.Contains(got, label) {
			t.Errorf("expected translated label %s in the rail", label)
		}
	}
}

// Decision I, extended to the whole render-side mapping (not just
// uislot.Resolve): with no amendments the template func hands back the
// package-init coreRailView — no copy, no map, no allocation. nav renders
// on every request, so this is the hottest slot path in the product.
func TestRailEntries_ZeroAmendmentsAllocateNothing(t *testing.T) {
	var sink []RailEntry
	allocs := testing.AllocsPerRun(1000, func() { sink = railEntriesFor("en", nil) })
	if allocs != 0 {
		t.Fatalf("zero-amendment railEntriesFor allocated %v times per run, want 0 (ADR-0088 Decision I)", allocs)
	}
	if len(sink) != len(uislot.CoreRail) || &sink[0] != &coreRailView[0] {
		t.Fatal("zero-amendment railEntriesFor must return coreRailView itself, not a copy")
	}
	// The bound template func too — through the real provider seam.
	stubRailAmendments(t, nil)
	fn := railEntriesFunc("en")
	allocs = testing.AllocsPerRun(1000, func() { sink = fn() })
	if allocs != 0 {
		t.Fatalf("zero-amendment {{ railEntries }} allocated %v times per run, want 0", allocs)
	}
}

func BenchmarkRailEntries_ZeroAmendments(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = railEntriesFor("en", nil)
	}
}

// Every declared core rail link has its presentation (data-testid, the
// phone-width class) — and nothing in railPresentation names a key that
// no longer exists in CoreRail. The e2e suites (pos_ui_mvp, sale-rail-
// orders-1349, nav-rail-svg-icons-1423, ...) locate links by these hooks.
func TestRailPresentation_CoversEveryCoreRailKey(t *testing.T) {
	for _, e := range uislot.CoreRail {
		p, ok := railPresentation[e.Key]
		if !ok || p.TestID == "" {
			t.Errorf("core rail link %q has no data-testid presentation — the e2e suites would lose their hook", e.Key)
		}
	}
	for key := range railPresentation {
		if _, ok := uislot.CoreRailEntry(key); !ok {
			t.Errorf("railPresentation names %q, which is not a CoreRail key", key)
		}
	}
	// The exact pre-#1912 attribute values, pinned by name.
	want := map[string]struct {
		TestID   string
		RailOnly bool
	}{
		"/":          {"nav-till", false},
		"/menu":      {"nav-menu", false},
		"/inventory": {"kiosk-inventory-link", true},
		"/orders":    {"nav-orders", true},
	}
	for key, w := range want {
		if got := railPresentation[key]; got.TestID != w.TestID || got.RailOnly != w.RailOnly {
			t.Errorf("%s: presentation %+v, want %+v", key, got, w)
		}
	}
}

// A `layout` plugin's Rail-slot amendments — the reorder plugins/layout-
// salon ships, plus a relabel — render through the same Resolve path the
// Menu and Items slots use: order follows the amendment, the relabelled
// link shows the translated new label, and the link's own data-testid and
// .nav-rail-only presentation travel with it (they're keyed by link, not
// by position).
func TestNavRail_ReorderAndRelabelAmendments(t *testing.T) {
	InitI18n(realI18n(t), "en")
	ordersFirst := 50
	stubRailAmendments(t, []uislot.Amendment{
		{PluginID: "com.example.layout", Slot: uislot.RailSlot, Key: "/orders", Order: &ordersFirst},
		// nav.items ("Items") is a core key that translates in every locale
		// — a relabel that resolves, so no Decision G fallback applies.
		{PluginID: "com.example.layout", Slot: uislot.RailSlot, Key: "/inventory", LabelKey: "nav.items"},
	})
	got := navPrimaryBlock(t, renderNav(t, "en"))

	wantTags := []string{
		`<a href="/orders" class="nav-toggle nav-rail-only" data-testid="nav-orders">`,
		`<a href="/" class="nav-toggle" data-testid="nav-till">`,
		`<a href="/menu" class="nav-toggle" data-testid="nav-menu">`,
		`<a href="/inventory" class="nav-toggle nav-rail-only" data-testid="kiosk-inventory-link">`,
	}
	rest := got
	for _, tag := range wantTags {
		i := strings.Index(rest, tag)
		if i < 0 {
			t.Fatalf("expected %s in order after the amendments, got:\n%s", tag, got)
		}
		rest = rest[i+len(tag):]
	}
	if n := strings.Count(got, `<a `); n != len(uislot.CoreRail) {
		t.Fatalf("a reorder/relabel must not drop or duplicate links, got %d", n)
	}
	if !strings.Contains(got, `<span class="nav-toggle-label">Items</span>`) {
		t.Fatalf("expected /inventory relabelled to the translated \"Items\", got:\n%s", got)
	}
	if strings.Contains(got, ">Inventory<") {
		t.Fatalf("the core label must be replaced, not shown alongside, got:\n%s", got)
	}
	// The icon is never amendable on this slot: /inventory keeps "package".
	if !strings.Contains(got, `data-icon="package"`) {
		t.Fatalf("relabel must not touch the icon, got:\n%s", got)
	}
}

// ADR-0088 Decision G on the rail: an amended label key with NO translation
// for the request locale degrades to the CORE label — never to the raw
// plugin key on the rail of every page.
func TestNavRail_UntranslatedRelabelFallsBackToCoreLabel(t *testing.T) {
	InitI18n(realI18n(t), "en")
	stubRailAmendments(t, []uislot.Amendment{
		{PluginID: "com.example.layout", Slot: uislot.RailSlot, Key: "/inventory", LabelKey: "layout.example.untranslated"},
	})
	got := navPrimaryBlock(t, renderNav(t, "en"))
	if strings.Contains(got, "layout.example.untranslated") {
		t.Fatalf("a raw plugin key must never reach the rail, got:\n%s", got)
	}
	if !strings.Contains(got, `<span class="nav-toggle-label">Inventory</span>`) {
		t.Fatalf("expected the core label as Decision G's fallback, got:\n%s", got)
	}
}

// The provider seam's default is the zero-plugin rail, so every caller of
// this package that never wires internal/pages.Init (tests, internal/ui's
// fragment views) renders the core four links unchanged.
func TestRailAmendmentsProvider_DefaultIsZeroPlugin(t *testing.T) {
	if got := RailAmendmentsProvider(); got != nil {
		t.Fatalf("default provider must return nil (the zero-plugin rail), got %+v", got)
	}
	got := railEntriesFunc("en")()
	if len(got) != len(uislot.CoreRail) {
		t.Fatalf("want %d core links, got %d", len(uislot.CoreRail), len(got))
	}
	for i, e := range uislot.CoreRail {
		if got[i].Href != e.Href || got[i].LabelKey != e.LabelKey || got[i].Icon != e.Icon {
			t.Errorf("link %d: got %+v, want core %+v", i, got[i], e)
		}
	}
}

// railEntries must be defined in BOTH func maps: the locale-less baseFuncs
// fallback (so internal/ui's fragment views, which parse nav.html without
// executing it, still parse) and FuncsFor's locale-bound override (what
// every whole-page render executes) — the same two-level shape helpHref
// and helpLink already have, and what ClonedTemplate's "same set of func
// names on every call" contract requires.
func TestRailEntriesIsATemplateFuncInBothMaps(t *testing.T) {
	if _, ok := baseFuncs["railEntries"]; !ok {
		t.Fatal("baseFuncs must define railEntries (parse-time fallback)")
	}
	if _, ok := FuncsFor("de")["railEntries"]; !ok {
		t.Fatal("FuncsFor must define railEntries (locale-bound)")
	}
}
