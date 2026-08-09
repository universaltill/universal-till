package pages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// bundleWithChecksum mirrors ut-plugin-faq's scripts/checksum.py: the
// checksum covers the JSON with its own checksum_sha256 value zeroed to a
// same-length placeholder.
func bundleWithChecksum(t *testing.T, jsonWithEmptyChecksum string) string {
	t.Helper()
	zeroed := strings.Replace(jsonWithEmptyChecksum, `"checksum_sha256":""`, `"checksum_sha256":"`+strings.Repeat("0", 64)+`"`, 1)
	if zeroed == jsonWithEmptyChecksum {
		t.Fatal(`bundleWithChecksum: fixture must contain "checksum_sha256":""`)
	}
	sum := sha256.Sum256([]byte(zeroed))
	digest := hex.EncodeToString(sum[:])
	return strings.Replace(zeroed, strings.Repeat("0", 64), digest, 1)
}

func pluginPageTestDeps(t *testing.T) (*common.Deps, string) {
	t.Helper()
	chdirRoot(t)

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER NOT NULL DEFAULT 1, runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '');`,
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

func TestPluginPage_KeywordsAreSearchable(t *testing.T) {
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
	// Neither the question nor the answer mentions "barcode" — only the
	// keywords array does. The rendered data-search haystack must still
	// carry it, or a search for "barcode" would find nothing.
	bundle := `{"locale":"en-US","rtl":false,
		"categories":[{"id":"general","name":"General","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"general","question":"How do I add an item?","answer":"Tap the tile.","sort_order":1,"keywords":["barcode","scan"]}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)
	req := httptest.NewRequest(http.MethodGet, "/plugin/faq", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugin/faq = %d (%s)", rec.Code, body[:min(300, len(body))])
	}
	if !strings.Contains(body, `id="plugin-content-search"`) {
		t.Error("expected a search input on the content page")
	}
	if !strings.Contains(body, `data-search="how do i add an item? tap the tile. barcode scan"`) {
		t.Errorf("expected data-search to include the keywords, body=%s", body[:min(1200, len(body))])
	}
}

// Every other fixture in this file sets "rtl":false — none of them would
// catch the RTL wiring breaking. A bundle with "rtl":true must set dir="rtl"
// on the content card (web/ui/pages/plugin_content.html); a bundle without
// it must not.
func TestPluginPage_RTLBundleSetsDirAttribute(t *testing.T) {
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
	bundle := `{"locale":"fa-IR","rtl":true,
		"categories":[{"id":"general","name":"عمومی","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"general","question":"چطور اسکن کنم؟","answer":"نشانه‌گیری و شلیک کنید.","sort_order":1}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "fa-IR.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)
	req := httptest.NewRequest(http.MethodGet, "/plugin/faq?lang=fa", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugin/faq?lang=fa = %d (%s)", rec.Code, body[:min(300, len(body))])
	}
	if !strings.Contains(body, `class="card plugin-content" dir="rtl"`) {
		t.Errorf("expected dir=\"rtl\" on an RTL bundle, body=%s", body[:min(1200, len(body))])
	}
}

func TestPluginPage_ChecksumValidBundleRenders(t *testing.T) {
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
	fixture := `{"locale":"en-US","checksum_sha256":"","rtl":false,
		"categories":[{"id":"general","name":"General","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"general","question":"How do I scan?","answer":"Point and shoot.","sort_order":1}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(bundleWithChecksum(t, fixture)), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)
	req := httptest.NewRequest(http.MethodGet, "/plugin/faq", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "How do I scan?") {
		t.Fatalf("valid-checksum bundle: code=%d body=%s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
}

func TestPluginPage_ChecksumMismatchRefusesToRender(t *testing.T) {
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
	fixture := `{"locale":"en-US","checksum_sha256":"","rtl":false,
		"categories":[{"id":"general","name":"General","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"general","question":"How do I scan?","answer":"Point and shoot.","sort_order":1}]}`
	signed := bundleWithChecksum(t, fixture)
	// Corrupt the content AFTER signing — same shape as on-disk bit-rot or a
	// hand-edit, the checksum no longer matches.
	tampered := strings.Replace(signed, "Point and shoot.", "Point and shoot!!", 1)
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)
	req := httptest.NewRequest(http.MethodGet, "/plugin/faq", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// loadContentBundle refuses the corrupted bundle; renderPluginPage falls
	// through to the "ships no page content" empty state rather than the
	// (possibly tampered) FAQ text.
	if strings.Contains(rec.Body.String(), "Point and shoot") {
		t.Fatalf("rendered content from a checksum-mismatched bundle: %s", rec.Body.String())
	}
}

func TestPluginPage_UnavailableLocaleShowsFallbackNoticeAndMeta(t *testing.T) {
	d, base := pluginPageTestDeps(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

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
	bundle := `{"locale":"en-US","version":"1.4.0","rtl":false,
		"categories":[{"id":"general","name":"General","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"general","question":"How do I scan?","answer":"Point and shoot.","sort_order":1,"last_updated":"2026-06-01"},
		               {"id":"q2","category":"general","question":"How do I refund?","answer":"Use the journal.","sort_order":2,"last_updated":"2026-07-13"}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)

	// ja-JP isn't shipped by this plugin (only en-US) — must fall back to
	// en-US and say so, not render as if ja-JP were actually available.
	req := httptest.NewRequest(http.MethodGet, "/plugin/faq?lang=ja-JP", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugin/faq?lang=ja-JP = %d (%s)", rec.Code, body[:min(300, len(body))])
	}
	if !strings.Contains(body, "available in your language yet") {
		t.Errorf("expected a fallback notice for an unshipped locale, body=%s", body[:min(600, len(body))])
	}
	// The most recent of the two entries' last_updated wins, not the first.
	if !strings.Contains(body, "1.4.0") || !strings.Contains(body, "2026-07-13") {
		t.Errorf("expected version/last-updated metadata (v1.4.0, 2026-07-13), body=%s", body[:min(600, len(body))])
	}
}

func TestPluginPage_MatchedLocaleHasNoFallbackNotice(t *testing.T) {
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
	bundle := `{"locale":"en-US","version":"1.4.0","rtl":false,
		"categories":[{"id":"general","name":"General","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"general","question":"How do I scan?","answer":"Point and shoot.","sort_order":1,"last_updated":"2026-06-01"}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/plugin/faq?lang=en-US", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugin/faq?lang=en-US = %d (%s)", rec.Code, body[:min(300, len(body))])
	}
	if strings.Contains(body, "available in your language yet") {
		t.Errorf("matched locale should not show a fallback notice, body=%s", body[:min(600, len(body))])
	}
}

// A regional variant of the SAME language (en-GB requested, only en-US
// shipped) is still "your language" — must not trigger the fallback
// notice, even though a different regional file is served.
func TestPluginPage_SameBaseLanguageDifferentRegionHasNoFallbackNotice(t *testing.T) {
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
	bundle := `{"locale":"en-US","version":"1.4.0","rtl":false,
		"categories":[{"id":"general","name":"General","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"general","question":"How do I scan?","answer":"Point and shoot.","sort_order":1,"last_updated":"2026-06-01"}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginPages(mux, d)

	// Only en-US.json exists; the request is en-GB — same language, just a
	// different region. Neither exact nor prefix match fires (prefix only
	// checks file-startswith-locale, not the reverse), so without the
	// same-base-language tier this would wrongly claim a fallback.
	req := httptest.NewRequest(http.MethodGet, "/plugin/faq?lang=en-GB", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "How do I scan?") {
		t.Fatalf("en-GB requesting en-US content: code=%d body=%s", rec.Code, body[:min(300, len(body))])
	}
	if strings.Contains(body, "available in your language yet") {
		t.Errorf("en-GB served en-US (same base language) should not show a fallback notice, body=%s", body[:min(600, len(body))])
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
	// Envelope: { "data": { success, event, event_id }, "error": null }
	// (universal-till/CLAUDE.md, ut-docs#387).
	resp := rec2.Body.String()
	if !strings.Contains(resp, `"error":null`) || !strings.Contains(resp, `"success":true`) || !strings.Contains(resp, `"event":"drawer.open"`) {
		t.Errorf("action response = %s", resp)
	}
	var out struct {
		Data struct {
			Success bool   `json:"success"`
			Event   string `json:"event"`
			EventID string `json:"event_id"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode action response: %v (%s)", err, resp)
	}
	if out.Error != nil || !out.Data.Success || out.Data.Event != "drawer.open" || out.Data.EventID == "" {
		t.Fatalf("expected data.{success,event,event_id} populated and error:null, got %+v", out)
	}

	// Unknown button -> 404.
	req3 := httptest.NewRequest(http.MethodPost, "/api/plugins/entries/com.x.drawer/nope/action", nil)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Errorf("unknown button action = %d, want 404", rec3.Code)
	}
}

// A second plugin declaring a type:"page" entry whose route is already
// owned by another installed plugin must be rejected at install time
// (validatePageEntryRoutes, ut-docs#499) — and, end to end through the real
// dispatch path this ticket is about, GET /plugin/<route> must keep
// resolving to the first plugin's content afterward, not 404 or serve
// nothing, confirming the rejected install left no half-registered row
// behind for findPageEntry's first-row-wins match to trip over.
func TestPluginPage_RouteCollisionRejectedAtInstall_FirstPluginStillServes(t *testing.T) {
	chdirRoot(t)
	db := openRealSchemaPagesDB(t)

	first := &plugins.Manifest{
		ID: "com.first.route", Name: "First Route Plugin", Version: "1.0.0", Entrypoint: "./main.wasm",
		Entries: []plugins.ManifestEntry{{Type: "page", Key: "firstkey", Label: "First", Route: "/plugin/shared-route"}},
	}
	if err := plugins.PersistManifest(t.Context(), db, first, plugins.InstallOptions{}); err != nil {
		t.Fatalf("first plugin must install cleanly: %v", err)
	}

	second := &plugins.Manifest{
		ID: "com.second.route", Name: "Second Route Plugin", Version: "1.0.0", Entrypoint: "./main.wasm",
		Entries: []plugins.ManifestEntry{{Type: "page", Key: "secondkey", Label: "Second", Route: "/plugin/shared-route"}},
	}
	if err := plugins.PersistManifest(t.Context(), db, second, plugins.InstallOptions{}); err == nil {
		t.Fatal("second plugin's colliding route must be rejected at install time")
	}

	d := &common.Deps{Db: db, State: common.RuntimeState{Theme: "monarch"}}
	mux := http.NewServeMux()
	registerPluginPages(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/plugin/shared-route", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugin/shared-route = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "First Route Plugin") {
		t.Errorf("expected the first (accepted) plugin's content, got: %s", body[:min(400, len(body))])
	}
	if strings.Contains(body, "Second Route Plugin") {
		t.Errorf("the rejected second plugin's content leaked into the response: %s", body[:min(400, len(body))])
	}
}
