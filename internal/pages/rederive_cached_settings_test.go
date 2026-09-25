package pages

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// TestSyncPull_AdminSyncedCachedSettingsTakeEffectWithoutRestart
// (ut-docs#2790): every shop-wide setting a till caches in a process global
// at boot (httpx.Init*/Set*) must follow an admin-bundle pull from the main
// till live — the additional till used to keep its boot-time
// sale.order_type_prompt (and default locale, locale generation) until the
// next restart, so the owner's "ask dine-in/takeaway at Pay" never reached
// the Windows till even though its DB already held at_pay.
//
// Drives the real pull tick against a real main-till server, with the same
// re-derive hook pages.Init hands StartSyncPull.
func TestSyncPull_AdminSyncedCachedSettingsTakeEffectWithoutRestart(t *testing.T) {
	cases := []struct {
		key, value string
		live       func() string
	}{
		{data.OrderTypePromptModeKey, data.OrderTypePromptModeAtPay, func() string {
			return httpx.FuncsFor("en")["ordertypepromptmode"].(func() string)()
		}},
		{common.KeyLocale, "de", httpx.DefaultLocale},
		{common.KeyLocaleGeneration, "7", func() string {
			return strconv.FormatInt(httpx.LocaleGeneration(), 10)
		}},
		{common.KeyIdleLock, "3", func() string {
			return strconv.FormatInt(httpx.FuncsFor("en")["idlelocksecs"].(func() int64)(), 10)
		}},
	}
	want := map[string]string{
		data.OrderTypePromptModeKey: data.OrderTypePromptModeAtPay,
		common.KeyLocale:            "de",
		common.KeyLocaleGeneration:  "7",
		common.KeyIdleLock:          "180", // minutes → seconds
	}

	// Boot-time values the replica starts from; restored afterwards so the
	// process globals don't leak into other tests.
	httpx.InitOrderTypePromptMode("")
	httpx.SetDefaultLocale("en")
	httpx.SetLocaleGeneration(0)
	httpx.InitIdleLock(0)
	t.Cleanup(func() {
		httpx.InitOrderTypePromptMode("")
		httpx.SetDefaultLocale("en")
		httpx.SetLocaleGeneration(0)
		httpx.InitIdleLock(0)
	})

	primary := newPullTestPrimary(t)
	ctx := t.Context()
	for _, c := range cases {
		for _, p := range data.PerTillSettingPrefixes {
			if strings.HasPrefix(c.key, p) {
				t.Fatalf("%s is per-till, not admin-synced — wrong case for this test", c.key)
			}
		}
		if err := primary.dp.Settings.Set(ctx, c.key, c.value); err != nil {
			t.Fatal(err)
		}
	}

	replica := newPullTestReplica(t, primary.server.URL)
	replica.Engine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	replica.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	replica.AuthSvc = auth.NewService(replica.Db)
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}
	// authDisabled=false: the idle-lock timer is only published when
	// sessions are enforced, as in production.
	rederive := newRederiveSettings(replica, false, i18n)

	syncPullTick(ctx, replica, &http.Client{Timeout: 5 * time.Second}, rederive)

	for _, c := range cases {
		if v, _, _ := replica.Settings.Get(ctx, c.key); v != c.value {
			t.Fatalf("%s did not sync to the replica DB (got %q) — fixture problem, not the bug", c.key, v)
		}
		if got := c.live(); got != want[c.key] {
			t.Errorf("%s: live value after admin pull = %q, want %q — the till keeps the boot-time value until restart (ut-docs#2790)", c.key, got, want[c.key])
		}
	}
}

// cachedSettingsPublisher is the ONE function that republishes every
// settings-derived process global, called by both boot (Init) and the
// re-derive hook (ut-docs#2790).
const cachedSettingsPublisher = "publishCachedSettings"

// notSettingsDerived are httpx's Init*/Set* globals that are NOT derived
// from a settings row, so they have nothing to re-derive after a sync.
var notSettingsDerived = map[string]string{
	"InitI18n":           "wires the translator itself; the default-locale half is republished via SetDefaultLocale",
	"InitKiosk":          "UT_KIOSK env var, not a setting",
	"InitRailAmendments": "wires a live accessor (reads Deps on every render), not a cached value",
}

// TestCachedSettingsGlobals_AllPublishedByOneHelper (ut-docs#2790 guard):
// every httpx Init*/Set* process global must either be republished by
// publishCachedSettings (so boot AND the post-sync re-derive both reach it)
// or be listed in notSettingsDerived with a reason; and pages.Init / the
// re-derive must not call them directly around the helper, where a new one
// would drift out of the re-derive again.
func TestCachedSettingsGlobals_AllPublishedByOneHelper(t *testing.T) {
	chdirRoot(t)
	root := "."
	fset := token.NewFileSet()

	// 1. Every Init*/Set* func httpx exports.
	var globals []string
	httpxPkgs, err := parser.ParseDir(fset, filepath.Join(root, "internal/httpx"), func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range httpxPkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !fn.Name.IsExported() {
					continue
				}
				if n := fn.Name.Name; strings.HasPrefix(n, "Init") || strings.HasPrefix(n, "Set") {
					globals = append(globals, n)
				}
			}
		}
	}
	sort.Strings(globals)
	if len(globals) == 0 {
		t.Fatal("found no httpx Init*/Set* funcs — the scan is broken")
	}

	// 2. What publishCachedSettings reaches (directly, or via
	// loadLocaleGeneration), and what else in init.go calls them.
	initFile, err := parser.ParseFile(fset, filepath.Join(root, "internal/pages/init.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	inHelper := map[string]bool{}
	outside := map[string][]string{} // httpx func → enclosing funcs outside the helper
	var sawHelper bool
	for _, decl := range initFile.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		isHelper := fn.Name.Name == cachedSettingsPublisher
		sawHelper = sawHelper || isHelper
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				if id, ok := f.X.(*ast.Ident); ok && id.Name == "httpx" {
					name = f.Sel.Name
				}
			case *ast.Ident:
				if isHelper && f.Name == "loadLocaleGeneration" {
					inHelper["SetLocaleGeneration"] = true
				}
			}
			if !strings.HasPrefix(name, "Init") && !strings.HasPrefix(name, "Set") {
				return true
			}
			if isHelper {
				inHelper[name] = true
			} else {
				outside[name] = append(outside[name], fn.Name.Name)
			}
			return true
		})
	}
	if !sawHelper {
		t.Fatalf("init.go has no %s — boot and the post-sync re-derive must share one publisher (ut-docs#2790)", cachedSettingsPublisher)
	}

	for _, g := range globals {
		if _, skip := notSettingsDerived[g]; skip {
			continue
		}
		if !inHelper[g] {
			t.Errorf("httpx.%s is a cached process global not republished by %s — a synced/edited setting behind it would stay stale until restart (ut-docs#2790). Add it to the helper, or to notSettingsDerived with a reason.", g, cachedSettingsPublisher)
		}
		if callers := outside[g]; len(callers) > 0 {
			t.Errorf("init.go calls httpx.%s directly from %v — publish it only through %s so boot and re-derive can't drift", g, callers, cachedSettingsPublisher)
		}
	}
	for g := range notSettingsDerived {
		found := false
		for _, have := range globals {
			found = found || have == g
		}
		if !found {
			t.Errorf("notSettingsDerived lists httpx.%s, which no longer exists — drop it", g)
		}
	}
}
