// Command coreneutral is the Go half of guard-core-neutral.sh
// (ut-docs#2888; owner rule 2026-09-25, #2848; mechanism ADR-0119):
// core Go under internal/, cmd/ and mobile/ must not select behaviour by a specific
// country, vendor or first-party plugin. It parses every non-test file with
// go/parser, so comments and string literals are the parser's problem, not
// a text heuristic's (the first, awk-based version was blinded by a "/*"
// inside a string).
//
// Flagged (a finding is keyed path|enclosing func|literal value):
//
//   - Country codes — a curated list of markets we touch (countryCodes).
//     Uppercase codes ("DE") and xx-XX / xx_XX locale tags always count; a
//     lowercase/mixed bare code ("de") counts only when the other operand,
//     switch tag, indexed map (or its index expressions) or sibling call
//     argument names a country-ish variable (contextName: country, region,
//     market, nation as substrings; cc, iso as whole camel/snake tokens) —
//     "de" is otherwise a language subtag or a JSON key. Only in a
//     position that TESTS or SELECTS by the code: an operand of == / !=;
//     any value of a switch case list; a literal switch tag; an argument
//     (or an element of a composite-literal slice argument) of
//     strings.EqualFold/HasPrefix/HasSuffix/Contains/Index or
//     slices.Contains/Index; a literal index m["DE"]; a key of a map
//     literal that is a set (value type bool or struct{}) or that is bound
//     to a name used somewhere in the package as name[expr] (a
//     per-country behaviour table). Map literals of a named map type
//     (type set map[string]bool) resolve through the package's type decl.
//   - Constants: an identifier in a testing position bound to a string
//     literal — a const (package- or function-level) or a var/:= that is
//     never reassigned — is judged by that literal (the const's own name
//     counts as context). The finding is keyed on the DECLARATION
//     (path|func-or-<file>|literal), so one allow-list entry or a marker on
//     the const's line covers every use.
//   - Vendor names (vendorTokens) — any string literal (struct tags and
//     import paths included) containing the vendor as a token, splitting
//     on non-alphanumerics, so "api.sumup.com" and `json:"stripe_id"`
//     count. Identifiers are never scanned (func square is fine).
//   - Plugin IDs — any string literal containing com.universaltill.<slug>
//     or ut-plugin-<slug>. The bare "com.universaltill." prefix (a generic
//     first-party trust check) is not a specific plugin and is not flagged.
//
// Not flagged: codes as data VALUES (struct fields, map values,
// assignments, plain call args), map literals never indexed by a runtime
// value, and name tables — map[string]string whose every value is a
// string literal longer than three characters ({"DE": "Germany"}).
//
// Keys are per enclosing func, not per line: a second occurrence of an
// already allow-listed literal inside the same func is covered by the same
// entry — by design, so entries survive edits; the owning card removes
// the whole func's dependency at once.
//
// Exceptions: a same-line `// core-neutral:allow <reason>` comment (a real
// comment from the AST — the marker inside a string does not count), or an
// allow-list entry (see parseAllowlist). The allow-list may only shrink:
// stale entries fail, and when a base-branch copy is supplied, entries not
// in it fail.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var countryCodes = map[string]bool{
	"DE": true, "AT": true, "CH": true, "GB": true, "UK": true, "TR": true,
	"PT": true, "ES": true, "FR": true, "IT": true, "NL": true, "IR": true,
	"AE": true, "SA": true,
}

var vendorTokens = map[string]bool{
	"fiskaly": true, "sumup": true, "stripe": true, "zettle": true,
	"square": true, "paypal": true, "qrpay": true, "adyen": true,
	"ready2order": true, "lightspeed": true, "loyverse": true,
	"speedykasse": true, // also matched as the token pair speedy+kasse
}

