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

// ut-docs#6823 slice 2 (ADR-0175 s15.2 rule 5): every shop-wide key a Settings
// handler writes through saveShopSettings has a registry entry (any tier,
// "never" included, so a decision is on record) or a reasoned line in the
// shrink-only baseline below. A NEW handler key must get a registry entry, not
// a baseline line. Keys the scan cannot resolve to a constant (a variable, a
// concatenation) are listed in dynamicSettingsHandlerWrites with a reason.

// settingsHandlerKeysWithoutRegistryEntry: shop-wide keys written by a handler
// that have no registry entry yet. Shrink-only; each states why it is not a
// registry key (a registry entry is a key the cloud may read or write).
var settingsHandlerKeysWithoutRegistryEntry = map[string]string{
	"payments.fee.":              "prefix family: per-method fee JSON (money); tier never until it has a typed rule",
	"store.currency_confirmed":   "internal flag set with store.currency (tier never: not a choice, derived from the currency save)",
	"store.locale_generation":    "internal counter set with store.locale (tier never: replica ordering, not a choice)",
	"menu.restored_keys":         "UI state of hidden Menu tiles (JSON); not cloud material, tier never",
	"reports.business_day_start": "slice 3: the End-of-day card (HH:MM); a typed text rule and its my. wording come with the EOD region",
	"update.auto_time":           "slice 3: update schedule time, same card as update.auto_enabled (sensitive: changes when code runs)",
	"update.fleet_follow":        "slice 3: fleet update policy next_idle|nightly, same card; sensitive",
	"receipt.show_sku":           "slice 3: receipt layout toggle (normal tier bool); designer region keys come with it",
	"receipt.show_tax":           "slice 3: receipt layout toggle (normal tier bool)",
	"receipt.show_barcode":       "slice 3: receipt layout toggle (normal tier bool)",
	"receipt.show_logo":          "slice 3: receipt layout toggle (normal tier bool)",
	"receipt.show_tracking_qr":   "slice 3: receipt layout toggle (normal tier bool)",
	"receipt.voucher_slip_text":  "slice 3: gift voucher slip text (sensitive: free text printed for customers)",
}

// dynamicSettingsHandlerWrites: handler files that call saveShopSettings with a
// key the scan cannot read as a constant, with the reason it is not registry
// material. A new file here needs a review reason.
var dynamicSettingsHandlerWrites = map[string]string{
	"fiscal_device_page.go":  "fiscal.<country>.signing_device_configured: fiscal posture, tier never (ADR-0175 s15.3)",
	"settings_page.go":       "the generic save path: each caller's key is a registered constant above; the till name key is till.name or sync.till_name (identity, tier never)",
	"settings_sync_proxy.go": "the write-through helper itself: forwards the caller's key set",
}

func TestEveryHandlerSettingsKeyHasARegistryEntry_6823(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	written, dynamic := scanHandlerSettingsKeys(t, filepath.Dir(self))
	if len(written) < 15 {
		t.Fatalf("only %d handler keys found: the scan drifted", len(written))
	}
	for key, where := range written {
		if data.SettingScope(key) != data.SettingShopWide {
			continue // per-till keys are never cloud-visible
		}
		if _, ok := data.LookupAgentSetting(key); ok {
			if reason := settingsHandlerKeysWithoutRegistryEntry[key]; reason != "" {
				t.Errorf("%s now has a registry entry: delete it from settingsHandlerKeysWithoutRegistryEntry", key)
			}
			continue
		}
		if baselineCovers(key) {
			continue
		}
		t.Errorf("%s (written at %s) has no registry entry: add one to data.AgentSettings with a tier "+
			"(tier never is a valid, recorded decision), or a reasoned line in settingsHandlerKeysWithoutRegistryEntry", key, where)
	}
	for file := range dynamic {
		if dynamicSettingsHandlerWrites[file] == "" {
			t.Errorf("%s writes settings through saveShopSettings with a non-constant key set: resolve it to constants, "+
				"or list the file in dynamicSettingsHandlerWrites with a reason", file)
		}
	}
	for k := range settingsHandlerKeysWithoutRegistryEntry {
		if _, ok := written[k]; !ok && !strings.HasSuffix(k, ".") {
			t.Errorf("baseline names %s, which no handler writes any more", k)
		}
	}
}

