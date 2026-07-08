package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

func pluginPageTestDeps(t *testing.T) (*common.Deps, string) {
	t.Helper()
	chdirRoot(t)

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE plugin_entries (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, type TEXT, key TEXT, route TEXT, label TEXT, icon_path TEXT, menu_group TEXT, parent_page_key TEXT, target_action TEXT, trigger_event TEXT, config_json TEXT, sort_order INTEGER DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	pluginBase := t.TempDir()
	orig := pluginPagesDir
	pluginPagesDir = pluginBase
	t.Cleanup(func() { pluginPagesDir = orig })

	return &common.Deps{Db: db, State: common.RuntimeState{Theme: "monarch"}}, pluginBase
}

func TestPluginPage_RendersContentBundle(t *testing.T) {
	d, base := pluginPageTestDeps(t)

	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version) VALUES('com.x.faq','FAQ Plugin','1.2.0')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e1','com.x.faq','page','faq-page','/plugin/faq','Help / FAQ')`); err != nil {
		t.Fatal(err)
	}
	contentDir := filepath.Join(base, "com.x.faq", "1.2.0", "content")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := `{"locale":"en-US","rtl":false,
		"categories":[{"id":"general","name":"General","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"general","question":"How do I scan?","answer":"Point and shoot.","sort_order":1}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/plugin/faq", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugin/faq = %d (%s)", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	body := rec.Body.String()
	for _, want := range []string{"Help / FAQ", "General", "How do I scan?", "Point and shoot."} {
		if !strings.Contains(body, want) {
			t.Errorf("plugin page missing %q", want)
		}
	}
	// It must NOT be the home page.
	if strings.Contains(body, "kiosk-checkout-start") {
		t.Error("plugin route rendered the home page")
	}
}

func TestPluginPage_UnknownRouteIs404(t *testing.T) {
	d, _ := pluginPageTestDeps(t)
	mux := http.NewServeMux()
	registerPluginPages(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/plugin/nope", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin route = %d, want 404", rec.Code)
	}
}

func TestPluginPage_StaticHTMLFallback(t *testing.T) {
	d, base := pluginPageTestDeps(t)

	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version) VALUES('com.x.static','Static','0.1.0')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e1','com.x.static','page','home','/plugin/static','Static Page')`); err != nil {
		t.Fatal(err)
	}
	contentDir := filepath.Join(base, "com.x.static", "0.1.0", "content")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contentDir, "index.html"), []byte("<h2>Hello from plugin</h2>"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)
	req := httptest.NewRequest(http.MethodGet, "/plugin/static", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Hello from plugin") {
		t.Fatalf("static plugin page: code=%d body missing embed", rec.Code)
	}
}

func TestPluginButtons_PartialAndAction(t *testing.T) {
	d, _ := pluginPageTestDeps(t)

	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version) VALUES('com.x.drawer','Drawer','1.0.0')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,trigger_event) VALUES('b1','com.x.drawer','button','open-drawer','Open Drawer','drawer.open')`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)

	// Partial renders the button with its action endpoint.
	req := httptest.NewRequest(http.MethodGet, "/ui/plugin-buttons", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Open Drawer") ||
		!strings.Contains(body, "/api/plugins/entries/com.x.drawer/open-drawer/action") {
		t.Fatalf("plugin buttons partial wrong: code=%d body=%s", rec.Code, body[:min(400, len(body))])
	}

	// Pressing the button publishes its trigger event.
	req2 := httptest.NewRequest(http.MethodPost, "/api/plugins/entries/com.x.drawer/open-drawer/action", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("button action = %d (%s)", rec2.Code, rec2.Body.String())
	}
	resp := rec2.Body.String()
	if !strings.Contains(resp, `"success":true`) || !strings.Contains(resp, `"event":"drawer.open"`) {
		t.Errorf("action response = %s", resp)
	}

	// Unknown button -> 404.
	req3 := httptest.NewRequest(http.MethodPost, "/api/plugins/entries/com.x.drawer/nope/action", nil)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Errorf("unknown button action = %d, want 404", rec3.Code)
	}
}
