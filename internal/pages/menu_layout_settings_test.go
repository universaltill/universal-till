package pages

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0088 Decision D: findability is mandatory. Settings lists every
// destination a layout plugin currently hides, names the plugin, and
// restores per entry — a merchant can always recover a destination without
// uninstalling the plugin.

func newMenuLayoutSettingsDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerMenuLayoutSettings(mux, dp)
	return mux, dp
}

func installSalonLayout(t *testing.T, dp *common.Deps) {
	t.Helper()
	m := &plugins.Manifest{
		ID: "com.example.salon", Name: "Salon layout", Version: "1.0.0", Runtime: "none", CanonicalType: "layout",
		Entries: []plugins.ManifestEntry{{Type: "layout", Key: "menu", Label: "Salon", Config: map[string]any{
			"slot": "menu", "amendments": []any{
				map[string]any{"key": "/tables", "hide": true},
				map[string]any{"key": "/kitchen-stations", "hide": true},
			},
		}}},
	}
	if err := plugins.PersistManifest(t.Context(), dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func getPage(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func postMenuLayoutForm(t *testing.T, mux *http.ServeMux, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestMenuLayoutSettings_ListsHiddenDestinationsNamingThePlugin(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installSalonLayout(t, dp)

	rec := getPage(t, mux, "/settings/menu")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings/menu = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		// the core label (as html/template escapes it), not the key
		template.HTMLEscapeString(httpx.T("en", "tables.title")), template.HTMLEscapeString(httpx.T("en", "kitchenstations.title")),
		"Salon layout",                               // the plugin that hid it, by name
		`href="/tables"`, `href="/kitchen-stations"`, // still reachable: a direct link to each
		`name="key" value="/tables"`, `action="/api/settings/menu/restore"`, // per-entry restore
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hidden-destinations page missing %q in: %s", want, body)
		}
	}
	if strings.Contains(body, "menulayout.none") || strings.Contains(body, httpx.T("en", "menulayout.none")) {
		t.Errorf("empty-state text must not show while tiles are hidden: %s", body)
	}
}

func TestMenuLayoutSettings_EmptyStateWhenNothingIsHidden(t *testing.T) {
	mux, _ := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	rec := getPage(t, mux, "/settings/menu")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), httpx.T("en", "menulayout.none")) {
		t.Fatalf("expected the empty state, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMenuLayoutSettings_RestoreBringsTheTileBackAndSurvivesReload(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installSalonLayout(t, dp)
	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("precondition: /tables hidden, got: %s", body)
	}

	rec := postMenuLayoutForm(t, mux, "/api/settings/menu/restore", url.Values{"key": {"/tables"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/settings/menu") {
		t.Fatalf("restore = %d Location=%q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	body := getMenu(t, mux)
	if !strings.Contains(body, `href="/tables"`) {
		t.Fatalf("restored /tables must render again, got: %s", body)
	}
	if strings.Contains(body, `href="/kitchen-stations"`) {
		t.Fatalf("restore is per entry — /kitchen-stations must stay hidden, got: %s", body)
	}

	// Persisted, not in-memory: a plugin reload (sync-pull fires one every
	// 30s) must not silently re-hide what the merchant restored.
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	if body := getMenu(t, mux); !strings.Contains(body, `href="/tables"`) {
		t.Fatalf("restore must survive ReloadPlugins, got: %s", body)
	}

	// The page now shows the entry as restored, with the way back.
	page := getPage(t, mux, "/settings/menu").Body.String()
	if !strings.Contains(page, httpx.T("en", "menulayout.state.restored")) || !strings.Contains(page, `action="/api/settings/menu/rehide"`) {
		t.Fatalf("expected restored state + re-hide action, got: %s", page)
	}
	rec = postMenuLayoutForm(t, mux, "/api/settings/menu/rehide", url.Values{"key": {"/tables"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rehide = %d: %s", rec.Code, rec.Body.String())
	}
	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("re-hidden /tables must be gone again, got: %s", body)
	}
}

func TestMenuLayoutSettings_RestoreRejectsAKeyNothingHides(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installSalonLayout(t, dp)
	for _, key := range []string{"/nope", "/items", ""} {
		rec := postMenuLayoutForm(t, mux, "/api/settings/menu/restore", url.Values{"key": {key}})
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=menulayout.error.unknown_key") {
			t.Fatalf("restore %q = %d Location=%q, want a redirect carrying the error key", key, rec.Code, rec.Header().Get("Location"))
		}
	}
	// The error renders as text on the page, through T.
	page := getPage(t, mux, "/settings/menu?err=menulayout.error.unknown_key").Body.String()
	if !strings.Contains(page, httpx.T("en", "menulayout.error.unknown_key")) || strings.Contains(page, ">menulayout.error.unknown_key<") {
		t.Fatalf("expected the translated error, got: %s", page)
	}
}

// Same gate as every other settings surface: canPerform(d, r, "settings").
func TestMenuLayoutSettings_RequiresManager(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	installSalonLayout(t, dp)
	// UT_AUTH unset, no session user on the request.
	if rec := getPage(t, mux, "/settings/menu"); rec.Code != http.StatusForbidden {
		t.Fatalf("GET /settings/menu without a manager = %d, want 403", rec.Code)
	}
	for _, path := range []string{"/api/settings/menu/restore", "/api/settings/menu/rehide"} {
		if rec := postMenuLayoutForm(t, mux, path, url.Values{"key": {"/tables"}}); rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without a manager = %d, want 403", path, rec.Code)
		}
	}
	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("a refused restore must change nothing, got: %s", body)
	}
}
