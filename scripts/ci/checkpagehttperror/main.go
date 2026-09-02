// Command checkpagehttperror is the Go half of guard-page-http-error.sh
// (ut-docs#1455). A bare http.Error(...) call (or its
// internal/pages/common wrappers LocalizedError/LogAndLocalizedError, both
// http.Error under the hood — see internal/pages/common/errors.go) reached
// from inside a top-level page GET route's handler replaces the ENTIRE
// WebView document with a bare-text body: no nav rail, no way back on the
// pinned Android kiosk (no browser Back). Reported live against /tables
// (internal/pages/tables_page.go, ut-docs#1455's own repro). The fix is
// httpx.RenderError, which renders the full layout (nav rail included)
// instead — this guard is the mechanical backstop that stops a future page
// handler from silently reintroducing the bare-body pattern.
//
// Like scripts/ci/checkhelptopics/routecoverage.go, route registrations are
// read from source with go/parser + go/ast rather than a regex over text —
// the AST doesn't care about line breaks, spacing, or strings that merely
// look like a route/call in a comment.
//
// Scope, deliberately NOT expanded beyond what's needed (see each helper's
// own doc comment for the exact boundary): a "page route" is a
// mux.HandleFunc/mux.Handle registration whose pattern carries no method
// prefix other than an optional "GET ", and whose path isn't in the same
// non-page denylist scripts/ci/checkhelptopics/routecoverage.go already
// uses (duplicated here intentionally — see skippedPrefixes below). Only
// ONE level of indirection is followed: a local, `:=`-assigned function
// literal in the same enclosing function that the page-route handler calls
// by name (e.g. tables_page.go's `tiles := func(...) {...}`, called from
// the "GET /tables" handler). Deeper indirection (a call to a top-level
// func, a value passed as an argument, …) is out of scope — same
// "don't over-engineer, document the boundary" spirit as
// scripts/ci/guard-kiosk-engine.sh.
//
// Known, verified gap (independent review, ut-docs#1455): a handler
// registered as a call to an external func — `handler ast.Expr` that isn't
// a `*ast.FuncLit` — is invisible to this guard, since it only descends
// into an inline literal. TODAY that pattern is used by exactly ONE live
// page route: plugins_store_page.go's
// `mux.HandleFunc("/plugins/store", PluginStoreHandler(deps))`. The guard
// currently cannot protect that route from a regression back to a bare
// http.Error inside PluginStoreHandler — verified by temporarily
// reintroducing the pre-fix bare call there and confirming this guard,
// its unit tests, and its shell regression test all stayed green. Closing
// this (resolve a same-file top-level FuncDecl the handler expr calls, and
// scan the FuncLit it returns) is tracked on ut-docs#1458 alongside that
// card's other page-error migration work, rather than done here.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// skippedPrefixes is intentionally the SAME denylist
// scripts/ci/checkhelptopics/routecoverage.go defines (not imported — these
// are sibling `package main` programs under scripts/ci, so importing one
// from the other isn't straightforward, and copying a ~15-line, rarely
// changed list is simpler than factoring out a shared package for it). Keep
// the two in sync if either changes: both exist to identify the same
// "not a page an operator lands on" namespaces.
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
	{"/self-order", "customer-facing kiosk screen (RenderPartial, no base layout/nav) — carries no operator \"?\" to link from"},
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

// pageRoutePath returns (path, true) if pattern is a page-route pattern —
// no method prefix, or a "GET " prefix — and not denylisted; ("", false)
// otherwise (a non-GET method prefix, or a denylisted path).
func pageRoutePath(pattern string) (string, bool) {
	path := pattern
	if method, rest, found := strings.Cut(pattern, " "); found {
		if method != "GET" {
			return "", false
		}
		path = rest
	}
	if skipRoute(path) {
		return "", false
	}
	return path, true
}

// flaggedCall names of the three functions this guard treats as "a bare
// http.Error(...) reaching the operator's screen" — see this package's own
// doc comment.
type flaggedCall struct{ pkg, name string }

var flaggedCalls = []flaggedCall{
	{"http", "Error"},
	{"common", "LocalizedError"},
	{"common", "LogAndLocalizedError"},
}

func matchFlaggedCall(call *ast.CallExpr) (flaggedCall, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return flaggedCall{}, false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return flaggedCall{}, false
	}
	for _, fc := range flaggedCalls {
		if pkgIdent.Name == fc.pkg && sel.Sel.Name == fc.name {
			return fc, true
		}
	}
	return flaggedCall{}, false
}

