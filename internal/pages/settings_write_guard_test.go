package pages

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2979 guard: on a till that follows a main till, a shop-wide
// setting written with a plain store write is silently reverted by the next
// admin pull (main-till-wins). Every settings write in this package goes
// through saveShopSettings / saveStateThrough (settings_sync_proxy.go),
// unless it is
//
//   - a Set/SetMany whose key(s) are string constants that data.SettingScope
//     classifies per-till (never synced, so a local write is right),
//   - annotated on its line or the line above with
//     `// settings-write:allow <reason>` (non-empty reason), or
//   - in settingsWriteGuardFileAllowlist below (main-till-only or pre-join
//     code, each with its reason).
//
// Flagged calls: Set( / SetMany( on any receiver that holds the settings
// store -- <x>.Settings, a parameter, variable or struct field typed
// *settings.Store, a variable assigned from one of those, or a value of a
// package interface type with a Set/SetMany method (ut-docs#2999: the
// store's type, not the field name; internal/pages/common's own helpers
// are scanned too) -- plus common.SaveState(, SetEnabledBarcodeSymbologies(
// and SetBarcodeSymbologyEnabled(. The tracking is syntactic (no type checker):
// a store reached through a function's return value, a map or a package
// variable is not recognised.

// settingsWriteGuardFileAllowlist: files whose direct writes are reviewed
// as a whole. Keep it tight -- prefer a per-line annotation.
var settingsWriteGuardFileAllowlist = map[string]string{
	"settings_sync_proxy.go": "the write-through itself: mirrors what the main till accepted, writes per-till keys locally",
	"sync_settings.go":       "main-till side of the write-through (/api/sync/settings/apply); refuses on a till that follows one",
	"init.go":                "boot: persists the state just loaded from this till's own rows (no change to send)",
	"setup_page.go":          "first-boot wizard: runs before this till follows a main till; a joining till takes the main till's settings from its first pull",
}

const settingsWriteGuardTag = "settings-write:allow"

func TestSettingsWriteGuard_NoDirectShopWideWrites(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// Not ".": other tests in this package chdir to the repo root.
	violations := scanSettingsWrites(t, filepath.Dir(self))
	if len(violations) > 0 {
		t.Fatalf("direct settings writes that bypass the main-till write-through (ut-docs#2979):\n  %s\n\n"+
			"Fix: persist through saveShopSettings(ctx, d, elev, map[string]string{...}) or saveStateThrough "+
			"(settings_sync_proxy.go) so an additional till sends the change to its main till. If the key is "+
			"per-till, use a string constant data.SettingScope classifies as per-till; if the write is genuinely "+
			"local (main-till-only, sale-time bookkeeping), annotate the line (or the line above) with "+
			"`// %s <reason>`.", strings.Join(violations, "\n  "), settingsWriteGuardTag)
	}
}

