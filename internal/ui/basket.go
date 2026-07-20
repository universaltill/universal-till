package ui

import (
	"html/template"
	"io"

	uiassets "github.com/universaltill/universal-till/web"
)

type BasketView struct {
	Tpl *template.Template
}

func NewBasketView(funcs template.FuncMap) (*BasketView, error) {
	t := template.Must(template.New("base.html").Funcs(funcs).ParseFS(uiassets.FS,
		"ui/layouts/base.html",
		"ui/partials/basket.html",
		"ui/partials/nav.html",
	))
	return &BasketView{Tpl: t}, nil
}

func (v *BasketView) Render(w io.Writer, basket any) error {
	// Render only the "basket" template (fragment); we don’t need the full layout here.
	return v.Tpl.ExecuteTemplate(w, "basket", basket)
}
