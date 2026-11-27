package settingsnav

import "github.com/universaltill/universal-till/internal/uislot"

// Resolve is ResolveClassed with no template (the till's pre-template output).
// Test-only: production always resolves with a class func.
func Resolve(locale string, amendments []uislot.Amendment) []Row {
	return ResolveClassed(locale, amendments, nil)
}
