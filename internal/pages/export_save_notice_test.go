package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
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

// ut-docs#1258: every failure branch in POST /api/catalog/export-save used
// to collapse into the one generic "import.export_save_failed" notice with
// nothing logged server-side to tell them apart — undiagnosable from logs
// alone (the exact complaint behind the Android report, before anyone could
// tell os.UserHomeDir/os.MkdirAll/os.Create apart from the outside). These
// two force the first two failure branches directly and assert the logged
// Problem names the specific failing step, while the operator-facing notice
// stays the same generic message (asserted already above).
func TestExportSave_UserHomeDirFailureLogsWhichStepFailed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Setenv("HOME", "") // os.UserHomeDir errors on Linux when $HOME is unset/empty
	logging.ResetRecent()

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

	if !strings.Contains(rec.Body.String(), `class="pos-notice error"`) {
		t.Fatalf("expected the generic error notice, got: %s", rec.Body.String())
	}
	found := false
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "catalog export-save") && strings.Contains(p.Msg, "os.UserHomeDir") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a logged Problem naming os.UserHomeDir as the failing step, got: %+v", logging.Recent())
	}
}

func TestExportSave_MkdirAllFailureLogsWhichStepFailed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// HOME itself is a plain FILE, not a directory: os.MkdirAll(HOME+"/Downloads", ...)
	// then fails because a path component that must be a directory isn't one.
	homeAsFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(homeAsFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed home-as-file: %v", err)
	}
	t.Setenv("HOME", homeAsFile)
	logging.ResetRecent()

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

	if !strings.Contains(rec.Body.String(), `class="pos-notice error"`) {
		t.Fatalf("expected the generic error notice, got: %s", rec.Body.String())
	}
	found := false
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "catalog export-save") && strings.Contains(p.Msg, "os.MkdirAll") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a logged Problem naming os.MkdirAll as the failing step, got: %+v", logging.Recent())
	}
}