var (
	localeRe   = regexp.MustCompile(`^[A-Za-z]{2}[-_]([A-Za-z]{2})$`)
	pluginIDRe = regexp.MustCompile(`(?i)(com\.universaltill\.|ut-plugin-)[a-z0-9]`)
	nonAlnumRe = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

const marker = "core-neutral:allow"

// Finding is one flagged literal occurrence.
type Finding struct {
	Path, Func, Literal, Kind string
	Line                      int
}

// Key is the allow-list key: stable across line moves.
func (f Finding) Key() string { return f.Path + "|" + f.Func + "|" + f.Literal }

func isVendor(v string) bool {
	toks := nonAlnumRe.Split(strings.ToLower(v), -1)
	for i, t := range toks {
		if vendorTokens[t] {
			return true
		}
		if t == "speedy" && i+1 < len(toks) && toks[i+1] == "kasse" {
			return true
		}
	}
	return false
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

func strLit(e ast.Expr) *ast.BasicLit {
	if bl, ok := unparen(e).(*ast.BasicLit); ok && bl.Kind == token.STRING {
		return bl
	}
	return nil
}

var testingCalls = map[string]map[string]bool{
	"strings": {"EqualFold": true, "HasPrefix": true, "HasSuffix": true, "Contains": true, "Index": true},
	"slices":  {"Contains": true, "Index": true},
}

func isSetType(t ast.Expr) bool {
	switch v := t.(type) {
	case *ast.Ident:
		return v.Name == "bool"
	case *ast.StructType:
		return v.Fields == nil || len(v.Fields.List) == 0
	}
	return false
}

// isNameTable: map[string]string whose values are all display strings.
func isNameTable(mt *ast.MapType, cl *ast.CompositeLit) bool {
	if id, ok := mt.Value.(*ast.Ident); !ok || id.Name != "string" {
		return false
	}
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return false
		}
		bl := strLit(kv.Value)
		if bl == nil {
			return false
		}
		v, err := strconv.Unquote(bl.Value)
		if err != nil || len([]rune(v)) <= 3 {
			return false
		}
	}
	return true
}

func funcName(d ast.Decl) string {
	fd, ok := d.(*ast.FuncDecl)
	if !ok {
		return "<file>"
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	for {
		switch v := t.(type) {
		case *ast.StarExpr:
			t = v.X
			continue
		case *ast.IndexExpr:
			t = v.X
			continue
		case *ast.IndexListExpr:
			t = v.X
			continue
		}
		break
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// binding is a const (or never-reassigned var) bound to a string literal.
type binding struct {
	name, path, fn, lit string
	line                int
}

// scope maps a name to its literal binding; a nil value means the name is
// defined more than once or reassigned, so it is not resolved.
type scope map[string]*binding

func (s scope) define(b *binding) {
	if _, dup := s[b.name]; dup {
		s[b.name] = nil
		return
	}
	s[b.name] = b
}

// collectBindings records string-literal consts/vars of one scope. For the
// package scope (fn == "<file>") root is a GenDecl; for a function it is
// the FuncDecl, and every define inside it (any value) is counted so a
// name defined twice is left unresolved.
func collectBindings(sc scope, fset *token.FileSet, path, fn string, root ast.Node) {
	add := func(id *ast.Ident, val ast.Expr) {
		if id.Name == "_" {
			return
		}
		bl := strLit(val)
		if bl == nil {
			sc[id.Name] = nil // defined, but not a literal: shadows any outer binding
			return
		}
		lit, err := strconv.Unquote(bl.Value)
		if err != nil {
			return
		}
		sc.define(&binding{name: id.Name, path: path, fn: fn, lit: lit, line: fset.Position(id.Pos()).Line})
	}
	ast.Inspect(root, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ValueSpec:
			for j, id := range v.Names {
				var val ast.Expr
				if j < len(v.Values) && len(v.Values) == len(v.Names) {
					val = v.Values[j]
				}
				add(id, val)
			}
		case *ast.AssignStmt:
			if v.Tok == token.DEFINE && len(v.Lhs) == len(v.Rhs) {
				for j, l := range v.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						add(id, v.Rhs[j])
					}
				}
			}
		case *ast.FuncLit:
			return fn != "<file>"
		}
		return true
	})
}

// contextRe / contextTokens decide whether a lowercase bare code is being
// compared as a COUNTRY: the other operand, switch tag, indexed map or
// sibling call argument names a country-ish variable. Long words match as
// substrings (billingCountry); cc and iso only as whole camel/snake tokens
// (so accent and isOpen don't count).
var (
	contextRe     = regexp.MustCompile(`(?i)country|countries|region|market|nation`)
	camelSplitRe  = regexp.MustCompile(`[A-Z]?[a-z0-9]+|[A-Z]+`)
	contextTokens = map[string]bool{"cc": true, "iso": true}
)

func contextName(name string) bool {
	if contextRe.MatchString(name) {
		return true
	}
	for _, part := range strings.Split(name, "_") {
		for _, t := range camelSplitRe.FindAllString(part, -1) {
			if contextTokens[strings.ToLower(t)] {
				return true
			}
		}
	}
	return false
}

// namesIn lists every identifier inside the expressions.
func namesIn(es ...ast.Expr) []string {
	var out []string
	for _, e := range es {
		if e == nil {
			continue
		}
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				out = append(out, id.Name)
			}
			return true
		})
	}
	return out
}

func anyContext(names []string) bool {
	for _, n := range names {
		if contextName(n) {
			return true
		}
	}
	return false
}

