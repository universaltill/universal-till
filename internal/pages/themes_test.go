package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
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

	// availableThemes lists both, built-in first.
	opts := availableThemes(t.Context(), d)
	if len(opts) != 2 || opts[0].Key != "monarch" || opts[0].Source != "built-in" ||
		opts[1].Key != "midnight" || opts[1].Source != "com.x.midnight" {
		t.Errorf("availableThemes = %+v", opts)
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
