package manual

import (
	"log"
	"sync"

	uiassets "github.com/universaltill/universal-till/web"
)

var (
	builtinOnce sync.Once
	builtin     *Library
)

// Builtin is the manual embedded in this binary (web/help/**). Parsed once —
// it is read-only content, and re-rendering ~17 topics × 4 locales per request
// would be pointless work on a Pi.
//
// It returns nil only if the embedded tree is unloadable, which is a build-time
// mistake rather than a runtime condition; callers treat nil as "no help
// available" and carry on, because a broken manual must never take the till
// down mid-sale.
func Builtin() *Library {
	builtinOnce.Do(func() {
		lib, err := Load(uiassets.HelpFS, "help")
		if err != nil {
			log.Printf("manual: embedded help unavailable: %v", err)
			return
		}
		builtin = lib
	})
	return builtin
}

// HelpHref is what the contextual "?" points at for a given page route: the
// topic documenting that page, or the manual's index when nothing claims it
// yet. Never returns empty — a "?" that goes nowhere is worse than one that
// lands on the contents page.
func HelpHref(route string) string {
	lib := Builtin()
	if lib == nil {
		return "/help"
	}
	if id, ok := lib.TopicForRoute(route); ok {
		return "/help/" + id
	}
	return "/help"
}
