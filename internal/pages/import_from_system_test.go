package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4431: the import page's "From another till system" section — the
// till-side face of API-source imports (ADR-0161) for till-only shops. It
// lists the installed api-source import entries by plugin name, shows each
// importer's secret settings as set / not set (never their values), links
// to the plugin settings editor, and runs POST /api/import/from-system
// (mode=preview|commit), which renders an htmx fragment.

const importSystemPluginName = "Other Till Connector"

// newImportSystemDeps is newRealDBDeps (real schema and role permissions)
// with the import page registered and a real manager row (audit FK).
func newImportSystemDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, d := newRealDBDeps(t)
	registerImport(mux, d)
	if _, err := d.Db.Exec(`INSERT INTO users (id, username, display_name, role) VALUES ('m1', 'mgr-system', 'Mgr', 'manager')`); err != nil {
		t.Fatalf("seed manager row: %v", err)
	}
	return mux, d
}

// seedSystemImporter installs the api-source test importer under a
// human name, with the two credential settings an API importer declares.
func seedSystemImporter(t *testing.T, d *common.Deps, apiKey string) {
	t.Helper()
	seedAPIImportPlugin(t, d.Db, apiImportPluginID, []string{"items", "stock"}, []string{"items", "stock"})
	if _, err := d.Db.Exec(`UPDATE plugins SET name = ? WHERE id = ?`, importSystemPluginName, apiImportPluginID); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(d.Db)
	for key, v := range map[string]string{"api_key": apiKey, "api_secret": ""} {
		b, _ := json.Marshal(v)
		if err := repo.UpsertPluginSetting(t.Context(), apiImportPluginID, key, string(b)); err != nil {
			t.Fatal(err)
		}
	}
}

func postImportSystem(mux *http.ServeMux, mode string, user *auth.User) *httptest.ResponseRecorder {
	return postForm(mux, "/api/import/from-system", url.Values{
		"plugin_id": {apiImportPluginID}, "entry_key": {"api"}, "mode": {mode},
	}, user)
}

