package ui

import (
	"html/template"
	"io"

	"github.com/universaltill/universal-till/internal/data"
	uiassets "github.com/universaltill/universal-till/web"
)

type JournalView struct {
	Tpl *template.Template
}

type JournalViewData struct {
	Entries []data.SaleJournalEntry
	OOB     bool
}

func NewJournalView(funcs template.FuncMap) (*JournalView, error) {
	t := template.Must(template.New("base.html").Funcs(funcs).ParseFS(uiassets.FS,
		"ui/partials/journal.html",
	))
	return &JournalView{Tpl: t}, nil
}

func (v *JournalView) Render(w io.Writer, data JournalViewData) error {
	return v.Tpl.ExecuteTemplate(w, "journal", data)
}
