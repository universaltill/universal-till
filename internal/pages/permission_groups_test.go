package pages

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3132 — the /users/permissions guard tests. The permission page is
// only as honest as the catalog behind it: an action the code gates on but
// the page can't show is a permission nobody can grant, and an action with
// no label renders as a raw key. These tests read the real code and the
// real migrated DB so neither side can drift silently.

// dbPermissionActions returns every permission_actions row after the full
// migration set, via the same repo method the page itself reads.
func dbPermissionActions(t *testing.T) map[string]bool {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	grants, err := data.NewAuthRepo(db).ListRolePermissionMatrix(t.Context())
	if err != nil {
		t.Fatalf("list permission matrix: %v", err)
	}
	out := map[string]bool{}
	for _, g := range grants {
		out[g.Action] = true
	}
	if len(out) == 0 {
		t.Fatal("migrated DB has no permission_actions rows — the test would pass vacuously")
	}
	return out
}

// enLocaleKeys reads web/locales/en.json (the base locale guard-i18n.sh
// holds every other locale to).
func enLocaleKeys(t *testing.T) map[string]string {
	t.Helper()
	chdirRoot(t)
	raw, err := os.ReadFile(filepath.Join("web", "locales", "en.json"))
	if err != nil {
		t.Fatalf("read en.json: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse en.json: %v", err)
	}
	return m
}

// gatedActionSite is one string action a gate call in internal/pages names.
type gatedActionSite struct {
	Action string
	Pos    string
}

// collectGatedActions parses every non-test .go file under internal/pages
// (subpackages included) and returns each constant action string passed to
// a permission gate. Gates are discovered, not listed: the seed is any
// `<x>.Can(ctx, user, action)` call (auth.Service.Can, the one permission
// check everything bottoms out in); any function that forwards one of its
// own parameters into a gate's action slot is itself a gate for that
// parameter (canPerform → requirePage/checkOrElevate/checkStepUp/
// verifyElevationPIN, and any new top-level function or method), iterated
// to a fixed point. Known limit: a func literal bound to a local variable
// (`check := func(a string) bool { return canPerform(d, r, a) }`) is not
// discovered, so its call sites would be missed — use a named function. An action argument that is neither a string literal nor a
// package-level string constant is reported as unresolved, so a gate can't
// be hidden from this test behind a computed name.
func collectGatedActions(t *testing.T) (sites []gatedActionSite, unresolved []string) {
	t.Helper()
	chdirRoot(t)
	root := filepath.Join("internal", "pages")
	fset := token.NewFileSet()
	type pkgFiles struct {
		files  []*ast.File
		consts map[string]string
	}
	pkgs := map[string]*pkgFiles{}
	err := filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() {
			if de.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		dir := filepath.Dir(path)
		p := pkgs[dir]
		if p == nil {
			p = &pkgFiles{consts: map[string]string{}}
			pkgs[dir] = p
		}
		p.files = append(p.files, f)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if s, err := strconv.Unquote(lit.Value); err == nil {
								p.consts[name.Name] = s
							}
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("parse internal/pages: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no packages under internal/pages")
	}

	// gateArg maps a gate function name to the index of its action argument.
	gateArg := map[string]int{}
	gateIndex := func(call *ast.CallExpr) (int, bool) {
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			i, ok := gateArg[fn.Name]
			return i, ok
		case *ast.SelectorExpr:
			if fn.Sel.Name == "Can" && len(call.Args) == 3 {
				return 2, true
			}
			i, ok := gateArg[fn.Sel.Name]
			return i, ok
		}
		return 0, false
	}
	paramIndex := func(fd *ast.FuncDecl) map[string]int {
		out := map[string]int{}
		i := 0
		for _, field := range fd.Type.Params.List {
			if len(field.Names) == 0 {
				i++
				continue
			}
			for _, n := range field.Names {
				out[n.Name] = i
				i++
			}
		}
		return out
	}
	forEachFunc := func(visit func(fd *ast.FuncDecl, consts map[string]string)) {
		for _, p := range pkgs {
			for _, f := range p.files {
				for _, decl := range f.Decls {
					if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
						visit(fd, p.consts)
					}
				}
			}
		}
	}

	// Fixed point: discover every wrapper that forwards a parameter into a
	// gate's action slot.
	for changed := true; changed; {
		changed = false
		forEachFunc(func(fd *ast.FuncDecl, _ map[string]string) {
			if _, known := gateArg[fd.Name.Name]; known {
				return
			}
			params := paramIndex(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				idx, ok := gateIndex(call)
				if !ok || idx >= len(call.Args) {
					return true
				}
				if id, ok := call.Args[idx].(*ast.Ident); ok {
					if pi, isParam := params[id.Name]; isParam {
						if _, known := gateArg[fd.Name.Name]; !known {
							gateArg[fd.Name.Name] = pi
							changed = true
						}
					}
				}
				return true
			})
		})
	}
	for _, want := range []string{"canPerform", "requirePage", "checkOrElevate"} {
		if _, ok := gateArg[want]; !ok {
			t.Fatalf("gate discovery missed %s (found %v) — the scan is broken, not the code", want, gateArg)
		}
	}

	forEachFunc(func(fd *ast.FuncDecl, consts map[string]string) {
		params := paramIndex(fd)
		forwardIdx, isGate := gateArg[fd.Name.Name]
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			idx, ok := gateIndex(call)
			if !ok || idx >= len(call.Args) {
				return true
			}
			pos := fset.Position(call.Pos()).String()
			switch a := call.Args[idx].(type) {
			case *ast.BasicLit:
				if a.Kind == token.STRING {
					if s, err := strconv.Unquote(a.Value); err == nil {
						sites = append(sites, gatedActionSite{Action: s, Pos: pos})
						return true
					}
				}
			case *ast.Ident:
				if s, ok := consts[a.Name]; ok {
					sites = append(sites, gatedActionSite{Action: s, Pos: pos})
					return true
				}
				// A gate forwarding its own action parameter is the
				// wrapper itself, not a call site.
				if pi, ok := params[a.Name]; ok && isGate && pi == forwardIdx {
					return true
				}
			}
			unresolved = append(unresolved, pos)
			return true
		})
	})
	return sites, unresolved
}

