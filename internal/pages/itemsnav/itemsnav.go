// Package itemsnav holds the /items left-rail section list (ut-docs#1950),
// shared by every one of that rail's five destinations' own handlers:
// /catalog, /modifiers and /catalog/option-sets (internal/pages/catalog)
// plus /categories and /inventory (internal/pages). It exists as its own
// leaf package — rather than living only on internal/pages/items_page.go's
// itemsSections, as it did before this card — because internal/pages
// already imports internal/pages/catalog (catalog.Register), so the reverse
// import that package would need to reach a rail definition living in
// internal/pages is a cycle. Neither internal/ui nor internal/httpx import
// this package, so both internal/pages and internal/pages/catalog can.
//
// Since ut-docs#1911 the section list is DATA resolved through
// internal/uislot's ADR-0088 registry (uislot.CoreItems + any active
// `layout` plugin's Items-slot amendments), the same mechanism
// internal/pages/menu_page.go already uses for the Menu launcher — not the
// fixed slice this package declared before that card (ut-docs#1897's
// original shape). Resolve below is that conversion's one entry point.
package itemsnav

import (
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/uislot"
)

// Section is one RESOLVED row of the /items rail — the render-time shape
// items_rail.html consumes. Href empty means the section has no screen yet
// and renders as a disabled "coming soon" row instead of a dead link (see
// items_rail.html) — no row is currently disabled, but the mechanism stays
// for the next section that lands before its own screen does, same as
// before ut-docs#1911 (originally ut-docs#1897).
type Section struct {
	NameKey, SubtitleKey string
	Href                 string // empty = not yet available
}

// Resolve returns the /items rail's rows for one render: uislot.CoreItems
// amended by amendments (ADR-0088 Decision C) — the same
// menuSlotEntries+uislot.Resolve+menuLabel pattern menu_page.go uses for the
// Menu slot, generalized to the Items slot by ut-docs#1911. With no
// amendments (the till's normal, zero-plugin state) uislot.Resolve hands
// CoreItems straight back (Decision I's zero-allocation fast path), so this
// is a length-checked no-op mapping over the SAME five rows
// items_page.go's old hardcoded Sections slice always declared.
func Resolve(locale string, amendments []uislot.Amendment) []Section {
	resolved := uislot.Resolve(uislot.CoreItems, amendments)
	out := make([]Section, len(resolved))
	for i, e := range resolved {
		out[i] = Section{
			NameKey:     sectionLabel(locale, e),
			SubtitleKey: e.SubtitleKey,
			Href:        e.Href,
		}
	}
	return out
}

// sectionLabel is menu_page.go's menuLabel, restated here for the Items
// slot (ADR-0088 Decision G): an amended entry's LabelKey resolves through
// the normal translator; if it does not resolve for this locale (T hands
// the key back unchanged), the CORE label key renders instead — a missing
// translation degrades to English, never to a raw, untranslated plugin key
// on a merchant's screen.
func sectionLabel(locale string, e uislot.Entry) string {
	if e.LabelFallback != "" && httpx.T(locale, e.LabelKey) == e.LabelKey {
		return e.LabelFallback
	}
	return e.LabelKey
}
