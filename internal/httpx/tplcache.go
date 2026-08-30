package httpx

import (
	"html/template"
	"sync"

	uiassets "github.com/universaltill/universal-till/web"
)

// tplCache holds one parsed *template.Template per distinct named file set
// (ut-docs#1320): before this, every handler and internal/ui view
// constructor called template.New(...).ParseFS(...) fresh on every single
// request — a sale-screen load alone triggered 4+ independent full parses,
// and 2-3 more per subsequent tap during checkout (nine separate
// ui.NewBasketView call sites in pos_api.go). Lexing that file set (turning
// HTML/template source text into a parse tree) is a fixed cost this removes
// entirely for every call after the first. html/template's contextual
// auto-escaping analysis is NOT eliminated by this — it re-runs on every
// Clone() (see ClonedTemplate below), since Clone deep-copies the parse
// trees into a fresh, unescaped namespace — but independent review
// (ut-docs#1320) measured the combined clone+escape+execute path at ~37%
// faster than the original parse+escape+execute path on the basket render,
// which is the actual win this fix delivers; see BenchmarkRender_* in
// tplcache_test.go for the execute-inclusive numbers, not just the parse.
var (
	tplCacheMu sync.Mutex
	tplCache   = map[string]*template.Template{}
)

// cachedTemplate parses the given base FS-rooted file set exactly once per
// distinct key and returns the same cached *template.Template on every
// later call with that key. rootName is the name of the template Execute
// (as opposed to ExecuteTemplate) will run — callers must pass the same
// value every call for a given key, and the same set of func NAMES every
// call for a given key (a later call may rebind a name to a different
// closure, but must never omit a name a prior call included — Template.Funcs
// only overwrites entries present in the map passed to it, so a name
// missing from a later call silently keeps whichever closure last set it,
// rather than the loud parse-time "function not defined" error the old
// per-call ParseFS gave). In practice this always holds today: FuncsFor
// returns a complete, identical key set for every locale, and every call
// site either passes its FuncsFor(...) result unmodified or only
// overrides an existing key's value (e.g. catalog/handlers.go's per-request
// taxCodeName).
//
// The funcs passed here are used ONLY to satisfy html/template's parse-time
// "every function referenced by name must exist" check. html/template's
// contextual auto-escaping inspects a function's TYPE SIGNATURE, never its
// return value or closure state, so which concrete closures are bound at
// parse time has no effect on correctness.
//
// Deliberately unexported: the returned *Template is shared across every
// concurrent caller for this key, and html/template refuses to Clone a
// template that has ever been Executed — so a caller that executed this
// return value directly (instead of going through ClonedTemplate) would
// permanently break Clone for every other caller of the same key, for the
// life of the process (ut-docs#1320 review, finding 4). ClonedTemplate is
// the only sanctioned way to get a template out of this cache.
func cachedTemplate(key, rootName string, funcs template.FuncMap, files ...string) (*template.Template, error) {
	tplCacheMu.Lock()
	if t, ok := tplCache[key]; ok {
		tplCacheMu.Unlock()
		return t, nil
	}
	tplCacheMu.Unlock()

	// Parsed outside the lock: ParseFS only reads the embedded FS (no
	// shared mutable state), so two goroutines racing to prime the same
	// key just do redundant work once, never anything unsafe.
	t, err := template.New(rootName).Funcs(funcs).ParseFS(uiassets.FS, files...)
	if err != nil {
		return nil, err
	}

	tplCacheMu.Lock()
	defer tplCacheMu.Unlock()
	if existing, ok := tplCache[key]; ok {
		// Lost the race — keep one canonical *Template per key so every
		// caller ends up Clone()-ing the same underlying parse tree.
		return existing, nil
	}
	tplCache[key] = t
	return t, nil
}

// ClonedTemplate is the render call sites' entry point: get the cached,
// parsed-once base template for key (parsing it on first use), then return
// an independent clone with funcs bound as the real, live function map —
// safe to Execute/ExecuteTemplate immediately, safe to call concurrently
// from multiple goroutines/requests (each gets its own clone — verified
// under -race with distinct per-goroutine funcs, ut-docs#1320 review), and
// correct across locales (funcs is applied fresh to each clone, never
// baked into the shared cached copy, and Funcs() is called AFTER Clone(),
// never before — calling it before would bind the closure onto the shared
// base instead, corrupting every other caller of this key; see the
// TestClonedTemplate_ConcurrentSafe doc comment for how that specific
// mistake is caught).
//
// Template.Clone must be called before the source template has ever been
// Executed (html/template returns an error otherwise) — cachedTemplate's
// only caller is this function, and this function never Executes the base
// template directly, so that always holds.
func ClonedTemplate(key, rootName string, funcs template.FuncMap, files ...string) (*template.Template, error) {
	base, err := cachedTemplate(key, rootName, funcs, files...)
	if err != nil {
		return nil, err
	}
	t, err := base.Clone()
	if err != nil {
		return nil, err
	}
	return t.Funcs(funcs), nil
}

// ResetCacheForTests clears the process-lifetime template cache. It exists
// ONLY for tests that need to force a fresh ParseFS on their next call —
// e.g. internal/ui/render_cwd_test.go's and internal/pages/receipt_test.go's
// "works from any working directory" guards, which exist because a shipped
// bug once made a template constructor read from a cwd-relative disk path
// instead of the embedded FS. Against a warm process-lifetime cache, only
// the very first test in a `go test` run to touch a given key still
// exercises the real parse — every later test silently gets a cache hit and
// stops testing anything, which is exactly the regression these guards
// exist to catch (ut-docs#1320 review, finding 1). Never call this from
// production code.
func ResetCacheForTests() {
	tplCacheMu.Lock()
	defer tplCacheMu.Unlock()
	tplCache = map[string]*template.Template{}
}