// handlerEntry is one page-route registration's handler literal.
type handlerEntry struct {
	pattern string
	lit     *ast.FuncLit
}

// isRouteRegistration reports whether call is a <recv>.HandleFunc(pattern,
// handler) / <recv>.Handle(pattern, handler) call with a string-literal
// pattern, returning the unquoted pattern and the handler expression.
func isRouteRegistration(call *ast.CallExpr) (pattern string, handler ast.Expr, ok bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") || len(call.Args) < 2 {
		return "", nil, false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", nil, false
	}
	pattern, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", nil, false
	}
	return pattern, call.Args[1], true
}

// violation is one unallowed flagged call found inside a page-route
// handler (or a local closure it calls).
type violation struct {
	path, call, pattern string
	line                int
}

func (v violation) String() string {
	return fmt.Sprintf("%s:%d: %s(...) inside page-route handler for %q — use httpx.RenderError instead (or add // page-error:allow <reason>)",
		v.path, v.line, v.call, v.pattern)
}

// checkFile scans one non-test Go file for unallowed violations.
func checkFile(path string) ([]violation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	// Same-line "// page-error:allow <reason>" escape hatch — same
	// convention as i18n:ignore / kiosk-engine-guard:allow elsewhere in
	// this repo. Indexed by line number so a flagged call can check its own
	// line cheaply.
	allowedLines := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "page-error:allow") {
				allowedLines[fset.Position(c.Pos()).Line] = true
			}
		}
	}

	var violations []violation
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}

		// Local, `:=`-assigned function-literal closures declared anywhere
		// in this function's body — the one level of indirection a
		// page-route handler is allowed to reach through (e.g.
		// tables_page.go's `tiles := func(...) {...}`).
		localClosures := map[string]*ast.FuncLit{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			ident, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			if lit, ok := assign.Rhs[0].(*ast.FuncLit); ok {
				localClosures[ident.Name] = lit
			}
			return true
		})

		// Page-route handler registrations directly inside this function.
		var handlers []handlerEntry
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			pattern, handlerExpr, ok := isRouteRegistration(call)
			if !ok {
				return true
			}
			routePath, isPage := pageRoutePath(pattern)
			if !isPage {
				return true
			}
			lit, ok := handlerExpr.(*ast.FuncLit)
			if !ok {
				// Handler isn't an inline literal (e.g.
				// PluginStoreHandler(deps), a call to a function defined
				// elsewhere) — deeper than the one-level indirection this
				// guard follows; documented scope limit, not a bug.
				return true
			}
			handlers = append(handlers, handlerEntry{pattern: routePath, lit: lit})
			return true
		})

		for _, h := range handlers {
			targets := []*ast.FuncLit{h.lit}
			ast.Inspect(h.lit.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				if lit, ok := localClosures[ident.Name]; ok {
					targets = append(targets, lit)
				}
				return true
			})

			for _, target := range targets {
				ast.Inspect(target.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					fc, matched := matchFlaggedCall(call)
					if !matched {
						return true
					}
					line := fset.Position(call.Pos()).Line
					if allowedLines[line] {
						return true
					}
					violations = append(violations, violation{
						path:    path,
						call:    fc.pkg + "." + fc.name,
						pattern: h.pattern,
						line:    line,
					})
					return true
				})
			}
		}
	}
	return violations, nil
}

// scanDir walks dir recursively (internal/pages/catalog is a subpackage,
// same reasoning as routecoverage.go's own walk), skipping _test.go files.
func scanDir(dir string) ([]violation, error) {
	var all []violation
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		vs, err := checkFile(path)
		if err != nil {
			return err
		}
		all = append(all, vs...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].path != all[j].path {
			return all[i].path < all[j].path
		}
		return all[i].line < all[j].line
	})
	return all, nil
}

func main() {
	violations, err := scanDir(filepath.Join("internal", "pages"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "guard-page-http-error: %v\n", err)
		os.Exit(1)
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, v.String())
		}
		fmt.Fprintf(os.Stderr, "guard-page-http-error: %d unallowed violation(s)\n", len(violations))
		os.Exit(1)
	}
	fmt.Println("✓ page-http-error guard: no page-route handler renders a bare http.Error body")
}
