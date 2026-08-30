package ui

import (
	"html/template"
	"io"

	"github.com/universaltill/universal-till/internal/httpx"
)

// HelpNavView renders the manual's topic-tree partial on its own, for the
// out-of-band swap that follows an htmx /help/{topic} response (ut-docs#351).
type HelpNavView struct {
	Tpl *template.Template
}

// NewHelpNavView's file set is fixed, so it's parsed once and cloned per
// call thereafter (ut-docs#1320) — see httpx.ClonedTemplate.
func NewHelpNavView(funcs template.FuncMap) (*HelpNavView, error) {
	t, err := httpx.ClonedTemplate("ui.HelpNavView", "base.html", funcs,
		"ui/partials/help_nav.html",
	)
	if err != nil {
		return nil, err
	}
	return &HelpNavView{Tpl: t}, nil
}

// Render executes the tree against data shaped like the "sections"/
// "currentID" keys renderHelpPage already builds — a map, not a typed
// struct, since that's the same shape the full-page and topic-fragment
// renders already share. Set data["OOB"] = true for the out-of-band swap.
func (v *HelpNavView) Render(w io.Writer, data map[string]any) error {
	return v.Tpl.ExecuteTemplate(w, "help_nav", data)
}
