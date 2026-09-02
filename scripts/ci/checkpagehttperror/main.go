// Command checkpagehttperror is the Go half of guard-page-http-error.sh
// (ut-docs#1455): a handler registered for a user-facing PAGE route (a GET
// a browser/WebView actually navigates to, not an /api/ or /ui/ fragment
// endpoint) must never answer with a bare, full-document error body — on a
// pinned Android kiosk that replaces the WHOLE screen with plain text — no
// rail, no way back, and the kiosk hides Android's own navigation bar —
// which is exactly the incident #1455 reports live on the TECLAST tablet
// ("failed to load tables"). httpx.RenderError renders the same translated,
// full-layout error page (rail + a way back) that a repo error should
// always produce here instead.
//
// Two call shapes are flagged, not just http.Error: `common.LocalizedError`
// and `common.LogAndLocalizedError` (internal/pages/common) are ALSO
// `http.Error(w, translatedText, status)` under the hood — translating the
// text does not stop it from replacing the whole document with no rail,
// which is the actual defect this guard exists to catch (independent
// review finding on this guard's first version, which matched only the
// literal `http.Error` token and missed 28 real page-route sites using
// this wrapper instead — two of them one line from a site the first
// version DID migrate).
//
// The route classification (which patterns count as a "page" at all) is a
// deliberate, minimal copy of scripts/ci/checkhelptopics/routecoverage.go's
// skippedPrefixes/pageRoutes logic, not a shared import — routecoverage.go
// lives in another `package main` and isn't importable. Keep the two lists'
// *intent* in sync (a route namespace either serves an operator/customer
// page or it doesn't) if one changes; letting them diverge silently is the
// kind of drift this repo's own CLAUDE.md warns about elsewhere, but
// extracting a shared internal package is a separate refactor, not this
// card's job.
//
// Detection is AST-based (go/parser), not a text grep, for the same reason
// routecoverage.go is: a `mux.HandleFunc("<pattern>", handler)` call is
// unambiguous in the AST regardless of line breaks or a route pattern that
// merely LOOKS like one inside a comment or string. Handler resolution
// covers three shapes:
//   - an inline closure: `func(w http.ResponseWriter, r *http.Request) {...}`
//     — the common case, checked directly.
//   - a same-package factory call: `SomeHandler(deps)` where `SomeHandler`
//     is declared in the same file/package and returns `http.HandlerFunc`
//     via `return func(w, r) {...}` — resolved by finding that returned
//     closure and checking IT (e.g. plugins_store_page.go's
//     `PluginStoreHandler`).
//   - a call to a LOCAL closure declared earlier in the same enclosing
//     `registerXxx` function via `name := func(...) {...}` (e.g.
//     tables_page.go's `tiles`/`requireManager`, shared by more than one
//     route in that file) — resolved transitively: a handler body's call
//     to any such local closure pulls that closure's body into the same
//     scan, and so on, so a violation inside a two-deep helper chain is
//     still caught (independent review finding: the first version of this
//     guard could not see this shape at all — it is the EXACT shape
//     tables_page.go's own reported incident had, via its `tiles()`
//     helper, so this guard's first version could not have caught the very
//     bug it was written for without the manual, one-off migration this
//     card already did for that specific file).
//
// A genuine, reviewed exception (a page that intentionally skips the
// operator layout, e.g. the pre-enrollment setup wizard or the anonymous
// customer order-tracking page — see their own files' comments) is allowed
// via a same-line `// page-error:allow <reason>` comment, the same escape
// hatch guard-i18n.sh's `i18n:ignore` and guard-compliance-claims.sh's
// `compliance-claim:allow` already use.
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

// skippedPrefixes mirrors routecoverage.go's non-page namespaces — see this
// file's own doc comment above for why it's a copy, not a shared import.
var skippedPrefixes = []string{
	"/api/", "/ui/", "/ext/", "/v1/", "/plugin-icons/", "/themes/",
	"/help", "/public/", "/healthz", "/plugin/", "/self-order",
}

func skipRoute(route string) bool {
	for _, p := range skippedPrefixes {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(route, p) {
				return true
			}
			continue
		}
		if route == p || strings.HasPrefix(route, p+"/") {
			return true
		}
	}
	return false
}

// allowMarker is the escape hatch: a violation on a source line carrying
// this comment (anywhere on the line — before or after the call) is
// deliberate and not reported.
const allowMarker = "page-error:allow"

type violation struct {
	file, route string
	line        int
}

func main() {
	dir := "internal/pages"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	violations, err := findViolations(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "guard-page-http-error: %v\n", err)
		os.Exit(1)
	}
	if len(violations) == 0 {
		fmt.Println("guard-page-http-error: OK — no bare http.Error/LocalizedError/LogAndLocalizedError in a page-route handler (inline, factory-call, or local-closure-indirected)")
		return
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})
	fmt.Fprintln(os.Stderr, "guard-page-http-error: bare http.Error/common.LocalizedError/common.LogAndLocalizedError(...) in a page-route handler — every one of these writes a translated-or-not, but still bare, full-document body with no rail. Use httpx.RenderError instead (see internal/httpx/render_error.go), or mark a reviewed exception with a same-line \"// page-error:allow <reason>\" comment:")
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s:%d  route=%s\n", v.file, v.line, v.route)
	}
	os.Exit(1)
}

