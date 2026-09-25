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

// TestBrowsingModeEndpoint (ut-docs#2499): POST /api/settings/browsing-mode
// — sale.browsing_mode, a closed enum. Same manager-gated, elevation-wired,
// 400-on-bad-value, SaveState-then-SetState shape as TestWindowModeEndpoint
// (settings_page_test.go), minus the WindowCtl apply step (nothing outside
// the sell screen's next render depends on this value).
//
// Like TestShowAllTabEndpoint before it, this doesn't assert
// newFullAuthDeps' own starting CurrentState() value: that helper builds
// &common.Deps{...} directly, so State is the Go zero value ("" here), and
// the real default is pinned by internal/pages/common's own LoadState tests.
func TestBrowsingModeEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/browsing-mode", url.Values{"mode": {common.BrowsingModeAllFilterChips}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier browsing-mode = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if got := d.CurrentState().BrowsingMode; got != "" {
		t.Fatalf("an un-elevated cashier POST changed the live setting to %q", got)
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyBrowsingMode); ok {
		t.Fatal("an un-elevated cashier POST persisted a browsing mode")
	}

	for _, bad := range []string{"", "tabs", "CATEGORY_TABS", "not-a-mode"} {
		if rec := postForm(mux, "/api/settings/browsing-mode", url.Values{"mode": {bad}}, &mgrUser); rec.Code != http.StatusBadRequest {
			t.Fatalf("browsing-mode=%q = %d, want 400", bad, rec.Code)
		}
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyBrowsingMode); ok {
		t.Fatal("a rejected browsing mode must not be persisted")
	}

	for _, mode := range []string{common.BrowsingModeStripOverflow, common.BrowsingModeAllFilterChips, common.BrowsingModeCategoryTabs} {
		if rec := postForm(mux, "/api/settings/browsing-mode", url.Values{"mode": {mode}}, &mgrUser); rec.Code != http.StatusNoContent {
			t.Fatalf("browsing-mode=%q = %d, want 204", mode, rec.Code)
		}
		if got := d.CurrentState().BrowsingMode; got != mode {
			t.Fatalf("live BrowsingMode = %q after saving %q", got, mode)
		}
		if v, _, _ := d.Settings.Get(t.Context(), common.KeyBrowsingMode); v != mode {
			t.Fatalf("stored %s = %q, want %q", common.KeyBrowsingMode, v, mode)
		}
	}
}