// The guard must actually catch a direct shop-wide write, and honour each
// exemption.
func TestSettingsWriteGuard_DetectsAndExempts(t *testing.T) {
	dir := t.TempDir()
	src := `package pages

import (
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const keyLocalPrinter = "printer.mode"
const keyShop = "store.name"

func f(d *common.Deps, k string) {
	_ = d.Settings.Set(ctx, keyShop, "x")                         // flagged: shop-wide const
	_ = d.Settings.Set(ctx, k, "x")                               // flagged: unresolvable key
	_ = d.Settings.SetMany(ctx, map[string]string{keyShop: "x"}) // flagged
	_ = common.SaveState(ctx, d.Settings, st)                     // flagged
	_, _ = repo.SetBarcodeSymbologyEnabled(ctx, "EAN13", true)    // flagged
	_ = d.Settings.Set(ctx, keyLocalPrinter, "x")                 // per-till const
	_ = d.Settings.Set(ctx, "sync.last_contact_at", "x")          // per-till literal
	_ = d.Settings.Set(ctx, data.UpdateFollowErrorSettingsKey, "") // per-till, other package
	_ = d.Settings.SetMany(ctx, map[string]string{"printer.a": "1", keyLocalPrinter: "2"})
	// settings-write:allow reviewed in a test
	_ = d.Settings.Set(ctx, keyShop, "x")
	_ = d.Settings.Set(ctx, keyShop, "x") // settings-write:allow same-line reason
	_ = d.Settings.Set(ctx, keyShop, "x") // settings-write:allow
}
`
	// ut-docs#2999: the store's type, not the field name -- a write through
	// any variable, parameter, struct field or package interface that holds
	// the settings store.
	src2 := `package pages

import (
	"context"

	st "github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/pages/common"
)

type kvWriter interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

type kvReader interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

type holder struct {
	store *st.Store
	other *otherCache
}

func g(ctx context.Context, store *st.Store, kv kvWriter, rd kvReader, h *holder, d *common.Deps) {
	_ = store.Set(ctx, keyShop, "x")                          // flagged: *settings.Store parameter
	_ = store.Set(ctx, keyLocalPrinter, "x")                  // per-till const
	s := d.Settings
	_ = s.SetMany(ctx, map[string]string{keyShop: "x"})       // flagged: alias of d.Settings
	_ = kv.Set(ctx, keyShop, "x")                             // flagged: interface with Set
	_ = kv.Set(ctx, keyLocalPrinter, "x")                     // per-till const
	_ = h.store.Set(ctx, keyShop, "x")                        // flagged: struct field of the store type
	var v *st.Store
	_ = v.Set(ctx, keyShop, "x")                              // flagged: var of the store type
	_ = h.other.Set(ctx, keyShop, "x")                        // not a settings store
	w.Header().Set("Content-Type", "text/html")               // not a settings store
	_ = rd.Get(ctx, keyShop)                                  // a read
	fn := func(inner *st.Store) {
		_ = inner.Set(ctx, keyShop, "x")                      // flagged: closure parameter
	}
	_ = fn
}
`
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad2.go"), []byte(src2), 0o644); err != nil {
		t.Fatal(err)
	}
	got := scanSettingsWrites(t, dir)
	wantLines := []string{
		"bad.go:12:", "bad.go:13:", "bad.go:14:", "bad.go:15:", "bad.go:16:", "bad.go:24:",
		"bad2.go:25:", "bad2.go:28:", "bad2.go:29:", "bad2.go:31:", "bad2.go:33:", "bad2.go:38:",
	}
	if len(got) != len(wantLines) {
		t.Fatalf("violations = %d, want %d:\n%s", len(got), len(wantLines), strings.Join(got, "\n"))
	}
	for i, w := range wantLines {
		if !strings.HasPrefix(got[i], w) {
			t.Fatalf("violation %d = %q, want it at %s", i, got[i], w)
		}
	}
}

// scanSettingsWrites returns "file:line: reason" for every unexempted write
// in the non-test .go files under root (subpackages included).
func scanSettingsWrites(t *testing.T, root string) []string {
	t.Helper()
	res := newConstResolver(t)
	var out []string
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if path != root && (e.Name() == "testdata" || strings.HasPrefix(e.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if filepath.Dir(path) == filepath.Clean(root) {
			if _, ok := settingsWriteGuardFileAllowlist[name]; ok {
				return nil
			}
		}
		shown, rerr := filepath.Rel(res.modRoot, path)
		if rerr != nil || strings.HasPrefix(shown, "..") {
			shown, _ = filepath.Rel(root, path)
		}
		out = append(out, res.scanFile(t, path, filepath.ToSlash(shown))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.SliceStable(out, func(i, j int) bool { return lessFileLine(out[i], out[j]) })
	return out
}

func lessFileLine(a, b string) bool {
	fa, la := splitFileLine(a)
	fb, lb := splitFileLine(b)
	if fa != fb {
		return fa < fb
	}
	return la < lb
}

func splitFileLine(s string) (string, int) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return s, 0
	}
	n, _ := strconv.Atoi(parts[1])
	return parts[0], n
}

// constResolver evaluates string constants: literals, package-level consts
// of the scanned file's own directory, and consts of any package of this
// module the file imports (by selector).
type constResolver struct {
	modPath, modRoot string
	pkgs             map[string]map[string]constDef // dir -> name -> def
	storeDecls       map[string]storeDecls          // dir -> settings-store-typed declarations
}

// storeDecls are a package's own declarations that hold the settings store:
// interface types with a Set or SetMany method (what *settings.Store is
// passed as), and struct fields typed *settings.Store or such an interface.
type storeDecls struct {
	ifaces map[string]bool
	fields map[string]bool
}

type constDef struct {
	expr    ast.Expr
	imports map[string]string // local name -> import path, of the defining file
	dir     string
}

func newConstResolver(t *testing.T) *constResolver {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("go.mod not found")
		}
		root = parent
	}
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	mod := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "module ") {
			mod = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	return &constResolver{modPath: mod, modRoot: root, pkgs: map[string]map[string]constDef{}, storeDecls: map[string]storeDecls{}}
}

