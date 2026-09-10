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
// This is the same struct shape items_page.go's itemsSection always had
// (ut-docs#1897) — relocated, not redesigned; ut-docs#1911's ADR-0088
// slot-registry conversion (unclaimed, out of scope here) is a separate,
// larger rewrite of how this list itself is produced, and should fold this
// package in rather than leave a second, competing list.
package itemsnav

// Section is one row of the /items rail. Href empty means the section has
// no screen yet and renders as a disabled "coming soon" row instead of a
// dead link (see items_rail.html) — no row is currently disabled, but the
// mechanism stays for the next section that lands before its own screen
// does, same as before this card (ut-docs#1897).
type Section struct {
	NameKey, SubtitleKey string
	Href                 string // empty = not yet available
}

// Sections is the fixed section list — five rows, same keys/hrefs
// items_page.go's itemsSections always had. Library reuses nav.catalog (the
// same key /catalog's own <h1> renders) rather than a separate "Library"
// name (ut-docs#1897 independent review); Categories/Modifiers/Option sets
// point at their real, shipped screens (ut-docs#1898/#1899/#1900).
var Sections = []Section{
	{NameKey: "nav.catalog", SubtitleKey: "items.library.subtitle", Href: "/catalog"},
	{NameKey: "items.categories.name", SubtitleKey: "items.categories.subtitle", Href: "/categories"},
	{NameKey: "items.inventory.name", SubtitleKey: "items.inventory.subtitle", Href: "/inventory"},
	{NameKey: "items.modifiers.name", SubtitleKey: "items.modifiers.subtitle", Href: "/modifiers"},
	{NameKey: "items.option_sets.name", SubtitleKey: "items.option_sets.subtitle", Href: "/catalog/option-sets"},
}
