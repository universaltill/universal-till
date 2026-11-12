package httpx

import (
	"net/http"
	"sync/atomic"
)

// windowToggleSource answers "does this request's page carry the F11
// window-toggle flag" (ut-docs#3735): wired by pages.Init; nil (tests, a
// boot before Init) = never. A live per-request check, not a cached
// setting.
var windowToggleSource atomic.Value // func(*http.Request) bool

// InitWindowToggle wires the F11 window-toggle check; nil turns it off.
func InitWindowToggle(check func(*http.Request) bool) {
	windowToggleSource.Store(check)
}

// WindowToggleOn reports whether r's page renders <body data-window-toggle>.
func WindowToggleOn(r *http.Request) bool {
	check, _ := windowToggleSource.Load().(func(*http.Request) bool)
	return check != nil && r != nil && check(r)
}
