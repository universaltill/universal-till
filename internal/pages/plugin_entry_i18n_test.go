package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#2015 (ut-docs#1883/#2004 independent review): reference/
// plugin-manifest.md's entries table documents `label` as a "Display name,
// rendered through the POS translator" for EVERY entry type — plain text
// passes through unchanged, a key is resolved at render time, picking up
// whatever locale overlay the OWNING plugin ships under its own
// locales/<locale>.json (ADR-0010's "any active plugin may also ship
// locales/*.json to translate its own strings", internal/plugins/plugins.go's
// syncLocales). Only 'page'/'export'/'report' honoured that. 'payment',
// 'theme' and 'button' entries never went through that call — their labels
// were copied/rendered verbatim, so a plugin's label always showed in
// whatever language its manifest happened to hardcode, regardless of the
// till's locale. These three tests pin the fix, one per entry type, each
// installing a plugin that ships ONLY a locale overlay (no base-locale
// string) so the assertion can only pass if the render path actually
// resolves the key through the plugin's own overlay, not by coincidence.

// restoreRealI18n re-wires the package-global translator back to the real
// web/locales bundle when this test finishes (ut-docs#2015 review).
// httpx.InitI18n is PROCESS-global with no getter, so a hermetic
// overlay-only fixture installed by one test stays installed for every test
// the runner schedules after it in this binary. The damage is invisible at
// the leaking test's own call site and surfaces as some unrelated test
// failing — and, worse, only for some file orderings, so merely renaming a
// test file can turn the suite red. Best-effort by design: a test that
// isn't chdir'd to the repo root can't find web/locales, and leaving the
// translator alone is strictly better than failing inside cleanup.
func restoreRealI18n(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
		if err != nil {
			return
		}
		httpx.InitI18n(i18n, "en")
	})
}

// newI18nForEntryOverlayTest wires a hermetic (real-bundle-free) translator
// into the package-level httpx translator, the same "must wire it
// explicitly, don't rely on ambient state from another test in this binary"
// posture setup_base_plugins_test.go's newHermeticEnOnlyI18n documents —
// plus the restore that posture needs to not simply move the problem onto
// the next test (restoreRealI18n).
func newI18nForEntryOverlayTest(t *testing.T) *config.I18n {
	t.Helper()
	restoreRealI18n(t)
	i18n, err := config.NewI18nFS(fstest.MapFS{
		"en.json": &fstest.MapFile{Data: []byte(`{}`)},
	}, "en")
	if err != nil {
		t.Fatalf("build test i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	return i18n
}

// writePluginLocaleOverlay writes <plugin>/<version>/locales/<locale>.json
// under paths.Plugins() — the exact tree internal/plugins.syncLocales scans
// (ut-docs/architecture/plugin-architecture.md §7's "plugins ship locales/
// overlays" convention).
func writePluginLocaleOverlay(t *testing.T, pluginID, version, locale, key, value string) {
	t.Helper()
	dir := paths.Plugins(pluginID, version, "locales")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plugin locales dir: %v", err)
	}
	body := `{"` + key + `":"` + value + `"}`
	if err := os.WriteFile(filepath.Join(dir, locale+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write plugin locale overlay: %v", err)
	}
}

// TestPayTab_PaymentEntryLabelResolvesPluginLocaleOverlay: a payment plugin's
// entry label is copied into payment_methods.name at sync time
// (SyncPluginPaymentMethods) and must resolve through T — same render-time
// mechanism the Pay grid's built-in fallbacks already use (index.html's
// {{ T "tender.cash" }} branch) — wherever it reaches the Pay tab.
func TestPayTab_PaymentEntryLabelResolvesPluginLocaleOverlay(t *testing.T) {
	chdirRoot(t)
	isolatePluginsDir(t)

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	const pluginID, version = "com.test.sumup", "1.0.0"
	seedTestPlugin(t, db, pluginID, "SumUp Payment", version)
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,config_json,is_active,sort_order) VALUES('pe-sumup',?,'payment','sumup','payment.sumup.label','{"method_type":"card"}',1,0)`, pluginID); err != nil {
		t.Fatalf("seed payment entry: %v", err)
	}
	writePluginLocaleOverlay(t, pluginID, version, "de", "payment.sumup.label", "Kartenzahlung SumUp")

	i18n := newI18nForEntryOverlayTest(t)
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	pm.SetLocalizer(i18n) // production chain: syncLocales -> i18n.SetOverlays

	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 100},
	})
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerIndex(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/?lang=de", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /?lang=de = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, "payment.sumup.label") {
		t.Errorf("Pay tab renders the raw plugin label/translation key verbatim instead of resolving it through T — the ut-docs#2015 defect is back. body snippet around the tender area:\n%s", snippetAround(body, "sumup"))
	}
	if !strings.Contains(body, "Kartenzahlung SumUp") {
		t.Errorf("Pay tab does not resolve the payment entry's label through the plugin's own de.json locale overlay; want %q somewhere in the rendered page", "Kartenzahlung SumUp")
	}
}

// TestThemePicker_LabelResolvesPluginLocaleOverlay: the Settings theme <select>
// lists ThemeOption.Label straight from plugin_entries (type='theme') — must
// resolve through T the same as a page/export/report entry's label does.
func TestThemePicker_LabelResolvesPluginLocaleOverlay(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	isolatePluginsDir(t)

	const pluginID, version = "com.test.midnight", "1.0.0"
	seedTestPlugin(t, d.Db, pluginID, "Midnight Theme", version)
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,config_json,is_active,sort_order) VALUES('pe-midnight',?,'theme','midnight','theme.midnight.label','{"css":"theme.css"}',1,0)`, pluginID); err != nil {
		t.Fatalf("seed theme entry: %v", err)
	}
	writePluginLocaleOverlay(t, pluginID, version, "de", "theme.midnight.label", "Mitternacht")

	i18n := newI18nForEntryOverlayTest(t)
	pm, err := plugins.Init(t.Context(), d.Cfg, d.Db)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	pm.SetLocalizer(i18n)
	d.Pm = pm

	req := httptest.NewRequest(http.MethodGet, "/settings?lang=de", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings?lang=de = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, "theme.midnight.label") {
		t.Errorf("theme picker renders the raw plugin label/translation key verbatim instead of resolving it through T — the ut-docs#2015 defect is back. body snippet:\n%s", snippetAround(body, "midnight"))
	}
	if !strings.Contains(body, "Mitternacht") {
		t.Errorf("theme picker does not resolve the theme entry's label through the plugin's own de.json locale overlay; want %q somewhere in the rendered page", "Mitternacht")
	}
}

