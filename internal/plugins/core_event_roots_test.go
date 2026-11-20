package plugins

// TestCoreEventsHaveReservedRoots guards ut-docs#5768: every root of a core
// Publish/Ask event name (and every NewEventBus eventModes key) must be in
// coreEventRoots, else a plugin id could squat on a core event namespace.
// A go/ast scan of this package and ../pages; stdlib only.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// eventCallArity maps an event-raising method to its exact arg count and
// the index of the event-name argument.
var eventCallArity = map[string][2]int{
	"Publish": {3, 1}, "PublishAuthorize": {3, 1}, "Ask": {3, 1}, "AskFrom": {3, 1},
	"PublishAuthorizeWithID": {4, 2}, "AskPlugin": {4, 2},
}

// coreEventHit is one place a core event name is raised or registered.
type coreEventHit struct {
	name      string
	pos       token.Pos
	pkg       string // package of the file the hit is in
	encl      string // enclosing func/method name ("" at package level)
	fromModes bool   // an eventModes[...] registration, not a publish
}

// scanCoreEventCalls parses the non-test files of this package and ../pages.
// hits lists every core event name raised through a Publish/Ask call or
// registered in eventModes; called holds every function or method name
// that appears as a call target, so a Publish* helper nobody calls can be
// told apart from one that is used.
func scanCoreEventCalls(t *testing.T, fset *token.FileSet) (hits []coreEventHit, called map[string]bool) {
	t.Helper()
	type file struct {
		pkg string
		f   *ast.File
	}
	var files []file
	consts := map[string]map[string][]ast.Expr{}
	for _, dir := range []string{".", filepath.Join("..", "pages")} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			pkg := f.Name.Name
			files = append(files, file{pkg, f})
			if consts[pkg] == nil {
				consts[pkg] = map[string][]ast.Expr{}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				gd, ok := n.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					return true
				}
				for _, sp := range gd.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, id := range vs.Names {
						if i < len(vs.Values) {
							consts[pkg][id.Name] = append(consts[pkg][id.Name], vs.Values[i])
						}
					}
				}
				return true
			})
		}
	}

	called = map[string]bool{}
	for _, fl := range files {
		pkg := fl.pkg
		var resolve func(e ast.Expr, pkg string, depth int) []string
		resolve = func(e ast.Expr, pkg string, depth int) []string {
			if s, ok := strLit(e); ok {
				return []string{s}
			}
			if depth > 4 {
				return nil
			}
			var vals []ast.Expr
			switch x := e.(type) {
			case *ast.Ident:
				vals = consts[pkg][x.Name]
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "plugins" {
					pkg = "plugins"
					vals = consts["plugins"][x.Sel.Name]
				}
			}
			var out []string
			for _, v := range vals { // const chains, e.g. = plugins.FiscalSignAskEvent
				out = append(out, resolve(v, pkg, depth+1)...)
			}
			return out // variable, concatenation, plugin-owned: skipped
		}
		scan := func(root ast.Node, encl string) {
			ast.Inspect(root, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					switch fn := x.Fun.(type) {
					case *ast.Ident:
						called[fn.Name] = true
					case *ast.SelectorExpr:
						called[fn.Sel.Name] = true
						ar, ok := eventCallArity[fn.Sel.Name]
						if !ok || len(x.Args) != ar[0] {
							return true
						}
						for _, s := range resolve(x.Args[ar[1]], pkg, 0) {
							hits = append(hits, coreEventHit{name: s, pos: x.Args[ar[1]].Pos(), pkg: pkg, encl: encl})
						}
					}
				case *ast.IndexExpr:
					if sel, ok := x.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "eventModes" {
						if s, ok := strLit(x.Index); ok {
							hits = append(hits, coreEventHit{name: s, pos: x.Index.Pos(), pkg: pkg, encl: encl, fromModes: true})
						}
					}
				}
				return true
			})
		}
		for _, d := range fl.f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				scan(fd, fd.Name.Name)
			} else {
				scan(d, "")
			}
		}
	}
	return hits, called
}

func TestCoreEventsHaveReservedRoots(t *testing.T) {
	fset := token.NewFileSet()
	hits, _ := scanCoreEventCalls(t, fset)

	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.name] = true
		root, _, found := strings.Cut(h.name, ".")
		if found && !coreEventRoots[root] {
			t.Errorf("%s: core event %q has root %q not in coreEventRoots", fset.Position(h.pos), h.name, root)
		}
	}
	var must []string
	for _, w := range []string{"sale.completed", "shift.opened", "charge.policy.ask", "agent.action.result", "fiscal.sign.ask", "export.requested.ask"} {
		if !seen[w] {
			must = append(must, w)
		}
	}
	sort.Strings(must)
	if len(must) > 0 {
		t.Fatalf("scanner no longer finds known core events %v (scan rotted)", must)
	}
}

// unpublishedModeKeys returns, sorted, the eventModes keys with no publish
// site in published.
func unpublishedModeKeys(modes map[string]EventDispatchMode, published map[string]bool) []string {
	var out []string
	for k := range modes {
		if !published[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestEventModesKeysArePublished guards ut-docs#5770: every key core
// registers in NewEventBus has a non-test publish site, so no plugin author
// can subscribe to a name that never fires. A publish site is a Publish/Ask
// call with that (const-resolved) name in internal/pages, or in
// internal/plugins inside a function that something calls (so an unused
// PublishX helper does not count). Limits: only internal/plugins and
// internal/pages are scanned (every core publisher lives there today), and
// helper indirection is followed one level. A publisher in another package
// means adding its directory to scanCoreEventCalls.
func TestEventModesKeysArePublished(t *testing.T) {
	hits, called := scanCoreEventCalls(t, token.NewFileSet())
	published := map[string]bool{}
	for _, h := range hits {
		if h.fromModes {
			continue
		}
		if h.pkg != "plugins" || called[h.encl] {
			published[h.name] = true
		}
	}
	modes := NewEventBus(nil).eventModes
	for _, k := range unpublishedModeKeys(modes, published) {
		t.Errorf("eventModes key %q has no non-test publish site in internal/plugins or internal/pages: publish it or delete the key (ut-docs#5770)", k)
	}
	for k := range modes {
		if _, ok := EventDataClasses(k); !ok {
			t.Errorf("eventModes key %q has no EventDataClasses entry: ai.agent plugins would never receive it", k)
		}
	}
}

func TestUnpublishedModeKeys(t *testing.T) {
	modes := map[string]EventDispatchMode{"a.b": NonBlocking, "x.never": NonBlocking, "c.d": NonBlocking}
	got := unpublishedModeKeys(modes, map[string]bool{"a.b": true, "c.d": true})
	if len(got) != 1 || got[0] != "x.never" {
		t.Fatalf("got %v, want [x.never]", got)
	}
	if got := unpublishedModeKeys(modes, map[string]bool{"a.b": true, "c.d": true, "x.never": true}); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func strLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	return s, err == nil
}