// countryKind classifies a literal in a testing position: uppercase codes
// and xx-XX / xx_XX locale tags always count; a lowercase or mixed-case
// bare code only with a country-ish context (ctx).
func isCountryIn(v string, ctx bool) bool {
	if m := localeRe.FindStringSubmatch(v); m != nil {
		return countryCodes[strings.ToUpper(m[1])]
	}
	if !countryCodes[strings.ToUpper(v)] {
		return false
	}
	return v == strings.ToUpper(v) || ctx
}

// scanPackage scans the files of one directory (keyed by repo-relative,
// slash-separated path). Package-wide because "is this map literal indexed
// somewhere", named map types and consts span files. Returns findings and
// marker problems.
func scanPackage(files map[string][]byte) ([]Finding, []string, error) {
	fset := token.NewFileSet()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	parsed := make([]*ast.File, 0, len(paths))
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, files[p], parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		parsed = append(parsed, f)
	}

	// Package-wide facts: names used as name[expr] with a non-literal index
	// (and the identifiers of those index expressions), named map types,
	// package-level literal bindings and plainly reassigned names.
	indexed := map[string]bool{}
	indexedBy := map[string][]string{}
	mapTypes := map[string]*ast.MapType{}
	pkgScope := scope{}
	reassigned := map[string]bool{}
	markers := map[string]map[int]string{} // path -> line -> reason
	for i, f := range parsed {
		path := paths[i]
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && (gd.Tok == token.CONST || gd.Tok == token.VAR) {
				collectBindings(pkgScope, fset, path, "<file>", gd)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.IndexExpr:
				if id, ok := unparen(v.X).(*ast.Ident); ok && strLit(v.Index) == nil {
					indexed[id.Name] = true
					indexedBy[id.Name] = append(indexedBy[id.Name], namesIn(v.Index)...)
				}
			case *ast.TypeSpec:
				if mt, ok := v.Type.(*ast.MapType); ok {
					mapTypes[v.Name.Name] = mt
				}
			case *ast.AssignStmt:
				if v.Tok != token.DEFINE {
					for _, l := range v.Lhs {
						if id, ok := l.(*ast.Ident); ok {
							reassigned[id.Name] = true
						}
					}
				}
			}
			return true
		})
		markers[path] = map[int]string{}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				text := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"), "*/"))
				if rest, ok := strings.CutPrefix(text, marker); ok {
					markers[path][fset.Position(c.Pos()).Line] = strings.TrimSpace(rest)
				}
			}
		}
	}

	var findings []Finding
	var problems []string
	// allowed reports whether a same-line marker suppresses a finding at
	// path:line, recording a problem for a reason-less marker.
	allowed := func(path string, line int) bool {
		reason, ok := markers[path][line]
		if !ok {
			return false
		}
		if reason != "" {
			return true
		}
		problems = append(problems, fmt.Sprintf("%s:%d: %s marker needs a reason (// %s <reason>)", path, line, marker, marker))
		return false
	}

	for i, f := range parsed {
		path := paths[i]
		// Map-literal bindings in this file: literal -> bound name.
		bound := map[*ast.CompositeLit]string{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.ValueSpec:
				for j, val := range v.Values {
					if cl, ok := unparen(val).(*ast.CompositeLit); ok && j < len(v.Names) {
						bound[cl] = v.Names[j].Name
					}
				}
			case *ast.AssignStmt:
				for j, val := range v.Rhs {
					if cl, ok := unparen(val).(*ast.CompositeLit); ok && j < len(v.Lhs) {
						if id, ok := v.Lhs[j].(*ast.Ident); ok {
							bound[cl] = id.Name
						}
					}
				}
			}
			return true
		})

		for _, decl := range f.Decls {
			fn := funcName(decl)
			local := scope{}
			if fd, ok := decl.(*ast.FuncDecl); ok {
				if fd.Type.Params != nil {
					for _, fld := range fd.Type.Params.List {
						for _, id := range fld.Names {
							local[id.Name] = nil
						}
					}
				}
				collectBindings(local, fset, path, fn, fd)
			}
			resolve := func(name string) *binding {
				if b, ok := local[name]; ok {
					return b
				}
				if b := pkgScope[name]; b != nil && !(b.fn == "<file>" && reassigned[name]) {
					return b
				}
				return nil
			}

			// pos: literal -> "has a country-ish context" (presence = in a
			// testing position). Idents bound to literals are resolved here.
			pos := map[*ast.BasicLit]bool{}
			mark := func(e ast.Expr, ctx []string) {
				c := anyContext(ctx)
				if bl := strLit(e); bl != nil {
					pos[bl] = pos[bl] || c
					return
				}
				id, ok := unparen(e).(*ast.Ident)
				if !ok {
					return
				}
				b := resolve(id.Name)
				if b == nil || !isCountryIn(b.lit, c || contextName(b.name)) {
					return
				}
				useLine := fset.Position(id.Pos()).Line
				if allowed(b.path, b.line) || allowed(path, useLine) {
					return
				}
				findings = append(findings, Finding{Path: b.path, Func: b.fn, Literal: b.lit,
					Kind: fmt.Sprintf("country code (const %s, used at %s:%d)", b.name, path, useLine), Line: b.line})
			}
			others := func(all []ast.Expr, skip int) []string {
				var rest []ast.Expr
				for k, a := range all {
					if k != skip {
						rest = append(rest, a)
					}
				}
				return namesIn(rest...)
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.BinaryExpr:
					if v.Op == token.EQL || v.Op == token.NEQ {
						mark(v.X, namesIn(v.Y))
						mark(v.Y, namesIn(v.X))
					}
				case *ast.SwitchStmt:
					if v.Tag != nil {
						mark(v.Tag, nil)
						for _, st := range v.Body.List {
							if cc, ok := st.(*ast.CaseClause); ok {
								for _, e := range cc.List {
									mark(e, namesIn(v.Tag))
								}
							}
						}
					}
				case *ast.IndexExpr:
					mark(v.Index, namesIn(v.X))
				case *ast.CallExpr:
					if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
						if pkg, ok := sel.X.(*ast.Ident); ok && testingCalls[pkg.Name][sel.Sel.Name] {
							for k, a := range v.Args {
								ctx := others(v.Args, k)
								mark(a, ctx)
								if cl, ok := unparen(a).(*ast.CompositeLit); ok {
									for _, el := range cl.Elts {
										mark(el, ctx)
									}
								}
							}
						}
					}
				case *ast.CompositeLit:
					mt, ok := v.Type.(*ast.MapType)
					if !ok {
						if id, isID := v.Type.(*ast.Ident); isID {
							mt, ok = mapTypes[id.Name]
						}
					}
					if !ok {
						break
					}
					name := bound[v]
					if isSetType(mt.Value) || (name != "" && indexed[name] && !isNameTable(mt, v)) {
						ctx := append([]string{name}, indexedBy[name]...)
						for _, el := range v.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok {
								mark(kv.Key, ctx)
							}
						}
					}
				}
				return true
			})

			ast.Inspect(decl, func(n ast.Node) bool {
				bl, ok := n.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(bl.Value)
				if err != nil {
					return true
				}
				kind := ""
				ctx, inPos := pos[bl]
				switch {
				case inPos && isCountryIn(val, ctx):
					kind = "country code"
				case isVendor(val):
					kind = "vendor name"
				case pluginIDRe.MatchString(val):
					kind = "plugin ID"
				}
				if kind == "" {
					return true
				}
				line := fset.Position(bl.Pos()).Line
				if allowed(path, line) {
					return true
				}
				findings = append(findings, Finding{Path: path, Func: fn, Literal: val, Kind: kind, Line: line})
				return true
			})
		}
	}
	return findings, problems, nil
}

