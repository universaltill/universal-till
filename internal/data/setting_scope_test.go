package data

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ut-docs#2791: every settings key the code reads or writes must be
// classified per-till or shop-wide (SettingScope). An additional till writes
// a shop-wide key through to the main till instead of saving it locally
// (pages/settings_sync_proxy.go), and the main till's
// POST /api/sync/settings/apply refuses anything SettingScope does not call
// shop-wide — so a new key nobody classified would be silently refused on
// every additional till. This scan fails the build on such a key instead.
//
// Blind spots (review of ut-docs#2791): a key held in a var, built with
// fmt.Sprintf, or written through a repo method other than the Settings
// Get/Set/Delete/GetByPrefix/GetOrCreate/SetMany/SaveState shapes is not
// seen. The anchor keys and the minimum count below stop the scan from
// silently finding nothing, not from missing such a key.

// settingsScanRoot is the repository root, from this file's own location.
func settingsScanRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// settingsKeyMethods are the settings.Store / SettingsRepo methods whose
// second argument (after ctx) is a settings key or key prefix.
var settingsKeyMethods = map[string]bool{
	"Get": true, "Set": true, "Delete": true, "GetByPrefix": true, "GetOrCreate": true,
}

// looksLikeCtx reports whether e is the context argument settings calls
// take first (ctx, c, r.Context(), context.Background(), ...). Settings are
// reached through many receivers (d.Settings, store, kv, repo, st,
// settingsReader, ...), so the call shape -- (ctx, key, ...) -- is what
// identifies a settings call, not the receiver's name.
func looksLikeCtx(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "ctx" || v.Name == "c" || strings.HasSuffix(v.Name, "Ctx")
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
			switch sel.Sel.Name {
			case "Context", "Background", "TODO":
				return true
			}
		}
	}
	return false
}

// keyConstName matches constant names that hold a settings key by this
// codebase's naming convention (KeyX, keyX, XKey, XSettingKey,
// XSettingsKey). Every such constant is scanned, not only the ones passed
// straight to a settings call: several packages reach settings through a
// small local wrapper (enroll's get(keyStoreID), a kv map built first and
// SetMany'd later).
var keyConstName = regexp.MustCompile(`^(Key|key)[A-Z]|Key$`)

// notSettingsKeyConsts are constants keyConstName matches that are not
// settings keys, each with the reason.
var notSettingsKeyConsts = map[string]string{
	"httpx.genericErrKey":       "an i18n message key",
	"pages.buttonsErrorKey":     "an i18n message key",
	"plugins.DocsEntryKey":      "a plugin manifest entry key",
	"pages.reservedChargeKey":   "a charge-hook line key",
	"ui.designerErrorServerKey": "an i18n message key",
	"pos.ServiceChargeKey":      "a sale charge-line key",
}

