package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// TestShowAllTabEndpoint (ut-docs#2294): "Show an All tab on the sell
// screen" — settings.sale.show_all_tab. Same manager-gated, elevation-wired,
// persist-a-bool, live-state-plus-persisted-row shape as
// TestAllowNegativeInventoryEndpoint above.
//
// Doesn't assert newFullAuthDeps' own starting CurrentState() value —
// unlike production boot (which always populates d.State via
// common.LoadState, whose own default IS true — see
// TestLoadState_DefaultsWhenStoreEmpty in internal/pages/common/
// state_test.go, the actual right place to pin that), this helper builds
// &common.Deps{...} directly and leaves State at its Go zero value, which
// reads as false for this field. AllowNegativeInventory's own equivalent
// test happens not to hit this gap only because ITS real default is also
// false, not because the helper mirrors LoadState.
func TestShowAllTabEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/show-all-tab", url.Values{"enabled": {"true"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier show-all-tab = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	// A cashier's refused attempt must not have changed anything (this
	// harness's own starting value is false — see the doc comment above).
	if d.CurrentState().ShowAllTabOnSellScreen {
		t.Fatal("an un-elevated cashier POST changed the live setting")
	}

	if rec := postForm(mux, "/api/settings/show-all-tab", url.Values{"enabled": {"not-a-bool"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed show-all-tab = %d, want 400", rec.Code)
	}

	if rec := postForm(mux, "/api/settings/show-all-tab", url.Values{"enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("disable show-all-tab = %d, want 204", rec.Code)
	}
	if d.CurrentState().ShowAllTabOnSellScreen {
		t.Fatal("live state still has the All tab on after disabling the setting")
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyShowAllTabOnSellScreen); v != "false" {
		t.Fatalf("stored %s = %q, want false", common.KeyShowAllTabOnSellScreen, v)
	}

	if rec := postForm(mux, "/api/settings/show-all-tab", url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable show-all-tab = %d, want 204", rec.Code)
	}
	if !d.CurrentState().ShowAllTabOnSellScreen {
		t.Fatal("live state still has the All tab off after re-enabling the setting")
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyShowAllTabOnSellScreen); v != "true" {
		t.Fatalf("stored %s = %q, want true", common.KeyShowAllTabOnSellScreen, v)
	}
}

// The control has to actually be on the page — same reasoning as
// TestSettingsPageRendersStockTrackingControl above.
func TestSettingsPageRendersShowAllTabControl(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="show-all-tab-cb"`) {
		t.Fatal("Settings has no show-all-tab checkbox")
	}
	if !strings.Contains(body, "/api/settings/show-all-tab") {
		t.Fatal("the show-all-tab checkbox posts nowhere")
	}
	if !strings.Contains(body, "Show an All tab on the sell screen") {
		t.Fatalf("the checkbox label did not render (missing i18n key?)")
	}
	// Default state: checked (the setting defaults on).
	idx := strings.Index(body, `id="show-all-tab-cb"`)
	tagEnd := strings.Index(body[idx:], ">")
	if !strings.Contains(body[idx:idx+tagEnd], "checked") {
		t.Fatalf("expected the checkbox to be checked by default, got tag: %s", body[idx:idx+tagEnd])
	}
}

// TestShowAllTabWiredIntoRawUpsert (ut-docs#2294): the generic
// /api/settings/upsert raw key/value table (settings-all card) must also
// reflect this key into live state, same as every other boolean sale/pos
// setting already does — see settings_page.go's UpdateState switch.
func TestShowAllTabWiredIntoRawUpsert(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyShowAllTabOnSellScreen}, "value": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert %s=false = %d, want 204", common.KeyShowAllTabOnSellScreen, rec.Code)
	}
	if d.CurrentState().ShowAllTabOnSellScreen {
		t.Fatal("upsert did not reflect show_all_tab=false into live state")
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyShowAllTabOnSellScreen}, "value": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert %s=true = %d, want 204", common.KeyShowAllTabOnSellScreen, rec.Code)
	}
	if !d.CurrentState().ShowAllTabOnSellScreen {
		t.Fatal("upsert did not reflect show_all_tab=true into live state")
	}
}
