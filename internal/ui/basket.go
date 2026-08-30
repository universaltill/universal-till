package ui

import (
	"html/template"
	"io"

	"github.com/universaltill/universal-till/internal/httpx"
)

type BasketView struct {
	Tpl *template.Template
}

// NewBasketView is on the checkout hot path — every add/void/tender/resume
// tap during a sale calls this (nine call sites in internal/pages/pos_api.go
// alone). The file set is fixed, so the parse itself only ever needs to
// happen once per process; ClonedTemplate does that and hands back a fresh,
// independently-executable copy bound to funcs every call (ut-docs#1320).
func NewBasketView(funcs template.FuncMap) (*BasketView, error) {
	t, err := httpx.ClonedTemplate("ui.BasketView", "base.html", funcs,
		"ui/layouts/base.html",
		"ui/partials/basket.html",
		"ui/partials/nav.html",
	)
	if err != nil {
		return nil, err
	}
	return &BasketView{Tpl: t}, nil
}

func (v *BasketView) Render(w io.Writer, basket any) error {
	// Render only the "basket" template (fragment); we don’t need the full layout here.
	return v.Tpl.ExecuteTemplate(w, "basket", basket)
}