// scanTree scans root/internal, root/cmd (both required) and root/mobile
// (when present), skipping _test.go files, testdata/ trees and
// internal/db/migrations.
func scanTree(root string) ([]Finding, []string, int, error) {
	byDir := map[string]map[string][]byte{}
	n := 0
	for _, top := range []string{"internal", "cmd", "mobile"} {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			if top == "mobile" && os.IsNotExist(err) {
				continue
			}
			return nil, nil, 0, fmt.Errorf("%s: %w", base, err)
		}
		err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == "testdata" || rel == "internal/db/migrations" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			dir := filepath.Dir(rel)
			if byDir[dir] == nil {
				byDir[dir] = map[string][]byte{}
			}
			byDir[dir][rel] = src
			n++
			return nil
		})
		if err != nil {
			return nil, nil, 0, err
		}
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var all []Finding
	var probs []string
	for _, d := range dirs {
		fs, ps, err := scanPackage(byDir[d])
		if err != nil {
			return nil, nil, 0, err
		}
		all = append(all, fs...)
		probs = append(probs, ps...)
	}
	return all, probs, n, nil
}

// Entry is one allow-list line.
type Entry struct {
	Key, Card, Reason string
	Line              int
}

// Allowlist is keyed by Finding.Key().
type Allowlist map[string]Entry

var reasonRe = regexp.MustCompile(`^#\s*#(\d+)\s+(\S.*)$`)