func fileImports(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		name := filepath.Base(p)
		if im.Name != nil {
			name = im.Name.Name
		}
		m[name] = p
	}
	return m
}

// consts parses (once) every package-level const of dir.
func (c *constResolver) consts(dir string) map[string]constDef {
	dir = filepath.Clean(dir)
	if m, ok := c.pkgs[dir]; ok {
		return m
	}
	m := map[string]constDef{}
	c.pkgs[dir] = m
	entries, _ := os.ReadDir(dir)
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		c.addConsts(m, f, dir)
	}
	return m
}

func (c *constResolver) addConsts(m map[string]constDef, f *ast.File, dir string) {
	imps := fileImports(f)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i < len(vs.Values) {
					m[n.Name] = constDef{expr: vs.Values[i], imports: imps, dir: dir}
				}
			}
		}
	}
}

// eval resolves e to a string constant in the context of a file in dir
// with the given imports.
func (c *constResolver) eval(e ast.Expr, dir string, imps map[string]string, depth int) (string, bool) {
	if depth > 10 {
		return "", false
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return c.eval(x.X, dir, imps, depth+1)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := c.eval(x.X, dir, imps, depth+1)
		b, ok2 := c.eval(x.Y, dir, imps, depth+1)
		return a + b, ok1 && ok2
	case *ast.Ident:
		def, ok := c.consts(dir)[x.Name]
		if !ok {
			return "", false
		}
		return c.eval(def.expr, def.dir, def.imports, depth+1)
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		path, ok := imps[pkg.Name]
		if !ok || !strings.HasPrefix(path, c.modPath+"/") {
			return "", false
		}
		pdir := filepath.Join(c.modRoot, strings.TrimPrefix(path, c.modPath+"/"))
		def, ok := c.consts(pdir)[x.Sel.Name]
		if !ok {
			return "", false
		}
		return c.eval(def.expr, def.dir, def.imports, depth+1)
	}
	return "", false
}

func (c *constResolver) perTill(e ast.Expr, dir string, imps map[string]string) bool {
	k, ok := c.eval(e, dir, imps, 0)
	return ok && data.SettingScope(k) == data.SettingPerTill
}

// isSettingsStorePtr reports whether e is *settings.Store (this module's
// internal/settings, under whatever name the file imports it).
func (c *constResolver) isSettingsStorePtr(e ast.Expr, imps map[string]string) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Store" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && imps[pkg.Name] == c.modPath+"/internal/settings"
}

// isStoreType reports whether the type expression e holds the settings
// store: *settings.Store, or a package interface type with Set/SetMany.
func (c *constResolver) isStoreType(e ast.Expr, dir string, imps map[string]string) bool {
	if c.isSettingsStorePtr(e, imps) {
		return true
	}
	id, ok := e.(*ast.Ident)
	return ok && c.stores(dir).ifaces[id.Name]
}

// stores parses (once) dir's store-holding interface types and struct
// fields.
func (c *constResolver) stores(dir string) storeDecls {
	dir = filepath.Clean(dir)
	if sd, ok := c.storeDecls[dir]; ok {
		return sd
	}
	sd := storeDecls{ifaces: map[string]bool{}, fields: map[string]bool{}}
	c.storeDecls[dir] = sd
	entries, _ := os.ReadDir(dir)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		files = append(files, f)
	}
	// Interfaces first: a struct field may be typed by one.
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				for _, m := range it.Methods.List {
					for _, n := range m.Names {
						if n.Name == "Set" || n.Name == "SetMany" {
							sd.ifaces[ts.Name.Name] = true
						}
					}
				}
			}
		}
	}
	for _, f := range files {
		imps := fileImports(f)
		ast.Inspect(f, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fl := range st.Fields.List {
				if c.isStoreType(fl.Type, dir, imps) {
					for _, n := range fl.Names {
						sd.fields[n.Name] = true
					}
				}
			}
			return true
		})
	}
	return sd
}

