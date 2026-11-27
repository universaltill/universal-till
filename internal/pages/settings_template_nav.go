package pages

import (
	"context"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/settingsnav"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/uislot"
)

// settingsTemplateResolved returns the shop-type template in effect for
// this /settings render (ADR-0190, ut-docs#6332): nil when the type is
// unset or unknown, or the shop is in legacy mode (a type set before
// templates existed, no offer accepted yet). Nil makes the page render
// exactly as it did before templates. It is a seam so the settings
// demotion (this file) and the template state (common.TemplateState, the
// menu slice) land independently; the menu slice wires it. Never
// overwritten outside tests and that wiring.
var settingsTemplateResolved = func(ctx context.Context, d *common.Deps) *shoptemplate.Resolved {
	return d.TemplateState(ctx).Effective()
}

// settingsTemplateClassFunc adapts the resolved template to the nav
// resolver; nil (no template) keeps every row at its base class.
func settingsTemplateClassFunc(r *shoptemplate.Resolved) settingsnav.ClassFunc {
	if r == nil {
		return nil
	}
	return func(key string, base uislot.Class) uislot.Class {
		return r.ClassFor(shoptemplate.KindSettings, key, base)
	}
}

// settingsTemplateView is what settings.html needs beyond the nav rows:
// the landing footer ("Set up for: {type}" + Change + search hint) and the
// More settings intro line. Zero value renders nothing.
type settingsTemplateView struct {
	Footer      string // "" = no footer line (type unset/unknown or not a manager)
	TagIntro    string // More settings intro; "" unless a template is in effect
	TagTucked   string // texts the page script puts on cards / search chips
	TagRec      string
	ChipText    string
	MissingText string
	MissingLink string
	Active      bool
}

func settingsTemplateViewFor(locale, shopType string, isManager, active bool) settingsTemplateView {
	var v settingsTemplateView
	if isManager && plugins.IsKnownShopType(shopType) {
		label := httpx.T(locale, "setup.shop_type."+shopType)
		v.Footer = strings.ReplaceAll(httpx.T(locale, "settings.template.footer_set_up_for"), "{type}", label)
	}
	if !active {
		return v
	}
	v.Active = true
	v.TagTucked = httpx.T(locale, "settings.template.tucked_away")
	v.TagRec = httpx.T(locale, "settings.template.recommended")
	v.ChipText = httpx.T(locale, "settings.template.in_more_settings")
	v.MissingText = httpx.T(locale, "settings.template.something_missing")
	v.MissingLink = httpx.T(locale, "settings.template.something_missing_link")
	v.TagIntro = strings.ReplaceAll(httpx.T(locale, "settings.template.more_intro"), "{tag}", v.TagTucked)
	return v
}
