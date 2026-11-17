package httpx

import (
	"html/template"
	"maps"
	"net/http"
	"sync/atomic"
)

// browserPrintSource answers "is the browser's print dialog useless on this
// request's screen" (ut-docs#6154): wired by pages.Init on the Pi kiosk
// appliance only; nil (tests, other tills) = never off. A live per-request
// check, not a cached setting.
var browserPrintSource atomic.Value // func(*http.Request) bool

// InitBrowserPrint wires the check; nil turns it off.
func InitBrowserPrint(off func(*http.Request) bool) {
	browserPrintSource.Store(off)
}

// BrowserPrintOff reports whether r's page must not open window.print().
func BrowserPrintOff(r *http.Request) bool {
	check, _ := browserPrintSource.Load().(func(*http.Request) bool)
	return check != nil && r != nil && check(r)
}

// WithBrowserPrint copies funcs and binds `browserprintoff` AND the viewer's
// `allowed` to r — for renders that don't pass through withHelpHref (the
// tender's receipt): there the request-less `allowed` fails closed for every
// viewer, which would hide the Settings link from managers.
func WithBrowserPrint(funcs template.FuncMap, r *http.Request) template.FuncMap {
	out := make(template.FuncMap, len(funcs)+2)
	maps.Copy(out, funcs)
	off := BrowserPrintOff(r)
	out["browserprintoff"] = func() bool { return off }
	out["allowed"] = allowedFunc(requestVisibility(r))
	return out
}
