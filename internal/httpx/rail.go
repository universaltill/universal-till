package httpx

import (
	"sync/atomic"

	"github.com/universaltill/universal-till/internal/uislot"
)

// RailEntry is one RESOLVED link of the nav rail — the render-time shape
// web/ui/partials/nav.html's `{{ range railEntries }}` consumes (ADR-0088
// Decision C, generalized to the rail by ut-docs#1912). Key/Href/Icon come
// straight from uislot.CoreRail (or its amended copy); LabelKey is the key
// the template hands to {{ T }} (already degraded to the core key when an
// amendment's own key has no translation — Decision G, see railLabel);
// TestID and RailOnly are the two render-only attributes the old hardcoded
// anchors carried per link, mapped from Key below so a reorder never loses
// them.
type RailEntry struct {
	Key, Href, LabelKey, Icon string
	// TestID is the anchor's data-testid — stable per key because e2e
	// specs select on it (nav-till, nav-menu, kiosk-inventory-link,
	// nav-orders).
	TestID string
	// RailOnly adds .nav-rail-only: hidden at phone width (<=480px), where
	// ut-docs#413's top-bar wrap has no budget for a third row — Inventory
	// and Orders are both reachable from the ☰ Menu there (ut-docs#1349).
	RailOnly bool
}

// railPresentation is the per-key render-only attributes nav.html's four
// hardcoded anchors carried before ut-docs#1912 — kept beside the renderer,
// not in uislot.Entry, because they are how THIS template draws a link,
// not slot data a plugin could amend. Every uislot.CoreRail key has a row
// (TestCoreRailPresentationCoversEveryKey pins it); an amendment can only
// reorder/relabel existing keys, never add one, so the lookup can't miss.
var railPresentation = map[string]struct {
	testID   string
	railOnly bool
}{
	"/":          {testID: "nav-till"},
	"/menu":      {testID: "nav-menu"},
	"/inventory": {testID: "kiosk-inventory-link", railOnly: true},
	"/orders":    {testID: "nav-orders", railOnly: true},
}

// coreRailView is the zero-amendment rail, built once: with no `layout`
// plugin installed railEntriesFor hands this exact slice back on every
// request (ADR-0088 Decision I — nav.html is on every page, so this is the
// hottest zero-plugin path in the product;
// TestRailEntries_ZeroAmendmentsAllocatesNothing pins 0 allocs).
var coreRailView = buildRailView(uislot.CoreRail, "")

// railAmendmentsSource is the process-global reader for the rail-slot
// amendments in force — pages.Init wires it to common.Deps'
// RailAmendmentsSnapshot (the PluginMu-locked accessor), so this package
// never touches the plugin manager directly and stays importable by
// internal/pages/common. Same shape as i18nRef: an atomic.Value holding a
// typed func, nil-func meaning "no source wired" (a test, or a boot
// before Init) = zero-plugin.
var railAmendmentsSource atomic.Value // func() []uislot.Amendment

// InitRailAmendments wires the rail slot's amendment reader (ut-docs#1912).
// Called once from pages.Init with Deps.RailAmendmentsSnapshot; nil resets
// to the zero-plugin state (tests).
func InitRailAmendments(src func() []uislot.Amendment) {
	railAmendmentsSource.Store(src)
}

func railAmendments() []uislot.Amendment {
	src, _ := railAmendmentsSource.Load().(func() []uislot.Amendment)
	if src == nil {
		return nil
	}
	return src()
}

// railEntriesFor resolves the rail for one render — the value FuncsFor's
// `railEntries` template func returns. Zero amendments: coreRailView, no
// allocation. Otherwise uislot.Resolve's amended copy, mapped through
// buildRailView with the locale for Decision G's label fallback.
func railEntriesFor(locale string) []RailEntry {
	amendments := railAmendments()
	if len(amendments) == 0 {
		return coreRailView
	}
	return buildRailView(uislot.Resolve(uislot.CoreRail, amendments), locale)
}

func buildRailView(entries []uislot.Entry, locale string) []RailEntry {
	out := make([]RailEntry, len(entries))
	for i, e := range entries {
		p := railPresentation[e.Key]
		out[i] = RailEntry{
			Key:      e.Key,
			Href:     e.Href,
			LabelKey: railLabel(locale, e),
			Icon:     e.Icon,
			TestID:   p.testID,
			RailOnly: p.railOnly,
		}
	}
	return out
}

// railLabel is menu_page.go's menuLabel / itemsnav's sectionLabel restated
// for the rail (ADR-0088 Decision G): an amended entry's LabelKey resolves
// through the normal translator; if it does not resolve for this locale (T
// hands the key back unchanged), the CORE label key renders instead — a
// missing translation degrades to English, never to a raw plugin key on a
// merchant's screen. An unamended entry has no fallback and renders its
// own key.
func railLabel(locale string, e uislot.Entry) string {
	if e.LabelFallback != "" && T(locale, e.LabelKey) == e.LabelKey {
		return e.LabelFallback
	}
	return e.LabelKey
}