// storeIdents returns the names a function declaration binds to the
// settings store: parameters (its closures' included) and variables typed
// as one, and variables assigned from a store expression. Shadowing is
// ignored -- a same-named non-store variable is flagged, and takes an
// allow tag.
func (c *constResolver) storeIdents(fn *ast.FuncDecl, dir string, imps map[string]string) map[string]bool {
	set := map[string]bool{}
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			if c.isStoreType(f.Type, dir, imps) {
				for _, n := range f.Names {
					set[n.Name] = true
				}
			}
		}
	}
	addFields(fn.Recv)
	// Assignments can chain (a := d.Settings; b := a), so repeat until no
	// new name appears.
	for grew := true; grew; {
		before := len(set)
		ast.Inspect(fn, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncType:
				addFields(x.Params)
			case *ast.ValueSpec:
				if x.Type != nil && c.isStoreType(x.Type, dir, imps) {
					for _, n := range x.Names {
						set[n.Name] = true
					}
				}
				for i, v := range x.Values {
					if i < len(x.Names) && c.isStoreExpr(v, dir, set) {
						set[x.Names[i].Name] = true
					}
				}
			case *ast.AssignStmt:
				if len(x.Lhs) != len(x.Rhs) {
					return true
				}
				for i, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok && c.isStoreExpr(x.Rhs[i], dir, set) {
						set[id.Name] = true
					}
				}
			}
			return true
		})
		grew = len(set) > before
	}
	return set
}

// isStoreExpr reports whether e evaluates to the settings store:
// <x>.Settings (common.Deps' field), <x>.<a store-typed struct field>, or
// a name already bound to the store.
func (c *constResolver) isStoreExpr(e ast.Expr, dir string, idents map[string]bool) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return c.isStoreExpr(x.X, dir, idents)
	case *ast.Ident:
		return idents[x.Name]
	case *ast.SelectorExpr:
		return x.Sel.Name == "Settings" || c.stores(dir).fields[x.Sel.Name]
	}
	return false
}

func (c *constResolver) scanFile(t *testing.T, path, shown string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	dir := filepath.Dir(path)
	imps := fileImports(f)
	// A reasoned allow tag covers its own line, and the next line when the
	// comment stands on a line of its own.
	allowed := map[int]bool{}
	for _, cg := range f.Comments {
		for _, cm := range cg.List {
			txt := cm.Text
			i := strings.Index(txt, settingsWriteGuardTag)
			if i < 0 {
				continue
			}
			reason := strings.TrimSpace(strings.TrimSuffix(txt[i+len(settingsWriteGuardTag):], "*/"))
			if reason == "" {
				continue
			}
			pos := fset.Position(cm.Slash)
			allowed[pos.Line] = true
			if pos.Line-1 < len(lines) && strings.TrimSpace(lines[pos.Line-1][:pos.Column-1]) == "" {
				allowed[pos.Line+1] = true
			}
		}
	}
	var out []string
	check := func(n ast.Node, idents map[string]bool) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		what := ""
		switch sel.Sel.Name {
		case "Set", "SetMany":
			if !c.isStoreExpr(sel.X, dir, idents) || len(call.Args) < 2 {
				return true
			}
			if sel.Sel.Name == "Set" && c.perTill(call.Args[1], dir, imps) {
				return true
			}
			if sel.Sel.Name == "SetMany" && c.perTillMap(call.Args[1], dir, imps) {
				return true
			}
			what = "settings store " + sel.Sel.Name + " with a key that is not a per-till constant"
		case "SaveState":
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "common" {
				return true
			}
			what = "common.SaveState rewrites every shop-wide key locally"
		case "SetEnabledBarcodeSymbologies", "SetBarcodeSymbologyEnabled":
			what = sel.Sel.Name + " writes the shop-wide barcode symbology set locally"
		default:
			return true
		}
		line := fset.Position(call.Pos()).Line
		if allowed[line] {
			return true
		}
		out = append(out, shown+":"+strconv.Itoa(line)+": "+what)
		return true
	}
	for _, decl := range f.Decls {
		idents := map[string]bool{}
		if fn, ok := decl.(*ast.FuncDecl); ok {
			idents = c.storeIdents(fn, dir, imps)
		}
		ast.Inspect(decl, func(n ast.Node) bool { return check(n, idents) })
	}
	return out
}

// perTillMap reports whether e is a map literal whose keys are all per-till
// constants.
func (c *constResolver) perTillMap(e ast.Expr, dir string, imps map[string]string) bool {
	lit, ok := e.(*ast.CompositeLit)
	if !ok || len(lit.Elts) == 0 {
		return false
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok || !c.perTill(kv.Key, dir, imps) {
			return false
		}
	}
	return true
}