// TestPermissionGuard_EveryGatedActionIsOnThePage: every action the code in
// internal/pages gates on has a permission_actions row (so /users/permissions
// renders a checkbox for it) and a translated label.
func TestPermissionGuard_EveryGatedActionIsOnThePage(t *testing.T) {
	actions := dbPermissionActions(t)
	keys := enLocaleKeys(t)
	sites, unresolved := collectGatedActions(t)
	if len(sites) < 20 {
		t.Fatalf("found only %d gate call sites — the scan is broken, not the code", len(sites))
	}
	for _, pos := range unresolved {
		t.Errorf("%s: permission gate called with a computed action — use a string literal or a package-level const so the permission page guard can see it", pos)
	}
	seen := map[string]bool{}
	for _, s := range sites {
		if seen[s.Action] {
			continue
		}
		seen[s.Action] = true
		if !actions[s.Action] {
			t.Errorf("%s: gates on %q, which has no permission_actions row — nobody can grant it on /users/permissions (add a migration)", s.Pos, s.Action)
		}
		if _, ok := keys["permissions.action."+s.Action]; !ok {
			t.Errorf("%s: gates on %q, but en.json has no permissions.action.%s label", s.Pos, s.Action, s.Action)
		}
	}
}

// TestPermissionGuard_CoreVisibleIfNamesAreActionsOrComposite: every
// VisibleIf name in a core uislot table is either a permission action or a
// composite predicate listed in menuCompositePredicates — which is what the
// page's "Unlocks" list is derived from.
func TestPermissionGuard_CoreVisibleIfNamesAreActionsOrComposite(t *testing.T) {
	actions := dbPermissionActions(t)
	for name, base := range menuCompositePredicates {
		if actions[name] {
			t.Errorf("menuCompositePredicates lists %q, which is a plain permission action — drop it from the map", name)
		}
		if base != "" && !actions[base] {
			t.Errorf("menuCompositePredicates maps %q to %q, which is not a permission action", name, base)
		}
		if _, ok := menuPredicates[name]; !ok {
			t.Errorf("menuCompositePredicates lists %q, which is not a registered menu predicate", name)
		}
	}
	checked := 0
	for table, entries := range coreUISlotTables() {
		for _, e := range entries {
			if e.VisibleIf == "" {
				continue
			}
			checked++
			if actions[e.VisibleIf] {
				continue
			}
			if _, ok := menuCompositePredicates[e.VisibleIf]; !ok {
				t.Errorf("uislot.%s entry %s: VisibleIf %q is neither a permission action nor listed in menuCompositePredicates", table, e.Key, e.VisibleIf)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("checked only %d VisibleIf names — coreUISlotTables lost a table", checked)
	}
}

// TestPermissionGroups_EveryActionInExactlyOneGroup: the group table places
// each DB action exactly once (an unplaced one would fall into the runtime
// "Other" group — this test is what makes CI catch that), names no action
// the DB lacks, and every group/action has its locale keys.
func TestPermissionGroups_EveryActionInExactlyOneGroup(t *testing.T) {
	actions := dbPermissionActions(t)
	keys := enLocaleKeys(t)
	placed := map[string]string{}
	seenGroup := map[string]bool{}
	for _, g := range permissionGroups {
		if g.Key == permissionOtherGroup {
			t.Errorf("group key %q is reserved for the runtime fallback", g.Key)
		}
		if seenGroup[g.Key] {
			t.Errorf("group %q declared twice", g.Key)
		}
		seenGroup[g.Key] = true
		if len(g.Actions) == 0 {
			t.Errorf("group %q is empty", g.Key)
		}
		for _, a := range g.Actions {
			if prev, dup := placed[a]; dup {
				t.Errorf("action %q is in both group %q and %q", a, prev, g.Key)
			}
			placed[a] = g.Key
			if !actions[a] {
				t.Errorf("group %q lists %q, which has no permission_actions row", g.Key, a)
			}
		}
	}
	var missing []string
	for a := range actions {
		if _, ok := placed[a]; !ok {
			missing = append(missing, a)
		}
	}
	sort.Strings(missing)
	for _, a := range missing {
		t.Errorf("permission action %q is in no group in permissionGroups — it renders under \"Other\"; place it (permission_groups.go)", a)
	}
	for _, g := range append(append([]permissionGroup{}, permissionGroups...), permissionGroup{Key: permissionOtherGroup}) {
		if _, ok := keys["permissions.group."+g.Key]; !ok {
			t.Errorf("en.json has no permissions.group.%s", g.Key)
		}
	}
	for a := range actions {
		if _, ok := keys["permissions.action_desc."+a]; !ok {
			t.Errorf("en.json has no permissions.action_desc.%s description", a)
		}
	}
	for _, k := range []string{"permissions.unlocks", "permissions.unlocks_separator"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("en.json has no %s", k)
		}
	}
}

// TestMenuUnlocksByAction_DerivedFromCoreTables pins the derivation: the
// Reports tile under reports, the rail's Stock entry under stock_management,
// the composite fiscal tiles folded under settings, and the derived
// administration tile never listed.
func TestMenuUnlocksByAction_DerivedFromCoreTables(t *testing.T) {
	got := menuUnlocksByAction()
	has := func(action, labelKey string) bool {
		for _, u := range got[action] {
			if u.LabelKey == labelKey {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ action, label string }{
		{"reports", "nav.reports"},
		{"catalog_management", "nav.designer"},
		{"catalog_management", "nav.items"},
		{"stock_management", "kiosk.inventory"},
		{"settings", "nav.settings"},
		{"settings", "fiscalregister.title"},
		{"stock_location_management", "locations.title"},
		{"plugin_management", "nav.plugins"},
	} {
		if !has(c.action, c.label) {
			t.Errorf("menuUnlocksByAction()[%q] lacks %s: %+v", c.action, c.label, got[c.action])
		}
	}
	for action, list := range got {
		seen := map[string]bool{}
		for _, u := range list {
			if u.LabelKey == "menu.group.administration" {
				t.Errorf("the derived administration tile must not be listed (found under %q)", action)
			}
			if seen[u.LabelKey] {
				t.Errorf("%q lists %s twice", action, u.LabelKey)
			}
			seen[u.LabelKey] = true
		}
	}
	if _, ok := got["administration"]; ok {
		t.Error("administration is a derived predicate, not an action")
	}
}