func TestImportSystem_NoImporterOffersTheStore(t *testing.T) {
	mux, _ := newImportSystemDeps(t)
	body := getImportAs(mux, "/import", mgrUser, false).Body.String()
	for _, want := range []string{
		`id="import-system-card"`,
		`data-import-system-none="1"`,
		`href="/plugins/store?type=import"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no-importer section missing %s", want)
		}
	}
	if strings.Contains(body, `hx-post="/api/import/from-system"`) {
		t.Error("no importer installed, yet a run button is rendered")
	}
}

func TestImportSystem_ListsImportersByNameWithSecretState(t *testing.T) {
	mux, d := newImportSystemDeps(t)
	seedSystemImporter(t, d, "sk_live_SECRETVALUE")
	// A file import entry is not an API importer: never listed here.
	if _, err := d.Db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at) VALUES ('com.test.fileimport', '1.0.0', 'File Importer Plugin', 'desc', 'wasm', './plugin.wasm', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.test.fileimport', 'File Importer Plugin', '1.0.0', './plugin.wasm', 'wasm', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, is_active, sort_order, config_json) VALUES ('fi-e', 'com.test.fileimport', 'bkp', 'Backup', 'import', 1, 0, '{"entities":["items"],"file_formats":[".bkp"]}')`); err != nil {
		t.Fatal(err)
	}

	body := getImportAs(mux, "/import", mgrUser, false).Body.String()
	for _, want := range []string{
		importSystemPluginName,
		`data-import-system-entry="` + apiImportPluginID + `"`,
		`data-secret-key="api_key" data-secret-state="set"`,
		`data-secret-key="api_secret" data-secret-state="not-set"`,
		`href="/plugins/` + apiImportPluginID + `/settings"`,
		`hx-post="/api/import/from-system"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("importer section missing %s", want)
		}
	}
	if strings.Contains(body, "SECRETVALUE") {
		t.Fatal("the import page echoed a secret setting's value")
	}
	if strings.Contains(body, "File Importer Plugin") {
		t.Error("a file import entry is listed as an API importer")
	}
	if strings.Contains(body, `data-import-system-none="1"`) {
		t.Error("an importer is installed, yet the none state renders")
	}
}

func TestImportSystem_WelcomeHasFourthOption(t *testing.T) {
	mux, _ := newImportSystemDeps(t)
	body := getImportAs(mux, "/import?welcome=1", mgrUser, false).Body.String()
	if !strings.Contains(body, `data-welcome-option="system"`) || !strings.Contains(body, `href="#import-system-card"`) {
		t.Fatalf("welcome lacks the From another till system option")
	}
}

// Cashier is sale-only (ut-docs#3079): no section, and the endpoint refuses
// before the plugin is ever asked.
func TestImportSystem_CashierRefused(t *testing.T) {
	mux, d := newImportSystemDeps(t)
	seedSystemImporter(t, d, "k")
	captured := subscribeImportAsk(t, d.Db, apiImportPluginID, json.RawMessage(`{"ok":true}`))

	if body := getImportAs(mux, "/import", cashUser, false).Body.String(); strings.Contains(body, "import-system-card") {
		t.Fatal("cashier sees the From another till system section")
	}
	for _, mode := range []string{"preview", "commit"} {
		rec := postImportSystem(mux, mode, &cashUser)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cashier %s: %d %s, want 403", mode, rec.Code, rec.Body.String())
		}
	}
	if captured.Type != "" {
		t.Fatalf("cashier request reached the plugin: %+v", captured)
	}
}

// Preview renders counts and sample rows and writes nothing; commit applies
// and renders its counts. Real compiled guest.
func TestImportSystem_PreviewThenCommit_RealGuest(t *testing.T) {
	mux, d := newImportSystemDeps(t)
	seedSystemImporter(t, d, "k")
	installImportRowsGuest(t, d)
	setGuestSetting(t, d, "rows", `[{"external_id":"p-1","name":"Flat white","price_minor":320,"category_path":["Drinks"]},`+
		`{"external_id":"p-2","name":"Scone","price_minor":250},{"external_id":"bad","price_minor":1}]`)
	items := func() int { return apiImportCount(t, d.Db, `SELECT COUNT(*) FROM items`) }
	before := items()

	rec := postImportSystem(mux, "preview", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-import-system-result="preview"`,
		`data-count="new">2<`,
		`data-count="issues">1<`,
		`data-sample-action="issue"`,
		"Flat white",
		"submitted 3", // the plugin's own message
		`data-import-system-commit="1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview fragment missing %s:\n%s", want, body)
		}
	}
	if n := items(); n != before {
		t.Fatalf("preview wrote items: %d -> %d", before, n)
	}

	rec = postImportSystem(mux, "commit", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `data-import-system-result="commit"`) || !strings.Contains(body, `data-count="new">2<`) {
		t.Fatalf("commit fragment:\n%s", body)
	}
	if n := items(); n != before+2 {
		t.Fatalf("items after commit = %d, want %d", n, before+2)
	}
}

// A plugin's refusal (a bad key, say) is shown in its own words as an
// alert, and a failed commit offers Run again.
func TestImportSystem_PluginErrorShownWithRunAgain(t *testing.T) {
	mux, d := newImportSystemDeps(t)
	seedSystemImporter(t, d, "")
	subscribeImportAsk(t, d.Db, apiImportPluginID, json.RawMessage(`{"ok":false,"error":"Invalid API key <b>"}`))

	rec := postImportSystem(mux, "commit", &mgrUser)
	if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("declined commit: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{`role="alert"`, "Invalid API key &lt;b&gt;", `data-import-system-retry="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("declined commit fragment missing %s:\n%s", want, body)
		}
	}
	// A failed preview has nothing to run again — the buttons above stay.
	body = postImportSystem(mux, "preview", &mgrUser).Body.String()
	if strings.Contains(body, `data-import-system-retry="1"`) {
		t.Errorf("a failed preview offers Run again:\n%s", body)
	}
}

// An importer that is not installed (or a file entry) is refused cleanly.
func TestImportSystem_UnknownEntryRefused(t *testing.T) {
	mux, d := newImportSystemDeps(t)
	seedSystemImporter(t, d, "k")
	rec := postForm(mux, "/api/import/from-system", url.Values{
		"plugin_id": {"com.test.nope"}, "entry_key": {"api"}, "mode": {"preview"},
	}, &mgrUser)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `role="alert"`) {
		t.Fatalf("unknown entry: %d %s", rec.Code, rec.Body.String())
	}
}