// parseAllowlist reads lines of the form
//
//	path/file.go|FuncOrTypeDotMethod|"quoted Go literal" # #NNNN reason
//
// ("<file>" for package-level code). Blank and #-comment lines are skipped.
func parseAllowlist(r io.Reader) (Allowlist, []string) {
	al := Allowlist{}
	var errs []string
	sc := bufio.NewScanner(r)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bad := func(why string) { errs = append(errs, fmt.Sprintf("allow-list line %d: %s: %s", ln, why, line)) }
		path, rest, ok := strings.Cut(line, "|")
		if !ok || path == "" {
			bad("want path|func|\"literal\" # #NNNN reason")
			continue
		}
		fn, rest, ok := strings.Cut(rest, "|")
		if !ok || fn == "" || strings.Contains(fn, `"`) {
			bad("want path|func|\"literal\" # #NNNN reason")
			continue
		}
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			bad("literal must be a Go-quoted string")
			continue
		}
		lit, _ := strconv.Unquote(q)
		m := reasonRe.FindStringSubmatch(strings.TrimSpace(rest[len(q):]))
		if m == nil {
			bad("needs \"# #NNNN reason\" with a card and a non-empty reason")
			continue
		}
		key := path + "|" + fn + "|" + lit
		if prev, dup := al[key]; dup {
			bad(fmt.Sprintf("duplicate of line %d", prev.Line))
			continue
		}
		al[key] = Entry{Key: key, Card: m[1], Reason: m[2], Line: ln}
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, err.Error())
	}
	return al, errs
}

// check returns every problem: un-allow-listed findings, stale entries and
// (when base is non-nil) entries the base branch's list doesn't have.
func check(findings []Finding, al Allowlist, base *Allowlist) []string {
	var probs []string
	seen := map[string]bool{}
	for _, f := range findings {
		seen[f.Key()] = true
		if _, ok := al[f.Key()]; ok {
			continue
		}
		probs = append(probs, fmt.Sprintf("%s:%d: %s %q in %s — core names a specific country, vendor or plugin (owner rule #2848, ut-docs#2888); move it to its ut-plugin-* repo or mark a reviewed exception `// %s <reason>`",
			f.Path, f.Line, f.Kind, f.Literal, f.Func, marker))
	}
	keys := make([]string, 0, len(al))
	for k := range al {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := al[k]
		if !seen[k] {
			probs = append(probs, fmt.Sprintf("allow-list line %d: stale entry (matches nothing) — remove it: %s", e.Line, k))
		}
		if base != nil {
			if _, ok := (*base)[k]; !ok {
				probs = append(probs, fmt.Sprintf("allow-list line %d: not in the base branch's allow-list — it is shrink-only, new offenders are moved or get a reviewed inline marker, never a new entry: %s", e.Line, k))
			}
		}
	}
	return probs
}

func readAllowlist(p string) (Allowlist, []string) {
	f, err := os.Open(p)
	if err != nil {
		return nil, []string{err.Error()}
	}
	defer f.Close()
	return parseAllowlist(f)
}

func main() {
	root := flag.String("root", ".", "repo root (holds internal/, cmd/ and optionally mobile/)")
	allowPath := flag.String("allowlist", "scripts/ci/core-neutral-allowlist.txt", "allow-list file")
	basePath := flag.String("base-allowlist", "", "base branch's allow-list (enables the shrink-only check)")
	flag.Parse()

	findings, probs, n, err := scanTree(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ core-neutral guard:", err)
		os.Exit(1)
	}
	if n == 0 {
		fmt.Fprintln(os.Stderr, "❌ core-neutral guard: no Go files scanned under internal/, cmd/ or mobile/")
		os.Exit(1)
	}
	al, errs := readAllowlist(*allowPath)
	probs = append(probs, errs...)
	var base *Allowlist
	if *basePath != "" {
		b, berrs := readAllowlist(*basePath)
		if len(berrs) != 0 {
			fmt.Fprintln(os.Stderr, "⚠ core-neutral guard: base allow-list unreadable, shrink check skipped:", strings.Join(berrs, "; "))
		} else {
			base = &b
		}
	}
	probs = append(probs, check(findings, al, base)...)
	for _, p := range probs {
		fmt.Fprintln(os.Stderr, "❌ core-neutral guard:", p)
	}
	if len(probs) != 0 {
		os.Exit(1)
	}
	fmt.Printf("✓ core-neutral guard: %d file(s) scanned, %d allow-list entr%s held, no un-reviewed offender\n",
		n, len(al), map[bool]string{true: "y", false: "ies"}[len(al) == 1])
}