func baselineCovers(key string) bool {
	for p, reason := range settingsHandlerKeysWithoutRegistryEntry {
		if reason == "" {
			continue
		}
		if key == p || (strings.HasSuffix(p, ".") && strings.HasPrefix(key, p)) {
			return true
		}
	}
	return false
}

// The scan itself must see literals, constants and prefix concatenations, and
// must flag a variable map.
func TestScanHandlerSettingsKeys_Detects(t *testing.T) {
	dir := t.TempDir()
	src := `package pages

const keyA = "store.name"

func f() {
	_ = saveShopSettings(ctx, d, elev, map[string]string{keyA: "x", "shop.type": "y", "payments.fee." + m: "z"})
	_ = saveShopSettings(ctx, d, elev, kv)
}
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	written, dynamic := scanHandlerSettingsKeys(t, dir)
	for _, k := range []string{"store.name", "shop.type", "payments.fee."} {
		if _, ok := written[k]; !ok {
			t.Errorf("key %q not found in %v", k, written)
		}
	}
	if _, ok := dynamic["x.go"]; !ok {
		t.Errorf("the variable key set was not flagged: %v", dynamic)
	}
}

// scanHandlerSettingsKeys returns key -> "file:line" for every constant key of a
// saveShopSettings map literal (a "prefix"+var concatenation yields the prefix),
// and the files with a call whose key set it cannot read.
func scanHandlerSettingsKeys(t *testing.T, root string) (map[string]string, map[string]bool) {
	t.Helper()
	res := newConstResolver(t)
	written := map[string]string{}
	dynamic := map[string]bool{}
	var files []string
	_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	for _, path := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		dir := filepath.Dir(path)
		imps := fileImports(f)
		name := filepath.Base(path)
		// add records one key expression; false when it is not readable.
		add := func(e ast.Expr) {
			where := name + ":" + strconv.Itoa(fset.Position(e.Pos()).Line)
			if k, ok := res.eval(e, dir, imps, 0); ok {
				written[k] = where
				return
			}
			if be, ok := e.(*ast.BinaryExpr); ok {
				if k, ok := res.eval(be.X, dir, imps, 0); ok && k != "" {
					written[k] = where // "prefix." + variable
					return
				}
			}
			dynamic[name] = true
		}
		addMap := func(lit *ast.CompositeLit) {
			for _, el := range lit.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					add(kv.Key)
				}
			}
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "saveShopSettings" || len(call.Args) < 4 {
					return true
				}
				switch a := call.Args[3].(type) {
				case *ast.CompositeLit:
					addMap(a)
				case *ast.Ident:
					// A map variable: its literal(s) and every kv[key] = ... in the function.
					found := false
					ast.Inspect(fn, func(m ast.Node) bool {
						switch x := m.(type) {
						case *ast.AssignStmt:
							for i, l := range x.Lhs {
								if ix, ok := l.(*ast.IndexExpr); ok {
									if b, ok := ix.X.(*ast.Ident); ok && b.Name == a.Name {
										add(ix.Index)
										found = true
									}
								}
								if b, ok := l.(*ast.Ident); ok && b.Name == a.Name && i < len(x.Rhs) {
									if lit, ok := x.Rhs[i].(*ast.CompositeLit); ok {
										addMap(lit)
										found = true
									}
								}
							}
						case *ast.ValueSpec:
							for i, b := range x.Names {
								if b.Name == a.Name && i < len(x.Values) {
									if lit, ok := x.Values[i].(*ast.CompositeLit); ok {
										addMap(lit)
										found = true
									}
								}
							}
						}
						return true
					})
					if !found {
						dynamic[name] = true
					}
				default:
					dynamic[name] = true
				}
				return true
			})
		}
	}
	return written, dynamic
}
