package ui

import (
	"html/template"
	"io"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ItemsRailView renders the /items left-rail partial on its own, for the
// out-of-band swap that follows an htmx fragment response from ANY of the
// rail's five section destinations (ut-docs#1950): /catalog, /modifiers,
// /catalog/option-sets (internal/pages/catalog), /categories and /inventory
// (internal/pages). Mirrors HelpNavView/help_nav.html's identical role for
// /help/{topic} (ut-docs#351) exactly.
type ItemsRailView struct {
	Tpl *template.Template
}

// NewItemsRailView's file set is fixed, so it's parsed once and cloned per
// call thereafter (ut-docs#1320) — see httpx.ClonedTemplate.
func NewItemsRailView(funcs template.FuncMap) (*ItemsRailView, error) {
	t, err := httpx.ClonedTemplate("ui.ItemsRailView", "base.html", funcs,
		"ui/partials/items_rail.html",
	)
	if err != nil {
		return nil, err
	}
	return &ItemsRailView{Tpl: t}, nil
}

// Render executes the rail against data shaped like {"Sections":
// itemsnav.Sections, "CurrentHref": "/catalog"} — a map, not a typed
// struct, same reasoning as HelpNavView.Render. Set data["OOB"] = true for
// the out-of-band swap.
func (v *ItemsRailView) Render(w io.Writer, data map[string]any) error {
	return v.Tpl.ExecuteTemplate(w, "items_rail", data)
}
