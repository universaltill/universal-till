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
// Flagged calls: <x>.Settings.Set( / <x>.Settings.SetMany(,
// common.SaveState(, SetEnabledBarcodeSymbologies(,
// SetBarcodeSymbologyEnabled(. Writes through other receivers (a local
// `store` variable, common.SaveRestoredMenuKeys) are not recognised -- keep
// new code on d.Settings so this guard sees it.

// settingsWriteGuardFileAllowlist: files whose direct writes are reviewed
// as a whole. Keep it tight -- prefer a per-line annotation.
var settingsWriteGuardFileAllowlist = map[string]string{
	"settings_sync_proxy.go": "the write-through itself: mirrors what the main till accepted, writes per-till keys locally",
	"sync_settings.go":       "main-till side of the write-through (/api/sync/settings/apply); refuses on a till that follows one",
	"init.go":                "boot: persists the state just loaded from this till's own rows (no change to send)",
	"setup_page.go":          "first-boot wizard: runs before this till follows a main till; a joining till takes the main till's settings from its first pull",
	"setup_tse.go":           "TSE provisioning: one TSE per store, run from the main till's wizard (ADR-0053)",
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
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := scanSettingsWrites(t, dir)
	wantLines := []string{"bad.go:12:", "bad.go:13:", "bad.go:14:", "bad.go:15:", "bad.go:16:", "bad.go:24:"}
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
	return &constResolver{modPath: mod, modRoot: root, pkgs: map[string]map[string]constDef{}}
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
	ast.Inspect(f, func(n ast.Node) bool {
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
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "Settings" || len(call.Args) < 2 {
				return true
			}
			if sel.Sel.Name == "Set" && c.perTill(call.Args[1], dir, imps) {
				return true
			}
			if sel.Sel.Name == "SetMany" && c.perTillMap(call.Args[1], dir, imps) {
				return true
			}
			what = "Settings." + sel.Sel.Name + " with a key that is not a per-till constant"
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
	})
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
