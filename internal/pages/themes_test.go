package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/pages/common"
)

func themeTestDeps(t *testing.T) (*common.Deps, string, string) {
	t.Helper()

	builtin := t.TempDir()
	pluginBase := t.TempDir()
	origBuiltin, origPlugin := builtinThemesDir, pluginThemesDir
	builtinThemesDir, pluginThemesDir = builtin, pluginBase
	t.Cleanup(func() { builtinThemesDir, pluginThemesDir = origBuiltin, origPlugin })

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER NOT NULL DEFAULT 1, runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '');`,
		`CREATE TABLE plugin_entries (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, type TEXT, key TEXT, route TEXT, label TEXT, menu_group TEXT, config_json TEXT, sort_order INTEGER DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return &common.Deps{Db: db}, builtin, pluginBase
}

func TestThemesHandler_ServesBuiltinAndPluginCSS(t *testing.T) {
	d, builtin, pluginBase := themeTestDeps(t)

	// Built-in theme on disk.
	if err := os.WriteFile(filepath.Join(builtin, "monarch.css"), []byte(":root{--brand:#000}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Plugin theme: DB entry + CSS file in the plugin dir.
	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.x.midnight','Midnight','1.0.0',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,config_json,is_active)
		VALUES('e1','com.x.midnight','theme','midnight','Midnight','{"css":"assets/theme.css"}',1)`); err != nil {
		t.Fatal(err)
	}
	cssPath := filepath.Join(pluginBase, "com.x.midnight", "1.0.0", "assets", "theme.css")
	if err := os.MkdirAll(filepath.Dir(cssPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cssPath, []byte("body{background:#0f172a}"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerThemes(mux, d)

	get := func(path string) (int, string, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), rec.Header().Get("Content-Type")
	}

	// Built-in.
	if code, body, ct := get("/themes/monarch.css"); code != 200 || body != ":root{--brand:#000}" || ct != "text/css; charset=utf-8" {
		t.Errorf("builtin: code=%d ct=%q body=%q", code, ct, body)
	}
	// Plugin-provided.
	if code, body, _ := get("/themes/midnight.css"); code != 200 || body != "body{background:#0f172a}" {
		t.Errorf("plugin theme: code=%d body=%q", code, body)
	}
	// Unknown -> 404.
	if code, _, _ := get("/themes/nope.css"); code != 404 {
		t.Errorf("unknown theme: code=%d, want 404", code)
	}
	// Path traversal / non-css rejected.
	for _, p := range []string{"/themes/..%2f..%2fsecret.css", "/themes/midnight.txt"} {
		if code, _, _ := get(p); code != 404 {
			t.Errorf("%s: code=%d, want 404", p, code)
		}
	}

	// availableThemes lists built-in themes first (sorted), then plugin themes.
	// Don't hardcode the built-in count: the embedded set grows as curated
	// built-in themes are added under web/public/themes (fresh/amber/slate/…),
	// which the fallback FS unions with the disk dir. Assert the contract
	// instead — monarch is a built-in, midnight is the plugin theme, built-ins
	// are sorted and all precede any plugin theme.
	opts := availableThemes(t.Context(), d)
	var builtinKeys []string
	firstPluginIdx := len(opts)
	var midnight *ThemeOption
	for i := range opts {
		o := opts[i]
		if o.Source == "built-in" {
			if i > firstPluginIdx {
				t.Errorf("built-in %q appears after a plugin theme; built-ins must be first: %+v", o.Key, opts)
			}
			builtinKeys = append(builtinKeys, o.Key)
			continue
		}
		if i < firstPluginIdx {
			firstPluginIdx = i
		}
		if o.Key == "midnight" {
			midnight = &opts[i]
		}
	}
	if !sort.StringsAreSorted(builtinKeys) {
		t.Errorf("built-in themes not sorted: %v", builtinKeys)
	}
	if !slices.Contains(builtinKeys, "monarch") {
		t.Errorf("expected monarch among built-in themes, got %v", builtinKeys)
	}
	if midnight == nil || midnight.Source != "com.x.midnight" {
		t.Errorf("expected midnight plugin theme with source com.x.midnight, got %+v", opts)
	}
}

// TestThemesHandler_FallsBackToEmbeddedDefaultWhenDiskDirMissing guards the
// fix itself: built-in theme resolution used to be a plain os.ReadDir/
// os.Stat against builtinThemesDir, so a packaged install where
// web/public/themes is empty or absent on disk (the common case — it's
// only ever populated by an upload/customization handler) served zero
// built-in themes and 404'd every /themes/{name}.css request. builtin here
// is a real, empty tempdir (nothing written to it, unlike themeTestDeps'
// other tests, and equivalent to a non-existent directory for fallbackFS's
// ReadDir/Open — both branches are handled identically) — the binary's
// embedded default (monarch.css) must still resolve.
func TestThemesHandler_FallsBackToEmbeddedDefaultWhenDiskDirMissing(t *testing.T) {
	d, _, _ := themeTestDeps(t)

	mux := http.NewServeMux()
	registerThemes(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/themes/monarch.css", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the embedded default theme to be served, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatalf("expected non-empty CSS body")
	}

	opts := availableThemes(t.Context(), d)
	found := false
	for _, o := range opts {
		if o.Key == "monarch" && o.Source == "built-in" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the embedded default theme to be listed, got %+v", opts)
	}
}

// TestThemesHandler_MonochromeAndDarkAreBuiltIn guards ut-docs#2176: a
// black-and-white/high-contrast theme and a dark theme, both built-in (no
// plugin/marketplace install required), each selectable from Settings →
// Theme like the four pre-existing built-ins. Empty disk dir like
// TestThemesHandler_FallsBackToEmbeddedDefaultWhenDiskDirMissing above, so
// this exercises the binary's real embedded web/public/themes/*.css, not a
// throwaway fixture — a future edit that breaks the embed or renames a key
// fails this, not just a manual look at Settings.
func TestThemesHandler_MonochromeAndDarkAreBuiltIn(t *testing.T) {
	d, _, _ := themeTestDeps(t)

	mux := http.NewServeMux()
	registerThemes(mux, d)

	opts := availableThemes(t.Context(), d)
	wantKeys := map[string]bool{"monochrome": false, "dark": false}
	for _, o := range opts {
		if _, ok := wantKeys[o.Key]; ok {
			if o.Source != "built-in" {
				t.Errorf("%s: Source = %q, want \"built-in\" (no plugin install should be required)", o.Key, o.Source)
			}
			wantKeys[o.Key] = true
		}
	}
	for key, found := range wantKeys {
		if !found {
			t.Errorf("expected built-in theme %q among availableThemes, got %+v", key, opts)
		}
	}

	// Each theme key collides with neither the other three pre-existing
	// built-ins nor the real ut-plugin-theme-midnight plugin's own "midnight"
	// key (themes_test.go's own ServesBuiltinAndPluginCSS test above uses
	// that exact key) — two entries sharing a key would render as two
	// visually-identical rows in the Settings <select>.
	seen := map[string]int{}
	for _, o := range opts {
		seen[o.Key]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("theme key %q appears %d times, want at most 1: %+v", key, n, opts)
		}
	}

	// Required tokens present in each new theme's own CSS (not just
	// inherited from app.css's :root defaults) — --accent/--focus-border/
	// --bg/--surface/--text/--muted is the same token set every existing
	// theme file defines or relies on; the WCAG ratios themselves are
	// documented and verified in the CLI review, not re-derived by this
	// test (a computed hex-math ratio check would just re-encode the same
	// design constants the CSS comments already record).
	wantTokens := []string{"--accent", "--focus-border", "--bg", "--surface", "--text", "--muted"}
	for _, key := range []string{"monochrome", "dark"} {
		req := httptest.NewRequest(http.MethodGet, "/themes/"+key+".css", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: GET /themes/%s.css = %d, want 200", key, key, rec.Code)
		}
		body := rec.Body.String()
		for _, tok := range wantTokens {
			if !strings.Contains(body, tok) {
				t.Errorf("%s.css: missing token %q", key, tok)
			}
		}
	}

	// Monochrome specifically: no colour accent carries meaning on its own
	// (ut-docs#2176 acceptance criteria) — the semantic tokens collapse to
	// black rather than leaving app.css's default green/red/amber.
	monoReq := httptest.NewRequest(http.MethodGet, "/themes/monochrome.css", nil)
	monoRec := httptest.NewRecorder()
	mux.ServeHTTP(monoRec, monoReq)
	monoBody := monoRec.Body.String()
	for _, tok := range []string{"--success", "--danger", "--warning"} {
		if !strings.Contains(monoBody, tok+": #000000") {
			t.Errorf("monochrome.css: expected %s: #000000 (no colour-only meaning), got body:\n%s", tok, monoBody)
		}
	}
}

// TestSemanticTintBackgrounds_MonochromeIsColourless guards ut-docs#2192:
// app.css used to paint several semantic-tint BACKGROUNDS (the sale-screen
// success/error notice, the "needs attention" tag, the sync banner, the
// catalogue-import row/block warning+success notices, and the journal
// cross-till replica notice) as literal rgba() green/red/amber, so no
// theme — monochrome included — could reach them; a merchant on Monochrome
// still saw colour behind black, legible text, undercutting the theme's own
// "no colour accent carries meaning" identity (already tested for the
// --success/--danger/--warning ink tokens by
// TestThemesHandler_MonochromeAndDarkAreBuiltIn above, which this doesn't
// duplicate). Reads the real embedded assets via registerStatic/
// registerThemes, not a copy of the CSS, so an edit to either file is what
// this test actually exercises.
func TestSemanticTintBackgrounds_MonochromeIsColourless(t *testing.T) {
	mux := http.NewServeMux()
	registerStatic(mux)
	registerThemes(mux, &common.Deps{})

	get := func(path string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		return rec.Body.String()
	}

	appCSS := get("/public/app.css")
	for _, tok := range []string{"--success-tint:", "--danger-tint:", "--warning-tint:"} {
		if !strings.Contains(appCSS, tok) {
			t.Errorf("app.css: missing base token %q", tok)
		}
	}
	// None of the affected rules may still paint a hardcoded semantic rgba()
	// as a BACKGROUND — every one of them must read a --*-tint var instead.
	// (Their borders are explicitly out of scope for this card and are
	// expected to still carry the literal rgba(...,.35) — this only checks
	// `background:`.)
	for _, bad := range []string{
		"background: rgba(22, 163, 74", "background: rgba(220, 38, 38", "background: rgba(217, 119, 6",
	} {
		if strings.Contains(appCSS, bad) {
			t.Errorf("app.css: found a hardcoded tint background %q — should read a --*-tint var instead", bad)
		}
	}
	for _, want := range []string{
		".tag.warn { background: var(--warning-tint)",
		".pos-notice.success { background: var(--success-tint)",
		".pos-notice.error { background: var(--danger-tint)",
		"background: var(--warning-tint); color: var(--warning); }\n.journal table",
		"tr.row-warn td { background: var(--warning-tint); }",
		".notice-block-warn { background: var(--warning-tint);",
		".notice-block-success { background: var(--success-tint);",
		".sync-banner { background: var(--warning-tint);",
	} {
		if !strings.Contains(appCSS, want) {
			t.Errorf("app.css: expected to find %q", want)
		}
	}

	// Monochrome overrides all three to a neutral grayscale wash — no hue.
	monoCSS := get("/themes/monochrome.css")
	for _, tok := range []string{"--success-tint: rgba(0, 0, 0,", "--danger-tint: rgba(0, 0, 0,", "--warning-tint: rgba(0, 0, 0,"} {
		if !strings.Contains(monoCSS, tok) {
			t.Errorf("monochrome.css: expected %q, got body:\n%s", tok, monoCSS)
		}
	}

	// Every other built-in theme (amber/fresh/monarch/slate/dark) must NOT
	// override the tint tokens — the four light themes and dark.css all
	// inherit app.css's defaults, so they render byte-identical to before
	// this change (dark.css deliberately overrides --danger for contrast,
	// per its own #2176 review comment, but never the tint).
	for _, key := range []string{"amber", "fresh", "monarch", "slate", "dark"} {
		css := get("/themes/" + key + ".css")
		for _, tok := range []string{"--success-tint", "--danger-tint", "--warning-tint"} {
			if strings.Contains(css, tok) {
				t.Errorf("%s.css: unexpectedly overrides %q — expected it to inherit app.css's default", key, tok)
			}
		}
	}
}

func TestResolvePluginThemeCSS_RejectsEscapingConfig(t *testing.T) {
	d, _, _ := themeTestDeps(t)
	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.x.evil','Evil','1.0.0',1)`); err != nil {
		t.Fatal(err)
	}
	// css path tries to escape the plugin base dir.
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,config_json,is_active)
		VALUES('e1','com.x.evil','theme','evil','Evil','{"css":"../../../../etc/passwd"}',1)`); err != nil {
		t.Fatal(err)
	}
	if got := resolvePluginThemeCSS(t.Context(), d, "evil"); got != "" {
		t.Errorf("expected empty path for escaping config, got %q", got)
	}
}
