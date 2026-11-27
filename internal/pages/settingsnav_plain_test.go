package pages

import (
	"github.com/universaltill/universal-till/internal/pages/settingsnav"
	"github.com/universaltill/universal-till/internal/uislot"
)

// settingsNavPlain is the sidebar resolved with no shop-type template.
func settingsNavPlain(locale string, amendments []uislot.Amendment) []settingsnav.Row {
	return settingsnav.ResolveClassed(locale, amendments, nil)
}
