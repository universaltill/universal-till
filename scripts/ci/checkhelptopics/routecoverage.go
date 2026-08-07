// Page-route coverage (ut-docs#326, closing ut-docs#365's gap): every
// user-facing GET page registered under internal/pages/** must be claimed by
// some manual topic's routes: front matter — exactly, or via a {param}
// pattern, matched by the same segment-wise rule the runtime "?" resolves
// with (manual.RouteMatches / Library.RouteCovered — shared, not a second
// copy that could drift).
//
// The registrations are read from source with go/parser + go/ast rather than
// a regex over text: mux.HandleFunc("<pattern>", …), with an optional
// leading HTTP-method token, is a call expression, and the AST doesn't care
// about line breaks, spacing, or strings that merely look like routes in
// comments.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// skippedPrefixes is the denylist of route namespaces this check deliberately
// does NOT require a manual topic for. Each entry carries its reason so the
// list fails loudly on growing silently (the guard's own doc-comment
// philosophy): adding a prefix here is a documented decision, not a quiet
// escape hatch. An entry ending in "/" is a pure prefix; any other entry
// matches itself exactly or as a path segment prefix ("/help" covers "/help"
// and "/help/{topic}", not "/helpless-page").
var skippedPrefixes = []struct{ prefix, why string }{
	{"/api/", "JSON/data endpoints, not pages an operator lands on"},
	{"/ui/", "htmx fragments swapped INTO pages — the enclosing page's topic covers them"},
	{"/ext/", "external device surfaces (customer display, …), not operator pages"},
	{"/v1/", "marketplace install protocol stubs, machine-to-machine"},
	{"/plugin-icons/", "static icon assets"},
	{"/themes/", "static theme assets"},
	{"/help", "the manual's own self-referential routes"},
	{"/public/", "static file server"},
	{"/healthz", "liveness probe"},
	{"/plugin/", "plugin-owned dynamically-dispatched pages — each plugin ships its own content bundle, not this manual's job"},
	{"/self-order", "customer-facing kiosk screen (RenderPartial, no base layout/nav) — carries no operator \"?\" to link from; content lives in the self-order topic, reachable via manual search, not a page-level link (ut-docs#326 review)"},
}

func skipRoute(route string) bool {
	for _, s := range skippedPrefixes {
		if strings.HasSuffix(s.prefix, "/") {
			if strings.HasPrefix(route, s.prefix) {
				return true
			}
			continue
		}
		if route == s.prefix || strings.HasPrefix(route, s.prefix+"/") {
			return true
		}
	}
	return false
}

// pageRoutes walks every non-test .go file under dir (recursively — /catalog
// is registered in the internal/pages/catalog subpackage, and a scan blind to
// subdirectories is exactly the silent gap this check exists to close) and
// collects the pattern of every <recv>.HandleFunc("<pattern>", …) /
// <recv>.Handle("<pattern>", …) call whose pattern is a string literal.
// Patterns registered for a non-GET method are dropped — an operator never
// lands on a POST — and a leading "GET " token is stripped. Returned unique
// and sorted.
func pageRoutes(dir string) ([]string, error) {
	seen := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if method, rest, found := strings.Cut(pattern, " "); found {
				if method != "GET" {
					return true // POST/PUT/… — not a page an operator lands on
				}
				pattern = rest
			}
			seen[pattern] = true
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}

// uncoveredRoutes is the pure comparison: which non-denylisted routes does no
// topic claim. covered is Library.RouteCovered in real use; a fake in tests.
func uncoveredRoutes(routes []string, covered func(string) bool) []string {
	var out []string
	for _, r := range routes {
		if skipRoute(r) || covered(r) {
			continue
		}
		out = append(out, r)
	}
	return out
}
