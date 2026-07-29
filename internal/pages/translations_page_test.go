package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// translation_overrides isn't in the simplified seedForPages schema, so
// (like sync_admin_test.go's newMigratedSyncDeps) these use a real migrated
// database.
func newTranslationsTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *config.I18n) {
	t.Helper()
	chdirRoot(t)
	d, err := appdb.Open(filepath.Join(t.TempDir(), "translations.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d.DB), cfg)
	// audit_log.actor_id has a real FK to users(id) in the migrated schema;
	// seed the manager withManager() attaches so InsertAudit doesn't fail
	// its FK check (silently, since callers do `_ = posRepo.InsertAudit(...)`).
	if _, err := d.DB.ExecContext(t.Context(), `
INSERT INTO users(id, username, display_name, role, is_active) VALUES('mgr-1', 'manager', 'Manager', 'manager', 1)`); err != nil {
		t.Fatalf("seed manager user: %v", err)
	}
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/settings", Label: "Settings"}},
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
	}
	mux := http.NewServeMux()
	registerTranslations(mux, dp, i18n)
	return mux, dp, i18n
}

func withManager(req *http.Request) *http.Request {
	return auth.WithUser(req, auth.User{ID: "mgr-1", Username: "manager", Role: "manager"})
}

func TestRegisterTranslations_GET_RequiresManager(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/translations", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestRegisterTranslations_GET_RendersAvailableLocales(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	req := withManager(httptest.NewRequest(http.MethodGet, "/translations?edit_locale=en", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /translations: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "en") {
		t.Fatalf("expected the en locale referenced in the page")
	}
}

func TestTranslationsTable_FiltersByQuery(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	// A key that certainly exists (used throughout this same test suite).
	req := withManager(httptest.NewRequest(http.MethodGet, "/ui/translations-table?edit_locale=en&q=sync.chip_queued", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync.chip_queued") {
		t.Fatalf("expected the matching key in the filtered table, got %q", rec.Body.String())
	}

	req = withManager(httptest.NewRequest(http.MethodGet, "/ui/translations-table?edit_locale=en&q=zzz-no-such-key-zzz", nil))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/translations-table (no match): code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sync.chip_queued") {
		t.Fatalf("expected the query to filter out non-matching keys")
	}
}

func TestTranslationsSet_RequiresManager(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	form := url.Values{"edit_locale": {"en"}, "key": {"sync.chip_queued"}, "value": {"Queued"}}
	req := httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestTranslationsSet_ValidatesRequiredFields(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	cases := []url.Values{
		{"edit_locale": {""}, "key": {"k"}, "value": {"v"}},
		{"edit_locale": {"en"}, "key": {""}, "value": {"v"}},
		{"edit_locale": {"en"}, "key": {"k"}, "value": {"  "}},
	}
	for _, form := range cases {
		req := withManager(httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(form.Encode())))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("form %+v: expected 400, got %d", form, rec.Code)
		}
	}
}

func TestTranslationsSet_PersistsReloadsLiveTranslatorAndAudits(t *testing.T) {
	mux, dp, i18n := newTranslationsTestDeps(t)
	form := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}, "value": {"Custom Home Label"}}
	req := withManager(httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/translations/set: code %d body %s", rec.Code, rec.Body.String())
	}

	// The live translator must reflect the override immediately, no restart.
	found := false
	for _, e := range i18n.Entries("en") {
		if e.Key == "nav.home" {
			found = true
			if e.Value != "Custom Home Label" || e.Source != "shop" {
				t.Fatalf("expected nav.home overridden to 'Custom Home Label' (source=shop), got %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("expected nav.home present in the live entries after the override")
	}

	var auditCount int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'translation_override_set'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one translation_override_set audit row, got %d", auditCount)
	}
}

func TestTranslationsClear_RemovesOverrideReloadsAndAudits(t *testing.T) {
	mux, dp, i18n := newTranslationsTestDeps(t)

	setForm := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}, "value": {"Temporary Override"}}
	req := withManager(httptest.NewRequest(http.MethodPost, "/api/translations/set", strings.NewReader(setForm.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	clearForm := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}}
	req = withManager(httptest.NewRequest(http.MethodPost, "/api/translations/clear", strings.NewReader(clearForm.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/translations/clear: code %d body %s", rec.Code, rec.Body.String())
	}

	for _, e := range i18n.Entries("en") {
		if e.Key == "nav.home" && e.Source == "shop" {
			t.Fatalf("expected the override cleared (source should fall back to base), got %+v", e)
		}
	}

	var auditCount int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'translation_override_cleared'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one translation_override_cleared audit row, got %d", auditCount)
	}
}

func TestTranslationsClear_RequiresManager(t *testing.T) {
	mux, _, _ := newTranslationsTestDeps(t)
	form := url.Values{"edit_locale": {"en"}, "key": {"nav.home"}}
	req := httptest.NewRequest(http.MethodPost, "/api/translations/clear", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}
