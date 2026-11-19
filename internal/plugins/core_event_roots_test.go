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

func TestCoreEventsHaveReservedRoots(t *testing.T) {
	fset := token.NewFileSet()
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

	type hit struct {
		name string
		pos  token.Pos
	}
	var hits []hit
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
		ast.Inspect(fl.f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ar, ok := eventCallArity[sel.Sel.Name]
				if !ok || len(x.Args) != ar[0] {
					return true
				}
				for _, s := range resolve(x.Args[ar[1]], pkg, 0) {
					hits = append(hits, hit{s, x.Args[ar[1]].Pos()})
				}
			case *ast.IndexExpr:
				if sel, ok := x.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "eventModes" {
					if s, ok := strLit(x.Index); ok {
						hits = append(hits, hit{s, x.Index.Pos()})
					}
				}
			}
			return true
		})
	}

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

func strLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	return s, err == nil
}
