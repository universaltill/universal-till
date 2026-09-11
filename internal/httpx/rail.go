package httpx

import (
	"github.com/universaltill/universal-till/internal/uislot"
)

// The nav rail's primary links (web/ui/partials/nav.html's .nav-primary
// block) are an ADR-0088 UI slot since ut-docs#1912: uislot.CoreRail
// declares the four core links, a `layout` plugin may reorder or re-label
// them, and nav.html renders whatever {{ railEntries }} — the template func
// bound below — resolves.
//
// Why the resolution lives HERE, as a template func, and not in each page
// handler's data map (the way /menu and /items resolve their own slots):
// nav.html is a shared partial base.html includes on EVERY page, rendered
// by Render/RenderWith/RenderContentFragment/RenderError plus internal/ui's
// fragment views — none of which see a *common.Deps, and threading a new
// "rail" key through every page handler's own map[string]any was ruled out
// by the design. The same shape helpHref already uses for the contextual
// "?" (a template func bound per render), except that helpHref only needs
// the request path while the rail needs live plugin state.
//
// Why a package-level provider variable and not a *common.Deps parameter:
// internal/pages/common already imports internal/httpx (errors.go and
// others), so httpx cannot import it back — the dependency has to be
// inverted. RailAmendmentsProvider is that seam: it starts as a no-op (the
// zero-plugin rail, so every existing caller and test of this package keeps
// working unchanged) and internal/pages.Init assigns the real
// Deps.RailAmendmentsSnapshot once at boot, closing over the live Deps.
// uislot itself imports nothing in the product, so httpx importing it is
// cycle-free (icons_test.go already did).

// RailAmendmentsProvider returns the Rail-slot amendments in force — the
// dependency-inversion seam described above. internal/pages.Init sets it
// to common.Deps.RailAmendmentsSnapshot (a PluginMu read-locked accessor,
// so the render path never reads a field Manager.Reload reassigns
// unlocked). The default returns nil: the zero-plugin rail. Assigned once
// at boot before any request is served; a test that overrides it must
// restore the prior value (t.Cleanup), same contract as UpdateAvailable /
// CrossDeviceLinkActionable above.
var RailAmendmentsProvider = func() []uislot.Amendment { return nil }

// RailEntry is one RESOLVED rail link — the render-time shape nav.html's
// {{ range railEntries }} consumes. Everything an amendment can change
// (order, label) comes through uislot.Resolve; everything it cannot
// (icon, data-testid, the phone-width class) comes from CoreRail and
// railPresentation.
type RailEntry struct {
	Href string
	// LabelKey is the locale key the template hands to {{ T }} — after
	// ADR-0088 Decision G's fallback (railLabel): an amended key that does
	// not resolve for this locale degrades to the core key, never to the
	// raw plugin key on a merchant's screen.
	LabelKey string
	// Icon is the core icon NAME for {{ icon }} — never amended on this
	// slot (railSpec refuses icon), so no IconFallback chain is needed.
	Icon string
	// TestID is the link's data-testid — the hook the e2e suites locate
	// each link by (nav-till, nav-menu, kiosk-inventory-link, nav-orders).
	TestID string
	// RailOnly adds .nav-rail-only (app.css: hidden at <=480px, where the
	// rail becomes a top bar with no spare room — ut-docs#413) — true for
	// the links index.html re-renders in its own phone-only fallback row.
	RailOnly bool
}

// railPresentation is the per-link presentation nav.html carried on each
// hardcoded <a> before ut-docs#1912 and uislot.Entry does not model: the
// e2e data-testid hook and the phone-width breakpoint class. Keyed by
// uislot.CoreRail Key; TestRailPresentation_CoversEveryCoreRailKey pins
// that no core link is ever rendered without its hook.
var railPresentation = map[string]struct {
	TestID   string
	RailOnly bool
}{
	"/":          {TestID: "nav-till"},
	"/menu":      {TestID: "nav-menu"},
	"/inventory": {TestID: "kiosk-inventory-link", RailOnly: true},
	"/orders":    {TestID: "nav-orders", RailOnly: true},
}

// coreRailView is uislot.CoreRail pre-shaped for the template, built once
// at package init — the zero-amendment render path hands this slice back
// as-is (ADR-0088 Decision I, extended from Resolve to the whole
// render-side mapping: nav renders on every request, so a per-render
// four-element allocation here would be the single most frequent
// allocation in the template layer). Never mutated after init.
var coreRailView = func() []RailEntry {
	out := make([]RailEntry, len(uislot.CoreRail))
	for i, e := range uislot.CoreRail {
		out[i] = railEntryOf(e, e.LabelKey)
	}
	return out
}()

func railEntryOf(e uislot.Entry, labelKey string) RailEntry {
	p := railPresentation[e.Key]
	return RailEntry{Href: e.Href, LabelKey: labelKey, Icon: e.Icon, TestID: p.TestID, RailOnly: p.RailOnly}
}

// railEntriesFor resolves the rail for one render: uislot.CoreRail amended
// by amendments (Decision C), labels passed through Decision G's fallback
// for locale. With no amendments — the till's normal, zero-plugin state —
// it returns coreRailView directly: no Resolve copy, no mapping, no
// allocation (TestRailEntries_ZeroAmendmentsAllocateNothing pins it).
func railEntriesFor(locale string, amendments []uislot.Amendment) []RailEntry {
	if len(amendments) == 0 {
		return coreRailView
	}
	resolved := uislot.Resolve(uislot.CoreRail, amendments)
	out := make([]RailEntry, len(resolved))
	for i, e := range resolved {
		out[i] = railEntryOf(e, railLabel(locale, e))
	}
	return out
}

// railLabel is menu_page.go's menuLabel / itemsnav's sectionLabel, restated
// for the Rail slot (ADR-0088 Decision G): an amended entry's LabelKey
// resolves through the normal translator; if it does not resolve for this
// locale (T hands the key back unchanged), the CORE label key renders
// instead — a missing translation degrades to English, never to a raw,
// untranslated plugin key on the rail of every page.
func railLabel(locale string, e uislot.Entry) string {
	if e.LabelFallback != "" && T(locale, e.LabelKey) == e.LabelKey {
		return e.LabelFallback
	}
	return e.LabelKey
}

// railEntriesFunc is the {{ railEntries }} template func bound for locale.
func railEntriesFunc(locale string) func() []RailEntry {
	return func() []RailEntry { return railEntriesFor(locale, RailAmendmentsProvider()) }
}
