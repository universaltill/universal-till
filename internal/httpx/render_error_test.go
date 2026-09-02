package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRenderErrorRendersFullLayout is the core regression test for
// ut-docs#1455: a page-route repo failure must render through the SAME
// base layout every other page uses (rail visible, a "Back to sale" way
// out) rather than replacing the whole document with bare text.
func TestRenderErrorRendersFullLayout(t *testing.T) {
	i18n := realI18n(t)
	chdirTemp(t)
	InitI18n(i18n, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tables", nil)
	RenderError(w, r, http.StatusInternalServerError, "common.error.server", errors.New("boom"))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="nav"`) {
		t.Errorf("response has no nav rail — a kiosk hitting this has no way back:\n%s", body)
	}
	if !strings.Contains(body, "Back to sale") {
		t.Errorf("response has no \"Back to sale\" link:\n%s", body)
	}
	if !strings.Contains(body, "Something went wrong") {
		t.Errorf("response doesn't contain the translated error message:\n%s", body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
	// The raw error must never reach the response body.
	if strings.Contains(body, "boom") {
		t.Errorf("raw error leaked into the response body:\n%s", body)
	}
}

// A 403/503 call site with no underlying repo error (a permission check, a
// feature-not-configured branch) passes err=nil — RenderError itself must
// not panic or misbehave on that path (the log line's exact text isn't
// observable here: internal/logging only feeds its in-memory Problems ring
// at Warn+ and this is a 403, so it logs at Info — internal/logging has no
// writer-injection seam for a unit test to assert the literal formatted
// string against).
func TestRenderErrorNilErrorDoesNotPanic(t *testing.T) {
	chdirTemp(t)
	InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users/permissions", nil)
	RenderError(w, r, http.StatusForbidden, "permissions.error.super_admin_required", nil)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestRenderErrorTranslatesPerLocale(t *testing.T) {
	i18n := realI18n(t)
	chdirTemp(t)
	InitI18n(i18n, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tables?lang=tr", nil)
	RenderError(w, r, http.StatusInternalServerError, "common.error.server", errors.New("boom"))

	body := w.Body.String()
	if !strings.Contains(body, "Bir şeyler ters gitti") {
		t.Errorf("expected the Turkish translation of common.error.server, got:\n%s", body)
	}
}
