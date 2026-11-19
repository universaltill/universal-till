package pages

import (
	"github.com/universaltill/universal-till/internal/auth"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5410: store.currency_display (auto|symbol|code) saved from the
// currency card through POST /api/settings/save.
func TestSettingsSave_CurrencyDisplay(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	defer httpx.SnapshotStateForTests()()

	// A bad value is a 400 (never clamped), before any PIN is asked for, and
	// writes nothing.
	for _, bad := range []string{"Symbol", "both", "1"} {
		rec := postForm(mux, "/api/settings/save", url.Values{"currency_display": {bad}}, &mgrUser)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("currency_display=%q = %d, want 400", bad, rec.Code)
		}
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyCurrencyDisplay); ok {
		t.Fatal("a rejected display mode must not be persisted")
	}

	// A cashier is asked for a manager PIN, nothing written.
	rec := postForm(mux, "/api/settings/save", url.Values{"currency_display": {"code"}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier save = %d, want the elevation prompt", rec.Code)
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyCurrencyDisplay); ok {
		t.Fatal("an un-elevated cashier must not change the display mode")
	}

	for _, mode := range []string{"code", "symbol", "auto"} {
		rec := postForm(mux, "/api/settings/save", url.Values{"currency_display": {mode}}, &mgrUser)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("currency_display=%q = %d, want 204", mode, rec.Code)
		}
		if v, _, _ := d.Settings.Get(t.Context(), common.KeyCurrencyDisplay); v != mode {
			t.Fatalf("stored = %q, want %q", v, mode)
		}
		if got := d.CurrentState().CurrencyDisplay; got != mode {
			t.Fatalf("state = %q, want %q", got, mode)
		}
		if got := httpx.CurrencyDisplayMode(); got != mode {
			t.Fatalf("httpx published %q, want %q (live, no restart)", got, mode)
		}
	}

	// The save is audited with the new mode.
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='store_settings_saved' AND data_json LIKE '%"currency_display":"auto"%'`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("no store_settings_saved audit row naming currency_display (n=%d err=%v)", n, err)
	}

	// Posting only the currency leaves the chosen mode alone.
	for _, v := range []url.Values{{"currency_display": {"code"}}, {"currency": {"AED"}}} {
		if rec := postForm(mux, "/api/settings/save", v, &mgrUser); rec.Code != http.StatusNoContent {
			t.Fatalf("save %v = %d", v, rec.Code)
		}
	}
	if got := d.CurrentState().CurrencyDisplay; got != "code" {
		t.Fatalf("a currency-only save reset the display mode to %q", got)
	}
}

func TestSettingsUpsert_CurrencyDisplay(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	defer httpx.SnapshotStateForTests()()

	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyCurrencyDisplay}, "value": {"nope"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mode via upsert = %d, want 400", rec.Code)
	}
	rec = postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyCurrencyDisplay}, "value": {"symbol"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d, want 204", rec.Code)
	}
	if d.CurrentState().CurrencyDisplay != "symbol" || httpx.CurrencyDisplayMode() != "symbol" {
		t.Fatalf("upsert did not reach state (%q) and httpx (%q)", d.CurrentState().CurrencyDisplay, httpx.CurrencyDisplayMode())
	}
}

func TestLoadState_CurrencyDisplayDefaultsToAuto(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	st := d.CurrentState()
	st.CurrencyDisplay = ""
	if err := common.SaveState(t.Context(), d.Settings, st); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyCurrencyDisplay); v != "auto" {
		t.Fatalf("empty state saved %q, want auto", v)
	}
}

// ut-docs#5410: every publisher of the shop currency also publishes the
// display mode. httpx.InitCurrency may be called only by
// common.InitCurrencyFromState and the boot/re-derive publisher
// (publishCachedSettings, which pairs it with InitCurrencyDisplay).
func TestInitCurrencyOnlyThroughStateHelper(t *testing.T) {
	chdirRoot(t)
	for _, dir := range []string{"internal/pages", "internal/recovery", "internal/app", "cmd"} {
		fset := token.NewFileSet()
		err := filepath.WalkDir(dir, func(path string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				calls, pairs := false, false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == "httpx" {
							switch sel.Sel.Name {
							case "InitCurrency":
								calls = true
							case "InitCurrencyDisplay":
								pairs = true
							}
						}
					}
					return true
				})
				if calls && !pairs {
					t.Errorf("%s: %s calls httpx.InitCurrency without InitCurrencyDisplay — use common.InitCurrencyFromState(st) (ut-docs#5410)", path, fn.Name.Name)
				}
			}
			return nil
		})
		if err != nil && !strings.Contains(err.Error(), "no such file") {
			t.Fatal(err)
		}
	}
}

// The control has to be on the page: select with the three modes, the stored
// one selected, examples built from the shop currency (no digits in the
// translation strings), and the example line under it.
func TestSettingsPageRendersCurrencyDisplayControl(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	defer httpx.SnapshotStateForTests()()
	httpx.InitCurrency("AED")
	httpx.InitCurrencyDisplay("auto")
	if rec := postForm(mux, "/api/settings/save", url.Values{"currency_display": {"code"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("seed = %d", rec.Code)
	}
	httpx.InitCurrency("AED") // the seed save republishes the (GBP) test state

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`name="currency_display"`, `id="currency-display"`, `data-lock-region="settings-currency-display"`,
		`aria-describedby="currency-display-help currency-display-example"`,
		`data-testid="currency-display-example"`,
		`value="auto"`, `value="symbol"`, `value="code" data-example="AED 12.50" selected`,
		"Symbol (12.50 د.إ)", "Code (AED 12.50)", "Automatic (AED 12.50)",
		// review: the Apply button is named by its card (two Apply buttons).
		`aria-labelledby="currency-display-apply currency-display-title"`, `id="currency-display-title"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page is missing %q", want)
		}
	}
	// review: dir="auto" on the example line left-aligned it in ar/fa (a
	// <bdi> is skipped by auto detection); it must inherit the page direction.
	if i := strings.Index(body, `id="currency-display-example"`); i < 0 || strings.Contains(body[i:i+120], `dir="auto"`) {
		t.Error("currency-display-example must not set dir=auto")
	}
}
