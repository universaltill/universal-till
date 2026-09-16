package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// TestCategoriesTabEndpoint (ut-docs#2283). Same manager-gated,
// elevation-wired, persist-a-bool shape as
// TestAllowNegativeInventoryEndpoint/catalog-import-barcode-default, but
// this key is purely presentational (internal/ui.ButtonsHTTP.List reads it
// fresh on every /ui/buttons render, no RuntimeState field), so only the
// persisted settings row is asserted — there is no in-memory state to check
// alongside it.
func TestCategoriesTabEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if v, ok, _ := d.Settings.Get(t.Context(), data.SellScreenCategoriesTabKey); ok && v == "1" {
		t.Fatal("the Categories tab should default to off — a till that never opens the toggle must keep today's tab bar unchanged")
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"true"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier categories-tab = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	// A cashier's refused attempt must not have changed anything.
	if v, _, _ := d.Settings.Get(t.Context(), data.SellScreenCategoriesTabKey); v == "1" {
		t.Fatal("an un-elevated cashier POST changed the Categories-tab setting")
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"not-a-bool"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed categories-tab = %d, want 400", rec.Code)
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable categories-tab = %d, want 204", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.SellScreenCategoriesTabKey); v != "1" {
		t.Fatalf("stored %s = %q, want 1", data.SellScreenCategoriesTabKey, v)
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("disable categories-tab = %d, want 204", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.SellScreenCategoriesTabKey); v != "0" {
		t.Fatalf("stored %s = %q, want 0", data.SellScreenCategoriesTabKey, v)
	}
}

// The control has to actually be reachable from Settings — same defect
// shape ut-docs#1843's own test guards against for its stock-tracking
// checkbox: a complete backend with no way to reach it.
func TestSettingsPageRendersCategoriesTabControl(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="categories-tab-cb"`) {
		t.Fatal("Settings has no Categories-tab checkbox — the setting is unreachable")
	}
	if !strings.Contains(body, "/api/settings/categories-tab") {
		t.Fatal("the Categories-tab checkbox posts nowhere")
	}
}