// The control has to actually be on the page — same reasoning as
// TestSettingsPageRendersStockTrackingControl: a complete backend with no
// way to reach it is the ut-docs#1843 defect class. A <select> with the
// three hardcoded options (the window-mode idiom, not the open theme list),
// the stored mode pre-selected, and a per-mode helper line (UX gate on
// ut-docs#2499: a bare enum picker means nothing to a shop owner).
func TestSettingsPageRendersBrowsingModeControl(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if rec := postForm(mux, "/api/settings/browsing-mode", url.Values{"mode": {common.BrowsingModeAllFilterChips}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("seed browsing-mode = %d, want 204", rec.Code)
	}
	_ = d

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="browsing-mode-form"`) || !strings.Contains(body, `hx-post="/api/settings/browsing-mode"`) {
		t.Fatal("Settings has no browsing-mode form, or it posts nowhere")
	}
	for _, mode := range []string{common.BrowsingModeCategoryTabs, common.BrowsingModeAllFilterChips, common.BrowsingModeStripOverflow} {
		if !strings.Contains(body, `value="`+mode+`"`) {
			t.Fatalf("browsing-mode <select> is missing the %q option", mode)
		}
	}
	// The stored mode is the selected option — find its <option> tag.
	idx := strings.Index(body, `value="`+common.BrowsingModeAllFilterChips+`"`)
	tagEnd := strings.Index(body[idx:], ">")
	if !strings.Contains(body[idx:idx+tagEnd], "selected") {
		t.Fatalf("expected the stored mode's option to be selected, got tag: %s", body[idx:idx+tagEnd])
	}
	for _, label := range []string{"Category tiles", "All items with category filters", "Category strip with"} {
		if !strings.Contains(body, label) {
			t.Fatalf("option label %q did not render (missing i18n key?)", label)
		}
	}
	// One helper line per mode is in the markup (the page swaps which one
	// shows client-side) — all three keys must resolve.
	for _, hint := range []string{`data-browsing-mode-hint="category_tabs"`, `data-browsing-mode-hint="all_filter_chips"`, `data-browsing-mode-hint="strip_overflow"`} {
		if !strings.Contains(body, hint) {
			t.Fatalf("browsing-mode helper text for %s did not render", hint)
		}
	}
	// The two retired controls are gone for real, not just hidden.
	for _, retired := range []string{`id="show-all-tab-cb"`, `id="categories-tab-cb"`, "/api/settings/show-all-tab", "/api/settings/categories-tab"} {
		if strings.Contains(body, retired) {
			t.Fatalf("retired sell-screen control %q still renders", retired)
		}
	}
}

// TestBrowsingModeWiredIntoRawUpsert (ut-docs#2499): the generic
// /api/settings/upsert raw key/value table must reflect this key into live
// state (same as every other sale/pos setting in settings_page.go's
// UpdateState switch) — and, since a bad value here would break the sell
// screen's very next render, refuse an out-of-enum value the way it already
// refuses a bad service-charge rate, rather than clamping it silently.
func TestBrowsingModeWiredIntoRawUpsert(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyBrowsingMode}, "value": {common.BrowsingModeStripOverflow}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert %s = %d, want 204", common.KeyBrowsingMode, rec.Code)
	}
	if got := d.CurrentState().BrowsingMode; got != common.BrowsingModeStripOverflow {
		t.Fatalf("upsert did not reflect browsing mode into live state, got %q", got)
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyBrowsingMode}, "value": {"bogus"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("upsert %s=bogus = %d, want 400", common.KeyBrowsingMode, rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyBrowsingMode); v != common.BrowsingModeStripOverflow {
		t.Fatalf("a rejected upsert must not overwrite the stored mode, got %q", v)
	}
	if got := d.CurrentState().BrowsingMode; got != common.BrowsingModeStripOverflow {
		t.Fatalf("a rejected upsert must not change live state, got %q", got)
	}
}

// The retired endpoints must be gone — not 404-by-accident later, but
// gone now (dead-flag rule, ut-docs#2499): a settings row nothing reads
// must have no writer either.
func TestRetiredSellScreenToggleEndpointsAreGone(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	for _, path := range []string{"/api/settings/show-all-tab", "/api/settings/categories-tab"} {
		if rec := postForm(mux, path, url.Values{"enabled": {"true"}}, &mgrUser); rec.Code != http.StatusNotFound {
			t.Fatalf("POST %s = %d, want 404 (retired)", path, rec.Code)
		}
	}
}

// TestStripOverflowSellScreenHasNoAllTab (ut-docs#2613): through the real
// mux — the browsing mode saved via its own settings endpoint, then the
// sell screen's GET /ui/buttons — the category strip renders its category
// tabs and the "…" button but no All tab and no All grid. The chip mode
// (the one mode that still has an All grid) keeps it.
func TestStripOverflowSellScreenHasNoAllTab(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.BtnStore = ui.NewButtonStore(d.Db)
	registerButtonsAPI(mux, d)
	for _, stmt := range []string{
		`INSERT INTO categories(id,name,sort_order,is_active) VALUES ('cat-a','Drinks',0,1),('cat-b','Snacks',1,1)`,
		`INSERT INTO items(id,sku,name,base_price,is_active,category_id) VALUES ('itm-a','A-SKU','Cola',120,1,'cat-a'),('itm-b','B-SKU','Crisps',150,1,'cat-b'),('itm-c','C-SKU','Loose',10,1,NULL)`,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES ('BTNA','Cola','itm-a',0),('BTNB','Crisps','itm-b',1)`,
	} {
		if _, err := d.Db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if rec := postForm(mux, "/api/settings/browsing-mode", url.Values{"mode": {common.BrowsingModeStripOverflow}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save strip_overflow = %d, want 204", rec.Code)
	}
	rec := getWithUser(mux, "/ui/buttons", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`id="cat-tab-cat-a"`, `id="cat-tab-cat-b"`, `id="cat-tab-more"`, `tab: 'cat-a',`} {
		if !strings.Contains(body, want) {
			t.Fatalf("strip_overflow /ui/buttons missing %q: %.3000s", want, body)
		}
	}
	for _, unwanted := range []string{`id="cat-tab-all"`, `id="buttons-grid-all"`, `'__all__'`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("strip_overflow /ui/buttons must not render %q (ut-docs#2613): %.3000s", unwanted, body)
		}
	}

	if rec := postForm(mux, "/api/settings/browsing-mode", url.Values{"mode": {common.BrowsingModeAllFilterChips}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("save all_filter_chips = %d, want 204", rec.Code)
	}
	chips := getWithUser(mux, "/ui/buttons", &mgrUser).Body.String()
	if !strings.Contains(chips, `id="buttons-grid-all"`) || !strings.Contains(chips, `data-name="Loose"`) {
		t.Fatalf("all_filter_chips must keep its All grid of every active item: %.3000s", chips)
	}
}
