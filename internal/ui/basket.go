package ui

import (
	"html/template"
	"net/http"
	"path/filepath"
)

type BasketView struct {
	Tpl *template.Template
}

func NewBasketView() (*BasketView, error) {
	funcs := template.FuncMap{
		"div100": func(cents int64) float64 { return float64(cents) / 100.0 },
	}
	t := template.Must(template.New("base.html").Funcs(funcs).ParseFiles(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "partials", "basket.html"),
		filepath.Join("web", "ui", "partials", "nav.html"),
	))
	return &BasketView{Tpl: t}, nil
}

func (v *BasketView) Render(w http.ResponseWriter, basket any) error {
	// Render only the "basket" template (fragment); we don’t need the full layout here.
	return v.Tpl.ExecuteTemplate(w, "basket", basket)
}
