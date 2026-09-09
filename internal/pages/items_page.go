package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// itemsSection is one row of the /items section list — the "Items" area's
// SumUp-style landing page (ut-docs#1897): a section list where each entry
// is a bold name plus a one-line subtitle, replacing the flat "Catalog"
// nav tile. Href empty means the section has no screen yet and renders as
// a disabled "coming soon" row instead of a dead link — Categories
// (ut-docs#1898) and Modifiers (ut-docs#1899) both started out that way
// and were wired up once their own screens shipped; no row is currently
// disabled, but the mechanism stays for the next section that lands
// before its own screen does.
type itemsSection struct {
	NameKey, SubtitleKey string
	Href                 string // empty = not yet available
}

// itemsSections is the fixed section list. Library reuses nav.catalog (the
// same key /catalog's own <h1> renders) rather than a separate "Library"
// name — independent review (ut-docs#1897) caught that two different
// English names for the same destination is exactly the disorientation
// this card exists to fix. Option sets pointed at /catalog while the
// reusable, named-set generator hadn't shipped, with a subtitle that said
// so honestly rather than implying a dedicated screen that didn't exist;
// ut-docs#1900 landed that screen, so this row now points at its real
// destination (/catalog/option-sets) and its subtitle describes the
// feature — same wiring-up, in the same merge that lands the screen, that
// Categories and Modifiers got below. Categories links to /categories as
// of ut-docs#1898 — this section list is the ONLY navigation to that page
// (it gets no top-level nav tile of its own, same as /catalog), so
// leaving it disabled here would make the screen reachable by typed URL
// only. Modifiers points at /modifiers (ut-docs#1899) — this row was
// originally left disabled ("coming soon") because that screen didn't
// exist yet when #1897 shipped; wired up in the same merge that landed it.
var itemsSections = []itemsSection{
	{NameKey: "nav.catalog", SubtitleKey: "items.library.subtitle", Href: "/catalog"},
	{NameKey: "items.categories.name", SubtitleKey: "items.categories.subtitle", Href: "/categories"},
	{NameKey: "items.inventory.name", SubtitleKey: "items.inventory.subtitle", Href: "/inventory"},
	{NameKey: "items.modifiers.name", SubtitleKey: "items.modifiers.subtitle", Href: "/modifiers"},
	{NameKey: "items.option_sets.name", SubtitleKey: "items.option_sets.subtitle", Href: "/catalog/option-sets"},
}

// registerItemsPage wires GET /items — the section-list landing page the
// top-level "Items" nav tile now opens (replacing the old flat "Catalog"
// tile and the standalone "Inventory" tile, both folded in as sections
// here). Static content, no DB access.
func registerItemsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/items", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"title":     "Items",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Sections":  itemsSections,
		}
		httpx.Render("ui/pages/items.html", data)(w, r)
	})
}
