package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// When an update exists but in-app apply is unsupported, a Windows desktop
// keeps an actionable download link (the user runs the installer), but a unix
// kiosk must NOT get a website link it can't act on (no way out of fullscreen)
// — it gets a plain "unavailable" message instead. Regression guard for the
// Pi kiosk dead-end (ut-docs#147).
func TestUpdateFallbackHTML(t *testing.T) {
	chdirRoot(t) // so web/locales resolves regardless of test ordering
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	win := updateUnavailableHTML("en", "0.2.51", "windows")
	if !strings.Contains(win, `href="https://www.universaltill.com/download"`) {
		t.Errorf("windows fallback should keep the download link, got %q", win)
	}

	kiosk := updateUnavailableHTML("en", "0.2.51", "linux")
	if strings.Contains(kiosk, "universaltill.com/download") || strings.Contains(kiosk, "<a ") {
		t.Errorf("kiosk fallback must not contain a website link, got %q", kiosk)
	}
	if !strings.Contains(kiosk, "available for this install") { // apostrophe is HTML-escaped
		t.Errorf("kiosk fallback should state in-app update is unavailable, got %q", kiosk)
	}

	// A Mac that can't self-update (ut-docs#18: an Intel Mac, since no Intel
	// .dmg is ever published) has a browser and can act on a link just like
	// Windows — it must NOT fall into the unix-kiosk dead-end branch, or the
	// Settings page contradicts the status bar (which already links out via
	// base.html's canselfupdate-false branch).
	mac := updateUnavailableHTML("en", "0.2.51", "darwin")
	if !strings.Contains(mac, `href="https://www.universaltill.com/download"`) {
		t.Errorf("macOS fallback should keep the download link, got %q", mac)
	}

	// Both still surface the available version so the operator knows one exists.
	for _, h := range []string{win, kiosk, mac} {
		if !strings.Contains(h, "0.2.51") {
			t.Errorf("fallback should show the available version, got %q", h)
		}
	}
}

// The updater endpoints are manager-gated. Without auth-off and without a
// manager in context, both must refuse before doing any work (crucially, apply
// must not reach selfupdate.Apply, and check must not hit the network). These
// tests deliberately leave UT_AUTH unset so the gate is exercised.
func TestUpdateAPI_ManagerGate(t *testing.T) {
	t.Setenv("UT_AUTH", "") // ensure auth is NOT disabled for this test

	dp := &common.Deps{}
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)

	for _, path := range []string{"/api/update/apply", "/api/update/check"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without manager: code %d, want 403 (body %q)",
				path, rec.Code, rec.Body.String())
		}
	}
}