// TestPluginButtons_LabelResolvesPluginLocaleOverlay: /ui/plugin-buttons
// renders ButtonEntryRow.Label straight into the button text — must resolve
// through T the same as a page/export/report entry's label does.
func TestPluginButtons_LabelResolvesPluginLocaleOverlay(t *testing.T) {
	d, _ := pluginPageTestDeps(t)
	isolatePluginsDir(t)

	const pluginID, version = "com.test.nosale", "1.0.0"
	seedTestPlugin(t, d.Db, pluginID, "No Sale Button", version)
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active,sort_order) VALUES('pe-nosale',?,'button','nosale','button.nosale.label',1,0)`, pluginID); err != nil {
		t.Fatalf("seed button entry: %v", err)
	}
	writePluginLocaleOverlay(t, pluginID, version, "de", "button.nosale.label", "Kein Verkauf")

	i18n := newI18nForEntryOverlayTest(t)
	pm, err := plugins.Init(t.Context(), &config.Config{Env: "test"}, d.Db)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	pm.SetLocalizer(i18n)

	mux := http.NewServeMux()
	registerPluginPages(mux, d)

	req := httptest.NewRequest(http.MethodGet, "/ui/plugin-buttons?lang=de", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/plugin-buttons?lang=de = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, "button.nosale.label") {
		t.Errorf("plugin button renders the raw plugin label/translation key verbatim instead of resolving it through T — the ut-docs#2015 defect is back. body:\n%s", body)
	}
	if !strings.Contains(body, "Kein Verkauf") {
		t.Errorf("plugin button does not resolve its label through the plugin's own de.json locale overlay; want %q somewhere in the rendered partial, got:\n%s", "Kein Verkauf", body)
	}
}

// snippetAround returns up to 400 chars of body around the first occurrence
// of marker, for readable failure output on a big rendered page.
func snippetAround(body, marker string) string {
	i := strings.Index(body, marker)
	if i < 0 {
		if len(body) > 400 {
			return body[:400]
		}
		return body
	}
	start := i - 200
	if start < 0 {
		start = 0
	}
	end := i + 200
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}
