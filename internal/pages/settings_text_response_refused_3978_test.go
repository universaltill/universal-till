package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#3978: inline-actions.js's `text-response` only trusts a body the
// server marked X-UT-Response: refused (already-translated operator text).
// Every status a Settings form routes to text-response must therefore be
// answered with httpx.RefuseText.
func assertRefusedText(t *testing.T, what string, rec *httptest.ResponseRecorder, wantCode int, wantBody string) {
	t.Helper()
	if rec.Code != wantCode {
		t.Fatalf("%s: code = %d, want %d: %s", what, rec.Code, wantCode, rec.Body.String())
	}
	if got := rec.Header().Get("X-UT-Response"); got != "refused" {
		t.Fatalf("%s: X-UT-Response = %q, want refused", what, got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("%s: Content-Type = %q, want text/plain", what, ct)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != wantBody {
		t.Fatalf("%s: body = %q, want %q", what, got, wantBody)
	}
}

func TestSettingsDisplayMode_InvalidModeIsRefusedText(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSettings(mux, dp)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/settings/display-mode", url.Values{"mode": {"bogus"}}, &manager)
	want := httpx.T("en", "settings.display.err_invalid_mode")
	if want == "settings.display.err_invalid_mode" || strings.Contains(want, "must be register") {
		t.Fatalf("en translation missing: %q", want)
	}
	assertRefusedText(t, "display-mode bogus", rec, http.StatusBadRequest, want)
}

func TestSettingsReportRetention_RefusalsAreRefusedText(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newEODAPITestMux(t)
	post := func(mode string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/settings/report-retention", strings.NewReader("mode="+mode))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		return rec
	}
	assertRefusedText(t, "retention bogus", post("bogus"), http.StatusBadRequest,
		httpx.T("en", "settings.retention.err_invalid_mode"))
	assertRefusedText(t, "retention cloud without subscription", post("cloud"), http.StatusConflict,
		httpx.T("en", "settings.retention.err_needs_subscription"))
}
