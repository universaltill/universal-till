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

// ut-docs#2154. error_page.html's "Back to sale" link had the same
// mode-unaware bare "/" shape ut-docs#2146 fixed for /open-orders.
// RenderError has no *common.Deps in scope (it's called from ~80 sites
// across many packages), so it can't read display.mode fresh per request
// the way index_page.go/open_orders_page.go do — instead it reads the same
// process-wide cached value InitDisplayMode publishes, mirroring the
// existing kiosk/selforder atomic template-func pattern in httpx.go.
func TestRenderErrorBackToSaleURL_BackofficeModeReachesSaleScreenNotDashboard(t *testing.T) {
	i18n := realI18n(t)
	chdirTemp(t)
	InitI18n(i18n, "en")
	InitDisplayMode("backoffice")
	t.Cleanup(func() { InitDisplayMode("") })

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tables", nil)
	RenderError(w, r, http.StatusInternalServerError, "common.error.server", errors.New("boom"))

	body := w.Body.String()
	if !strings.Contains(body, `href="/?stay=1"`) {
		t.Fatalf("expected Back to sale to link to /?stay=1 on a backoffice-mode till, got: %s", body)
	}
}

func TestRenderErrorBackToSaleURL_SelfOrderModeStaysOnTillHomeNotCashierScreen(t *testing.T) {
	i18n := realI18n(t)
	chdirTemp(t)
	InitI18n(i18n, "en")
	InitDisplayMode("self_order")
	t.Cleanup(func() { InitDisplayMode("") })

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tables", nil)
	RenderError(w, r, http.StatusInternalServerError, "common.error.server", errors.New("boom"))

	body := w.Body.String()
	if !strings.Contains(body, `href="/self-order"`) {
		t.Fatalf("expected Back to sale to link to /self-order on a self-order-mode till (ADR-0020 containment), got: %s", body)
	}
}

func TestRenderErrorBackToSaleURL_DefaultModeIsUnchanged(t *testing.T) {
	i18n := realI18n(t)
	chdirTemp(t)
	InitI18n(i18n, "en")
	InitDisplayMode("")
	t.Cleanup(func() { InitDisplayMode("") })

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tables", nil)
	RenderError(w, r, http.StatusInternalServerError, "common.error.server", errors.New("boom"))

	body := w.Body.String()
	if !strings.Contains(body, `href="/"`) || strings.Contains(body, `href="/?stay=1"`) {
		t.Fatalf("expected Back to sale to keep its plain / link when no mode is set, got: %s", body)
	}
}

// ut-docs#2362: RenderError's data map never carried ".theme", so a themed
// till's error page rendered with no theme stylesheet AND — via the
// shellsig template func base.html's <meta name="ut-shell"> uses — a
// different ADR-0098 shell signature than every other page on that till,
// which makes a boosted navigation onto an error page (correctly, but
// wrongly here) fall back to a full, unthemed document load instead of
// swapping in place. RenderError has no *common.Deps in scope, so it reads
// the same process-wide cached value InitTheme publishes, mirroring the
// InitDisplayMode/backtosaleurl pattern just above.
func TestRenderErrorCarriesCurrentTheme(t *testing.T) {
	i18n := realI18n(t)
	chdirTemp(t)
	InitI18n(i18n, "en")
	InitTheme("dark")
	t.Cleanup(func() { InitTheme("") })

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tables", nil)
	RenderError(w, r, http.StatusInternalServerError, "common.error.server", errors.New("boom"))

	body := w.Body.String()
	if !strings.Contains(body, `data-theme="dark"`) {
		t.Fatalf("expected the error page to carry the shop's current theme, got: %s", body)
	}
	if !strings.Contains(body, `href="/themes/dark.css`) {
		t.Fatalf("expected the error page to link the theme's stylesheet, got: %s", body)
	}
	wantSig := ShellSignature("en", "dark")
	if !strings.Contains(body, `content="`+wantSig+`"`) {
		t.Fatalf("expected <meta name=\"ut-shell\"> to match ShellSignature(\"en\", \"dark\") = %q — a themed till's other pages compute this same signature, and a mismatch is what forces a boosted navigation onto an error page to fall back to a full document load; got: %s", wantSig, body)
	}
}

// A shop that has never configured a theme (the common case, including this
// product's own default install) must render exactly as it did before this
// fix — no regression on the untouched-theme path this bug was masked by.
func TestRenderErrorDefaultThemeIsUnchanged(t *testing.T) {
	i18n := realI18n(t)
	chdirTemp(t)
	InitI18n(i18n, "en")
	InitTheme("")
	t.Cleanup(func() { InitTheme("") })

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tables", nil)
	RenderError(w, r, http.StatusInternalServerError, "common.error.server", errors.New("boom"))

	body := w.Body.String()
	if !strings.Contains(body, `data-theme=""`) {
		t.Fatalf("expected no theme configured to render an empty data-theme, got: %s", body)
	}
	wantSig := ShellSignature("en", "")
	if !strings.Contains(body, `content="`+wantSig+`"`) {
		t.Fatalf("expected <meta name=\"ut-shell\"> to match ShellSignature(\"en\", \"\") = %q, got: %s", wantSig, body)
	}
}
