package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#2283: the sell-screen Categories tab is opt-in per till, off by
// default. Same manager-gated, elevation-wired, persist-a-bool shape as
// allow-negative-inventory/launch-on-startup above (settings_page.go).
func TestCategoriesTabEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if d.CurrentState().CategoriesTabEnabled {
		t.Fatal("CategoriesTabEnabled should default to false — the feature is opt-in")
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"true"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier categories-tab = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	// A cashier's refused attempt must not have changed anything.
	if d.CurrentState().CategoriesTabEnabled {
		t.Fatal("an un-elevated cashier POST changed the live categories-tab setting")
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"not-a-bool"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed categories-tab = %d, want 400", rec.Code)
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable categories-tab = %d, want 204", rec.Code)
	}
	if !d.CurrentState().CategoriesTabEnabled {
		t.Fatal("live state still has the Categories tab off after enabling the setting")
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyCategoriesTabEnabled); v != "true" {
		t.Fatalf("stored %s = %q, want true", common.KeyCategoriesTabEnabled, v)
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"false"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("disable categories-tab = %d, want 204", rec.Code)
	}
	if d.CurrentState().CategoriesTabEnabled {
		t.Fatal("live state still has the Categories tab on after disabling the setting")
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyCategoriesTabEnabled); v != "false" {
		t.Fatalf("stored %s = %q, want false", common.KeyCategoriesTabEnabled, v)
	}
}

// The control has to actually be on the page — a complete backend with no
// way to reach it is not a shipped feature (same reasoning as
// TestSettingsPageRendersStockTrackingControl).
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
	if !strings.Contains(body, `id="categories-tab-enabled-cb"`) {
		t.Fatal("Settings has no Categories-tab checkbox — the setting is unreachable")
	}
	if !strings.Contains(body, "/api/settings/categories-tab") {
		t.Fatal("the Categories-tab checkbox posts nowhere")
	}
}

// The Categories tab must actually gate on the setting: off by default, on
// (with a tile per non-empty top-level category) once enabled.
func TestButtonsList_CategoriesTabGatedBySetting(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	// newFullAuthDeps wires auth/setup/settings only (no BtnStore, no
	// /ui/buttons route) — add the buttons API on the same mux/DB so a
	// single test can drive both the settings toggle and its effect on the
	// sell-screen fragment.
	d.BtnStore = ui.NewButtonStore(d.Db)
	registerButtonsAPI(mux, d)

	// Two top-level categories: the tab bar (and so the Categories tab
	// nested inside it, same $hasTabs gating the existing All tab already
	// uses) only renders once there is something to switch between —
	// matching TestButtonsHTTPList_NoTabBarWithOneCategory's own
	// single-category precedent (internal/ui).
	if _, err := d.Db.Exec(`INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('drinks','Drinks',NULL,0,NULL)`); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('food','Food',NULL,1,NULL)`); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm1','S1','Cola', 150, 1, 'drinks')`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm2','S2','Burger', 650, 1, 'food')`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B1','Cola','itm1',0)`); err != nil {
		t.Fatalf("seed shortcut button: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B2','Burger','itm2',1)`); err != nil {
		t.Fatalf("seed shortcut button: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/buttons", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `id="cat-tab-categories"`) {
		t.Fatalf("did not expect the Categories tab before the setting is enabled, got: %s", rec.Body.String())
	}

	if rec := postForm(mux, "/api/settings/categories-tab", url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable categories-tab = %d, want 204", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/ui/buttons", nil)
	req = auth.WithUser(req, mgrUser)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="cat-tab-categories"`) {
		t.Fatalf("expected the Categories tab once the setting is enabled, got: %s", body)
	}
	if !strings.Contains(body, `data-category-id="drinks"`) {
		t.Fatalf("expected a Drinks category tile, got: %s", body)
	}
}
