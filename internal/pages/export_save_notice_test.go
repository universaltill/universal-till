package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#238: POST /api/catalog/export-save used to write ad-hoc
// <span class="...">...</span> fragments straight into #export-msg via
// fmt.Fprintf — this migrates it onto the documented .pos-notice pattern
// (docs/sale-screen-notifications.md), the same shape
// web/ui/partials/basket.html renders for the sale screen.

func TestExportSave_ForbiddenRendersPosNoticeError(t *testing.T) {
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/export-save", nil)
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `class="pos-notice error"`) {
		t.Fatalf("expected a pos-notice error, got: %s", body)
	}
	if !strings.Contains(body, `role="alert"`) {
		t.Fatalf("error notice must carry role=alert, got: %s", body)
	}
	if !strings.Contains(body, `class="notice-dismiss"`) {
		t.Fatalf("expected a dismiss control, got: %s", body)
	}
	want := httpx.T("en", "settings.enrol.forbidden")
	if !strings.Contains(body, want) {
		t.Fatalf("expected translated forbidden message %q, got: %s", want, body)
	}
	// Guards against the old ad-hoc markup regressing back in.
	if strings.Contains(body, `<span class="error">`) {
		t.Fatalf("old ad-hoc <span class=\"error\"> markup must be gone, got: %s", body)
	}
}

func TestExportSave_SuccessRendersPosNoticeSuccessWithDestinationPath(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	home := t.TempDir()
	t.Setenv("HOME", home)

	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/export-save", nil)
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `class="pos-notice success"`) {
		t.Fatalf("expected a pos-notice success, got: %s", body)
	}
	if !strings.Contains(body, `role="status"`) {
		t.Fatalf("success notice must carry role=status, got: %s", body)
	}
	wantMsg := httpx.T("en", "settings.backup.saved_to")
	if !strings.Contains(body, wantMsg) {
		t.Fatalf("expected translated saved-to message %q, got: %s", wantMsg, body)
	}
	if !strings.Contains(body, "<code>"+filepath.Join(home, "Downloads")) {
		t.Fatalf("expected the destination path wrapped in <code>, got: %s", body)
	}
	// Guards against the old ad-hoc markup regressing back in.
	if strings.Contains(body, `<span>`) {
		t.Fatalf("old ad-hoc <span> markup must be gone, got: %s", body)
	}
}