// settingsKeyShape is what a settings key (or a key-family prefix built by
// "prefix."+x) looks like: lower-case words, usually dotted ("theme" and
// "barcode_enabled_symbologies" are single-word keys).
var settingsKeyShape = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*\.?$`)

type settingsKeyScan struct {
	consts map[string]string // "pkg.Name" -> value
	keys   map[string]string // key -> first place it was seen
}

func (s *settingsKeyScan) add(key, where string) {
	if key == "" {
		return
	}
	if !settingsKeyShape.MatchString(key) {
		return
	}
	if _, ok := s.keys[key]; !ok {
		s.keys[key] = where
	}
}

// resolve returns the string value of a literal, a constant (bare or
// package-qualified), or the leading literal/constant of a "+" concatenation
// (a key family like "payments.fee."+method).
func (s *settingsKeyScan) resolve(e ast.Expr, pkg string, imports map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		str, err := strconv.Unquote(v.Value)
		return str, err == nil
	case *ast.Ident:
		val, ok := s.consts[pkg+"."+v.Name]
		return val, ok
	case *ast.SelectorExpr:
		x, ok := v.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		p, ok := imports[x.Name]
		if !ok {
			return "", false
		}
		val, ok := s.consts[p+"."+v.Sel.Name]
		return val, ok
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		return s.resolve(v.X, pkg, imports)
	case *ast.ParenExpr:
		return s.resolve(v.X, pkg, imports)
	}
	return "", false
}

// evalConst fully evaluates a constant string expression (literals,
// other constants, "+" concatenations); ok=false while a referenced
// constant is not known yet.
func (s *settingsKeyScan) evalConst(e ast.Expr, pkg string, imports map[string]string) (string, bool) {
	if b, ok := e.(*ast.BinaryExpr); ok {
		if b.Op != token.ADD {
			return "", false
		}
		l, ok1 := s.evalConst(b.X, pkg, imports)
		r, ok2 := s.evalConst(b.Y, pkg, imports)
		return l + r, ok1 && ok2
	}
	return s.resolve(e, pkg, imports)
}

type parsedGoFile struct {
	file *ast.File
	pkg  string
	rel  string
}

func (s *settingsKeyScan) importsOf(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := path.Base(p)
		if imp.Name != nil {
			m[imp.Name.Name] = name
		} else {
			m[name] = name
		}
	}
	return m
}

// scanSettingsKeysInCode walks the non-test Go files under internal/ and
// cmd/: first every string constant declaration, then every settings call.
func scanSettingsKeysInCode(t *testing.T, root string) *settingsKeyScan {
	t.Helper()
	s := &settingsKeyScan{consts: map[string]string{}, keys: map[string]string{}}
	fset := token.NewFileSet()
	var files []parsedGoFile
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				if de.Name() == "testdata" || de.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			files = append(files, parsedGoFile{file: f, pkg: f.Name.Name, rel: rel})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Pass 1: string constants, keyed by package name. Iterated to a fixed
	// point so a constant built from others (fxlevel's SettingsPrefix +
	// "level") resolves whatever order the files were read in.
	type pendingConst struct {
		pf      parsedGoFile
		imports map[string]string
		name    string
		expr    ast.Expr
	}
	var pending []pendingConst
	for _, pf := range files {
		imports := s.importsOf(pf.file)
		for _, decl := range pf.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						pending = append(pending, pendingConst{pf, imports, name.Name, vs.Values[i]})
					}
				}
			}
		}
	}
	for progress := true; progress; {
		progress = false
		rest := pending[:0]
		for _, pc := range pending {
			if v, ok := s.evalConst(pc.expr, pc.pf.pkg, pc.imports); ok {
				s.consts[pc.pf.pkg+"."+pc.name] = v
				if keyConstName.MatchString(pc.name) && notSettingsKeyConsts[pc.pf.pkg+"."+pc.name] == "" {
					s.add(v, pc.pf.rel+" const "+pc.name)
				}
				progress = true
				continue
			}
			rest = append(rest, pc)
		}
		pending = rest
	}
	// Pass 2: settings calls and SaveState's kv map.
	for _, pf := range files {
		imports := s.importsOf(pf.file)
		ast.Inspect(pf.file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if settingsKeyMethods[sel.Sel.Name] && len(v.Args) >= 2 && looksLikeCtx(v.Args[0]) {
					if key, ok := s.resolve(v.Args[1], pf.pkg, imports); ok {
						s.add(key, pf.rel+" "+sel.Sel.Name)
					}
				}
				if sel.Sel.Name == "SetMany" && len(v.Args) >= 2 {
					if cl, ok := v.Args[1].(*ast.CompositeLit); ok {
						s.addMapKeys(cl, pf, imports)
					}
				}
			case *ast.FuncDecl:
				if v.Name.Name == "SaveState" && v.Body != nil {
					ast.Inspect(v.Body, func(m ast.Node) bool {
						switch w := m.(type) {
						case *ast.CompositeLit:
							if _, ok := w.Type.(*ast.MapType); ok {
								s.addMapKeys(w, pf, imports)
							}
						case *ast.IndexExpr:
							if id, ok := w.X.(*ast.Ident); ok && id.Name == "kv" {
								if key, ok := s.resolve(w.Index, pf.pkg, imports); ok {
									s.add(key, pf.rel+" SaveState")
								}
							}
						}
						return true
					})
				}
			}
			return true
		})
	}
	return s
}

func (s *settingsKeyScan) addMapKeys(cl *ast.CompositeLit, pf parsedGoFile, imports map[string]string) {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := s.resolve(kv.Key, pf.pkg, imports); ok {
			s.add(key, pf.rel+" map")
		}
	}
}

var (
	migrationInsertSettings = regexp.MustCompile(`(?is)INSERT\s+(?:OR\s+\w+\s+)?INTO\s+"?settings"?\s*\([^)]*\)\s*VALUES\s*(.*?);`)
	migrationTupleKey       = regexp.MustCompile(`\(\s*'([^']+)'`)
	migrationRenameKey      = regexp.MustCompile(`(?i)UPDATE\s+settings\s+SET\s+key\s*=\s*'([^']+)'`)
)

