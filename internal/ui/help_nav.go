package ui

import (
	"html/template"
	"io"

	uiassets "github.com/universaltill/universal-till/web"
)

// HelpNavView renders the manual's topic-tree partial on its own, for the
// out-of-band swap that follows an htmx /help/{topic} response (ut-docs#351).
type HelpNavView struct {
	Tpl *template.Template
}

func NewHelpNavView(funcs template.FuncMap) (*HelpNavView, error) {
	t := template.Must(template.New("base.html").Funcs(funcs).ParseFS(uiassets.FS,
		"ui/partials/help_nav.html",
	))
	return &HelpNavView{Tpl: t}, nil
}

// Render executes the tree against data shaped like the "sections"/
// "currentID" keys renderHelpPage already builds — a map, not a typed
// struct, since that's the same shape the full-page and topic-fragment
// renders already share. Set data["OOB"] = true for the out-of-band swap.
func (v *HelpNavView) Render(w io.Writer, data map[string]any) error {
	return v.Tpl.ExecuteTemplate(w, "help_nav", data)
}