// A commit that stopped part-way says how many rows it applied and offers
// Run again (rows are linked, so a re-run finishes without duplicates).
func TestImportSystem_PartialCommitRendersRunAgain(t *testing.T) {
	chdirRoot(t)
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i18n, "en")
	req := httptest.NewRequest(http.MethodPost, "/api/import/from-system", nil)
	rec := httptest.NewRecorder()
	renderImportSystemResult(rec, req, importSystemView{
		PluginID: apiImportPluginID, EntryKey: "api", Mode: "commit",
		Err: &apiImportError{status: http.StatusInternalServerError, key: apiImportErrPartial, applied: 7},
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"7", `data-import-system-retry="1"`, `role="alert"`, `data-import-system-partial="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("partial fragment missing %s:\n%s", want, body)
		}
	}
}

// A key can have a shop-wide row and a per-till row; like the settings
// editor, the shop-wide one decides set / not set.
func TestImportSystem_SecretStatePrefersTheShopWideRow(t *testing.T) {
	mux, d := newImportSystemDeps(t)
	seedSystemImporter(t, d, "sk_live_SECRETVALUE")
	// An empty per-till api_key row stored BEFORE the shop-wide one, so a
	// "first row wins" reading would pick it.
	for _, q := range []string{
		`DELETE FROM plugin_settings WHERE plugin_id = ? AND key = 'api_key'`,
		`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id) VALUES ('0-register-api-key', ?, 'api_key', '""', 'register', 'till-2')`,
	} {
		if _, err := d.Db.Exec(q, apiImportPluginID); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal("sk_live_SECRETVALUE")
	if err := data.NewPluginRepo(d.Db).UpsertPluginSetting(t.Context(), apiImportPluginID, "api_key", string(b)); err != nil {
		t.Fatal(err)
	}
	body := getImportAs(mux, "/import", mgrUser, false).Body.String()
	if !strings.Contains(body, `data-secret-key="api_key" data-secret-state="set"`) {
		t.Error("an empty per-till row hid the shop-wide api_key: want set")
	}
	if strings.Count(body, `data-secret-key="api_key"`) != 1 {
		t.Error("api_key listed more than once")
	}
}

func TestShopWideSettingRows_GlobalBeatsAnEarlierPerTillRow(t *testing.T) {
	got := shopWideSettingRows([]data.PluginSettingRow{
		{Key: "api_key", Scope: "register", ValueJSON: `""`},
		{Key: "api_key", Scope: "global", ValueJSON: `"k"`},
		{Key: "api_secret", Scope: "register", ValueJSON: `"s"`},
	})
	if len(got) != 2 {
		t.Fatalf("want one row per key (2), got %d", len(got))
	}
	if got[0].Key != "api_key" || got[0].Scope != "global" {
		t.Errorf("api_key: want the global row, got %+v", got[0])
	}
	if got[1].Key != "api_secret" || got[1].Scope != "register" {
		t.Errorf("api_secret: a lone per-till row stays, got %+v", got[1])
	}
}

// ut-docs#4485: a row's issue in the preview is shown in the till's
// language (a code plus args from catsync, translated here), never as
// catsync's English API text.
func TestImportSystem_PreviewRowIssueIsTranslated(t *testing.T) {
	mux, d := newImportSystemDeps(t)
	seedSystemImporter(t, d, "k")
	installImportRowsGuest(t, d)
	setGuestSetting(t, d, "rows", `[{"external_id":"p-1","name":"Flat white","price_minor":320},`+
		`{"external_id":"bad","price_minor":1},`+
		`{"external_id":"p-3","name":"Deep","price_minor":1,"category_path":["a","b","c","d","e"]}]`)

	post := func(lang string) string {
		rec := postForm(mux, "/api/import/from-system?lang="+lang, url.Values{
			"plugin_id": {apiImportPluginID}, "entry_key": {"api"}, "mode": {"preview"},
		}, &mgrUser)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview (%s): %d %s", lang, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	fa := post("fa")
	for _, want := range []string{
		httpx.T("fa", "import.issue.name_invalid"),
		fmt.Sprintf(httpx.T("fa", "import.issue.category_too_deep"), "4"),
	} {
		if want == "" || strings.HasPrefix(want, "import.issue.") || !strings.Contains(fa, want) {
			t.Errorf("fa preview missing %q:\n%s", want, fa)
		}
	}
	for _, english := range []string{"name is missing or invalid", "category_path is deeper"} {
		if strings.Contains(fa, english) {
			t.Errorf("fa preview shows the API English %q:\n%s", english, fa)
		}
	}

	en := post("en")
	if want := httpx.T("en", "import.issue.name_invalid"); !strings.Contains(en, want) {
		t.Errorf("en preview missing %q:\n%s", want, en)
	}
}

// An issue with a code the till has no text for never puts machine text on
// screen.
func TestImportSystemIssueText_UnknownCodeIsGeneric(t *testing.T) {
	chdirRoot(t)
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i18n, "en")
	T := func(k string) string { return httpx.T("en", k) }
	got := importSystemIssueText(T, catsync.Sample{Issue: "raw machine text", IssueCode: "no_such_code"})
	if got != T("import.status.unknown_issue") {
		t.Errorf("unknown code rendered %q", got)
	}
	if got := importSystemIssueText(T, catsync.Sample{Issue: "raw machine text"}); got != T("import.status.unknown_issue") {
		t.Errorf("empty code rendered %q", got)
	}
}

// Every catsync issue code has a translated text in every locale, with the
// same placeholders as English.
func TestCatsyncIssueCodes_HaveLocaleKeys(t *testing.T) {
	chdirRoot(t)
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i18n, "en")
	verbs := func(s string) int { return strings.Count(s, "%s") }
	for _, code := range catsync.IssueCodes() {
		key := "import.issue." + code
		en := httpx.T("en", key)
		if en == key {
			t.Errorf("en.json has no %s", key)
			continue
		}
		for _, loc := range []string{"ar", "fa", "tr"} {
			got := httpx.T(loc, key)
			if got == key || got == en {
				t.Errorf("%s: %s missing or untranslated: %q", loc, key, got)
			} else if verbs(got) != verbs(en) {
				t.Errorf("%s: %s has %d %%s, English has %d", loc, key, verbs(got), verbs(en))
			}
		}
	}
}