// scanSettingsKeysInMigrations adds every key a migration seeds into (or
// renames within) the settings table.
func scanSettingsKeysInMigrations(t *testing.T, root string, s *settingsKeyScan) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "internal", "db", "migrations", "*.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no migrations found (err=%v)", err)
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sql := string(raw)
		for _, m := range migrationInsertSettings.FindAllStringSubmatch(sql, -1) {
			for _, k := range migrationTupleKey.FindAllStringSubmatch(m[1], -1) {
				s.add(k[1], filepath.Base(p))
			}
		}
		for _, m := range migrationRenameKey.FindAllStringSubmatch(sql, -1) {
			s.add(m[1], filepath.Base(p))
		}
	}
}

func TestSettingScope_EveryUsedKeyIsClassified(t *testing.T) {
	root := settingsScanRoot(t)
	s := scanSettingsKeysInCode(t, root)
	scanSettingsKeysInMigrations(t, root, s)

	// The scan must actually see the codebase, or "nothing unclassified"
	// proves nothing.
	for _, want := range []string{OrderTypePromptModeKey, "store.currency", "sync.bearer", "store.name", "payments.fee.", "store.tax_rate"} {
		if _, ok := s.keys[want]; !ok {
			t.Errorf("scan did not find %q — the scanner is broken, not the classification", want)
		}
	}
	if len(s.keys) < 60 {
		t.Errorf("scan found only %d keys, want at least 60", len(s.keys))
	}

	var unclassified []string
	for k, where := range s.keys {
		if SettingScope(k) == SettingUnclassified {
			unclassified = append(unclassified, k+"  ("+where+")")
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("%d settings key(s) are neither per-till (PerTillSettingPrefixes) nor shop-wide (ShopWideSettingPrefixes) — classify each in sync_admin_repo.go:\n  %s",
			len(unclassified), strings.Join(unclassified, "\n  "))
	}
	t.Logf("scan found %d settings keys, all classified", len(s.keys))
}

func TestSettingScope_Classification(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want SettingScopeKind
	}{
		{"sale.order_type_prompt", SettingShopWide},
		{"store.currency", SettingShopWide},
		{"payments.fee.card", SettingShopWide},
		{"payments.default_method", SettingShopWide},
		{"marketplace.store_id", SettingShopWide},
		{"marketplace.telemetry_opt_in", SettingShopWide},
		{"sync.bearer", SettingPerTill},
		{"printer.host", SettingPerTill},
		{"display.mode", SettingPerTill},
		{"theme", SettingPerTill},
		{"marketplace.device_id", SettingPerTill},
		{"marketplace.token", SettingPerTill},
		// ut-docs#2950: one till's own state.
		{"diagnostics.active", SettingPerTill},
		{"cloudsync.snapshot_hash", SettingPerTill},
		{"install.desktop_kiosk_overlay_provisioned", SettingPerTill},
		// ut-docs#2950: reviewed and kept shop-wide (reasons at
		// ShopWideSettingPrefixes).
		{"setup.restore_prompt_status", SettingShopWide},
		{"lan_discovery.till_id", SettingShopWide},
		{"fiscal.tse_provisioning_state", SettingShopWide},
		{"till.name", SettingShopWide},
		{"menu.restored_keys", SettingShopWide},
		{"no_such_family.key", SettingUnclassified},
		{"", SettingUnclassified},
	} {
		if got := SettingScope(tc.key); got != tc.want {
			t.Errorf("SettingScope(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// Per-till wins: a shop-wide prefix must never pull a per-till key into the
// write-through (and so onto the main till).
func TestSettingScope_PerTillWinsOverShopWide(t *testing.T) {
	for _, p := range PerTillSettingPrefixes {
		if SettingScope(p) != SettingPerTill || SettingScope(p+"x") != SettingPerTill {
			t.Errorf("per-till prefix %q not classified per-till", p)
		}
	}
}