// bareErrorCall reports whether call is one of the flagged shapes: a bare
// http.Error, or internal/pages/common's LocalizedError/
// LogAndLocalizedError — both of the latter are ALSO a raw
// http.Error(w, translatedText, status) under the hood, so translating the
// text doesn't stop it replacing the whole document with no rail. See this
// file's own doc comment.
func bareErrorCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	xid, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch {
	case xid.Name == "http" && sel.Sel.Name == "Error":
		return true
	case xid.Name == "common" && (sel.Sel.Name == "LocalizedError" || sel.Sel.Name == "LogAndLocalizedError"):
		return true
	default:
		return false
	}
}

// callTarget returns the identifier name of a direct `name(...)` call
// expression (not a method/selector call), or "" if call isn't one.
func callTarget(call *ast.CallExpr) string {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

func findViolations(dir string) ([]violation, error) {
	fset := token.NewFileSet()
	var violations []violation

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		lines, err := sourceLines(path)
		if err != nil {
			return err
		}

		// Index top-level named funcs in this file, for resolving a
		// factory-call handler (see doc comment above).
		named := map[string]*ast.FuncDecl{}
		for _, decl := range file.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil {
				named[fd.Name.Name] = fd
			}
		}

		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			// Local closures declared in THIS enclosing function via
			// `name := func(...) {...}` — e.g. tables_page.go's
			// `tiles`/`requireManager`, shared by more than one route
			// registered in the same registerXxx function.
			locals := localClosures(fd)

			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
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
				method, route := "", pattern
				if idx := strings.IndexByte(pattern, ' '); idx > 0 {
					method, route = pattern[:idx], pattern[idx+1:]
				}
				if method != "" && method != "GET" {
					return true
				}
				if skipRoute(route) {
					return true
				}

				body := handlerBody(call.Args[1], named)
				if body == nil {
					return true
				}

				// Scan the handler body itself, then transitively follow
				// any call to a local closure it reaches (bounded by
				// `visited` against a call cycle).
				visited := map[string]bool{}
				var scan func(n ast.Node)
				scan = func(n ast.Node) {
					ast.Inspect(n, func(n2 ast.Node) bool {
						call2, ok := n2.(*ast.CallExpr)
						if !ok {
							return true
						}
						if bareErrorCall(call2) {
							line := fset.Position(call2.Pos()).Line
							if line >= 1 && line <= len(lines) && strings.Contains(lines[line-1], allowMarker) {
								return true
							}
							violations = append(violations, violation{path, route, line})
							return true
						}
						if name := callTarget(call2); name != "" && !visited[name] {
							if closureBody, ok := locals[name]; ok {
								visited[name] = true
								scan(closureBody)
							}
						}
						return true
					})
				}
				scan(body)
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return violations, nil
}

// localClosures collects every `name := func(...) {...}` (or `var name = func...`)
// declared anywhere in fd's body — a flat scan, not scoped to a single
// block, since these are typically declared once near the top of a
// registerXxx function and called from several route closures further
// down the same function body.
func localClosures(fd *ast.FuncDecl) map[string]*ast.FuncLit {
	out := map[string]*ast.FuncLit{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if s.Tok != token.DEFINE || len(s.Lhs) != len(s.Rhs) {
				return true
			}
			for i, rhs := range s.Rhs {
				lit, ok := rhs.(*ast.FuncLit)
				if !ok {
					continue
				}
				if id, ok := s.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
					out[id.Name] = lit
				}
			}
		case *ast.GenDecl:
			if s.Tok != token.VAR {
				return true
			}
			for _, spec := range s.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}
				for i, val := range vs.Values {
					if lit, ok := val.(*ast.FuncLit); ok {
						out[vs.Names[i].Name] = lit
					}
				}
			}
		}
		return true
	})
	return out
}

// handlerBody resolves a mux.HandleFunc/Handle second argument to the
// function body to actually scan: a literal closure directly, or — for a
// `SomeHandler(args...)` factory call — the closure returned by that
// same-package function's own `return func(...) {...}` statement.
func handlerBody(arg ast.Expr, named map[string]*ast.FuncDecl) ast.Node {
	if lit, ok := arg.(*ast.FuncLit); ok {
		return lit.Body
	}
	call, ok := arg.(*ast.CallExpr)
	if !ok {
		return nil
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return nil
	}
	fd, ok := named[ident.Name]
	if !ok || fd.Body == nil {
		return nil
	}
	var found ast.Node
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		if lit, ok := ret.Results[0].(*ast.FuncLit); ok {
			found = lit.Body
			return false
		}
		return true
	})
	return found
}

func sourceLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(b), "\n"), nil
}
