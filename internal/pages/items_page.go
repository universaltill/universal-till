package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// itemsSection is one row of the /items section list — the "Items" area's
// SumUp-style landing page (ut-docs#1897): a section list where each entry
// is a bold name plus a one-line subtitle, replacing the flat "Catalog"
// nav tile. Href empty means the section has no screen yet (Categories and
// Modifiers — ut-docs#1898/#1899, tracked separately, deliberately NOT
// built by this card) and renders as a disabled "coming soon" row instead
// of a dead link.
type itemsSection struct {
	NameKey, SubtitleKey string
	Href                 string // empty = not yet available
}

// itemsSections is the fixed section list. Option sets links to /catalog
// today (the per-item variants panel lives there) because the reusable,
// named-set generator itself (ut-docs#1900) hasn't shipped — it is a real,
// reachable feature today, unlike Categories/Modifiers, so it is NOT
// disabled.
var itemsSections = []itemsSection{
	{NameKey: "items.library.name", SubtitleKey: "items.library.subtitle", Href: "/catalog"},
	{NameKey: "items.categories.name", SubtitleKey: "items.categories.subtitle", Href: ""},
	{NameKey: "items.inventory.name", SubtitleKey: "items.inventory.subtitle", Href: "/inventory"},
	{NameKey: "items.modifiers.name", SubtitleKey: "items.modifiers.subtitle", Href: ""},
	{NameKey: "items.option_sets.name", SubtitleKey: "items.option_sets.subtitle", Href: "/catalog"},
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
