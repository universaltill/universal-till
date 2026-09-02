package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRenderErrorRendersFullLayout guards the actual bug ut-docs#1455 fixes:
// a page-route error must never fall back to a bare-text http.Error body —
// no nav rail, no way back on the pinned Android kiosk (no browser Back).
// RenderError must render the SAME full layout (base.html + nav) any normal
// page does, with the right status code, a no-store cache header, and the
// translated operator-facing message — while the raw underlying error text
// never reaches the response body at all.
func TestRenderErrorRendersFullLayout(t *testing.T) {
	InitI18n(realI18n(t), "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tables", nil)

	sentinel := errors.New("some internal detail not for operators: constraint failed on table held_sales")
	RenderError(w, r, http.StatusInternalServerError, "common.error.server", sentinel)

	res := w.Result()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusInternalServerError)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}

	body := w.Body.String()

	// The nav rail must still be present — this is the whole point: the
	// operator is never dropped into a bare-text document with no way back.
	if !strings.Contains(body, "nav-toggle") {
		t.Fatalf("response body missing nav rail marker (nav-toggle); got:\n%s", body)
	}

	// The translated operator-facing message must be present.
	wantMsg := T("en", "common.error.server")
	if wantMsg == "" || wantMsg == "common.error.server" {
		t.Fatalf("test setup: common.error.server did not translate, got %q", wantMsg)
	}
	if !strings.Contains(body, wantMsg) {
		t.Fatalf("response body missing translated message %q; got:\n%s", wantMsg, body)
	}

	// The "Back to sale" link must be present and reachable.
	backToSale := T("en", "menu.back_to_sale")
	if !strings.Contains(body, backToSale) {
		t.Fatalf("response body missing %q (Back to sale link); got:\n%s", backToSale, body)
	}

	// The raw Go error text must NEVER reach the response body.
	if strings.Contains(body, "some internal detail not for operators") {
		t.Fatalf("response body leaked the raw underlying error text; got:\n%s", body)
	}
}

// TestRenderErrorWorksWithNilErr covers a gate with no underlying Go error
// at all (e.g. a plain 403 permission check) — must not panic and must
// still render the full page.
func TestRenderErrorWorksWithNilErr(t *testing.T) {
	InitI18n(realI18n(t), "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users/permissions", nil)

	RenderError(w, r, http.StatusForbidden, "common.error.super_admin_required", nil)

	res := w.Result()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusForbidden)
	}
	body := w.Body.String()
	if !strings.Contains(body, "nav-toggle") {
		t.Fatalf("response body missing nav rail marker (nav-toggle); got:\n%s", body)
	}
	wantMsg := T("en", "common.error.super_admin_required")
	if !strings.Contains(body, wantMsg) {
		t.Fatalf("response body missing translated message %q; got:\n%s", wantMsg, body)
	}
}
