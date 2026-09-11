package ui

import (
	"html/template"
	"io"

	"github.com/universaltill/universal-till/internal/httpx"
)

// AdminTreeView renders the /admin left-rail partial (web/ui/partials/
// admin_tree.html) on its own, for the out-of-band swap that follows an
// htmx fragment response from any of the tree's six destinations
// (ut-docs#2116, mirroring /items' own two-pane shell, ut-docs#1950).
// Mirrors ItemsRailView (items_rail.go) exactly.
type AdminTreeView struct {
	Tpl *template.Template
}

// NewAdminTreeView's file set is fixed, so it's parsed once and cloned per
// call thereafter (ut-docs#1320) — see httpx.ClonedTemplate.
func NewAdminTreeView(funcs template.FuncMap) (*AdminTreeView, error) {
	t, err := httpx.ClonedTemplate("ui.AdminTreeView", "base.html", funcs,
		"ui/partials/admin_tree.html",
	)
	if err != nil {
		return nil, err
	}
	return &AdminTreeView{Tpl: t}, nil
}

// Render executes the tree against data shaped like {"Groups": []adminGroup,
// "CurrentHref": "/locations"} — a map, not a typed struct, same reasoning
// as ItemsRailView.Render. Set data["OOB"] = true for the out-of-band swap.
func (v *AdminTreeView) Render(w io.Writer, data map[string]any) error {
	return v.Tpl.ExecuteTemplate(w, "admin_tree", data)
}
