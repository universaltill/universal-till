package ui

import (
	"database/sql"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// newButtonsHTTP wires ButtonsHTTP exactly as internal/pages does: the
// real embedded templates via NewRenderer, the real store over sqlite.
func newButtonsHTTP(t *testing.T, partial string) (*ButtonsHTTP, *ButtonStore) {
	t.Helper()
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", partial),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Coffee', 350, 1)`)
	return &ButtonsHTTP{Store: *store, View: renderer}, store
}

func TestButtonsHTTPList_RendersTiles(t *testing.T) {
	h, store := newButtonsHTTP(t, "buttons.html")
	if err := store.Add(Button{Label: "Coffee Tile", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Coffee Tile") {
		t.Fatalf("rendered fragment missing tile label: %s", rec.Body.String())
	}
}

func TestButtonsHTTPAdd_NormalizesImageAndRendersGrid(t *testing.T) {
	h, store := newButtonsHTTP(t, "buttons_admin.html")

	cases := []struct {
		in, want string
	}{
		{"coffee.png", "/public/images/coffee.png"},                // bare filename -> local images folder
		{"/public/uploads/c.png", "/public/uploads/c.png"},         // already public path, untouched
		{"https://cdn.example/c.png", "https://cdn.example/c.png"}, // absolute URL, untouched
	}
	for i, tc := range cases {
		form := url.Values{"label": {"Coffee"}, "code": {"C" + string(rune('1'+i))}, "itemId": {"i1"}, "imageUrl": {tc.in}}
		req := httptest.NewRequest("POST", "/api/buttons/add", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.Add(rec, req)
		if rec.Code != 200 {
			t.Fatalf("Add(%q) = %d (%s)", tc.in, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "buttons-grid-admin") {
			t.Fatalf("Add did not re-render admin grid: %s", rec.Body.String())
		}
	}
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != len(cases) {
		t.Fatalf("buttons = %d, want %d", len(btns), len(cases))
	}
	got := map[string]string{}
	for _, b := range btns {
		got[b.Code] = b.ImageURL
	}
	for i, tc := range cases {
		code := "C" + string(rune('1'+i))
		if got[code] != tc.want {
			t.Fatalf("image for %s = %q, want %q", code, got[code], tc.want)
		}
	}
}

func TestButtonsHTTPAdd_ErrorPaths(t *testing.T) {
	h, _ := newButtonsHTTP(t, "buttons_admin.html")

	// Store validation error -> 400 (missing itemId).
	form := url.Values{"label": {"X"}, "code": {"C9"}}
	req := httptest.NewRequest("POST", "/api/buttons/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Add(rec, req)
	if rec.Code != 400 {
		t.Fatalf("Add missing fields = %d, want 400", rec.Code)
	}

	// Malformed percent-encoding -> ParseForm error -> 400.
	req = httptest.NewRequest("POST", "/api/buttons/add", strings.NewReader("label=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.Add(rec, req)
	if rec.Code != 400 {
		t.Fatalf("Add bad form = %d, want 400", rec.Code)
	}
}

// TestButtonsHTTPAdd_StoreValidationErrorRendersNonEmptyHTMLBody
// (ut-docs#1220): a bare http.Error(w, err.Error(), 400) here used to leave
// the response body as a raw Go error string with Content-Type text/plain
// -- and, worse, buttons_admin.html's search-result button unconditionally
// hid the search dropdown on htmx:afterRequest (success or not) with
// nothing else listening for the failure, so the 400 was completely
// invisible to the operator: tap a SKU-only result, dropdown closes, no
// tile appears, no error shown anywhere. This pins the mechanically
// checkable half of the fix: the failure response must carry a non-empty,
// non-raw-error HTML body a page-level htmx:responseError listener can
// swap into view (see buttons_admin.html's own listener + #buttons-add-error
// for the client half).
func TestButtonsHTTPAdd_StoreValidationErrorRendersNonEmptyHTMLBody(t *testing.T) {
	h, _ := newButtonsHTTP(t, "buttons_admin.html")

	// Missing label -> ButtonStore.Add's "label and itemId are required"
	// validation error. (Missing code alone no longer errors as of
	// ut-docs#1459 -- Add now synthesizes a stable code from itemId, so
	// this test's original "missing code" trigger would silently start
	// asserting a 200, not the 400 it means to pin.)
	form := url.Values{"itemId": {"i1"}}
	req := httptest.NewRequest("POST", "/api/buttons/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Add(rec, req)

	if rec.Code != 400 {
		t.Fatalf("Add with missing label = %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	if strings.TrimSpace(body) == "" {
		t.Fatalf("expected a non-empty HTML error body, got blank response")
	}
	if strings.Contains(body, "label and itemId are required") {
		t.Fatalf("raw Go validation error text leaked into the operator-facing response: %s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html (so the client's innerHTML swap renders it as markup)", ct)
	}
}

func TestButtonsHTTPRemove_DeletesAndRerenders(t *testing.T) {
	h, store := newButtonsHTTP(t, "buttons_admin.html")
	if err := store.Add(Button{Label: "Coffee", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	form := url.Values{"code": {"C1"}}
	req := httptest.NewRequest("POST", "/api/buttons/remove", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Remove(rec, req)
	if rec.Code != 200 {
		t.Fatalf("Remove = %d (%s)", rec.Code, rec.Body.String())
	}
	if btns, _ := store.Load(); len(btns) != 0 {
		t.Fatalf("button not removed: %+v", btns)
	}

	// Malformed body -> 400.
	req = httptest.NewRequest("POST", "/api/buttons/remove", strings.NewReader("code=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.Remove(rec, req)
	if rec.Code != 400 {
		t.Fatalf("Remove bad form = %d, want 400", rec.Code)
	}
}

func TestButtonsHTTPRemove_StoreErrorIs400(t *testing.T) {
	db := setupFullTestDB(t)
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons_admin.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := &ButtonsHTTP{Store: *store, View: renderer}
	db.Close() // repo delete will fail

	form := url.Values{"code": {"C1"}}
	req := httptest.NewRequest("POST", "/api/buttons/remove", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Remove(rec, req)
	if rec.Code != 400 {
		t.Fatalf("Remove with failing store = %d, want 400", rec.Code)
	}
}

// newButtonsHTTPWithDB is newButtonsHTTP plus the underlying *sql.DB, for
// tests that need to seed categories directly (ButtonStore itself has no
// category-writing method — categories come from the catalog/import side).
func newButtonsHTTPWithDB(t *testing.T, partial string) (*ButtonsHTTP, *sql.DB) {
	t.Helper()
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", partial),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	return &ButtonsHTTP{Store: *store, View: renderer}, db
}

// TestButtonsHTTPList_RendersNestedColorCodedGroups drives the real
// rendered HTML (not just the pure grouping function) — the highest-risk
// part of this feature, since html/template's CSS-context escaping can
// silently mangle an inline style value (rewriting anything it doesn't
// like to "ZgotmplZ", with no error) if a color value is ever malformed.
// This pins the happy path stays clean: a real hex color survives verbatim
// and a nested category structurally nests in the HTML output.
func TestButtonsHTTPList_RendersNestedColorCodedGroups(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")

	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('drinks','Drinks',NULL,0,NULL)`)
	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('hot','Hot Drinks','drinks',0,'#1D4ED8')`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm1','S1','Latte', 320, 1, 'hot')`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B1','Latte','itm1',0)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, "ZgotmplZ") {
		t.Fatalf("html/template mangled a color value: %s", body)
	}
	if !strings.Contains(body, "--cat-color: #1D4ED8") {
		t.Fatalf("expected the explicit hex color to survive verbatim in the style attribute, got: %s", body)
	}
	drinksIdx := strings.Index(body, ">Drinks<")
	hotIdx := strings.Index(body, ">Hot Drinks<")
	latteIdx := strings.Index(body, "Latte")
	if drinksIdx == -1 || hotIdx == -1 || latteIdx == -1 {
		t.Fatalf("expected Drinks, Hot Drinks and Latte all present, got: %s", body)
	}
	if !(drinksIdx < hotIdx && hotIdx < latteIdx) {
		t.Fatalf("expected Drinks, then nested Hot Drinks, then Latte in document order, got: %s", body)
	}
	if strings.Contains(body, "Uncategorized") {
		t.Fatalf("did not expect an uncategorized bucket when every button has a category: %s", body)
	}
}

// TestButtonsHTTPList_FlatWhenNoCategoriesConfigured pins the fallback: a
// till with no categories set on anything (every existing till today, since
// the grid was flat until this feature) must render its tiles flat, not
// under a single pointless "Uncategorized" header — a real regression an
// earlier version of this change shipped with (caught by independent
// review) since every button's synthetic bucket was still just "a group".
func TestButtonsHTTPList_FlatWhenNoCategoriesConfigured(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','S1','Loose Sweet', 10, 1)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B1','Loose Sweet','itm1',0)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, "Uncategorized") {
		t.Fatalf("expected no Uncategorized header when nothing has a category, got: %s", body)
	}
	if strings.Contains(body, "category-header") {
		t.Fatalf("expected no category header at all in the flat fallback, got: %s", body)
	}
	if !strings.Contains(body, "Loose Sweet") {
		t.Fatalf("expected the tile to still render, got: %s", body)
	}
}

// TestButtonsHTTPList_RendersTabBarAndSearchWithMultipleCategories pins
// ut-docs#418: once there are >=2 top-level categories, the sale screen
// gets a tab bar (one tab per root, in Groups order) plus a search input —
// both scoped by the shared Alpine "products-finder" data so a tile's
// visibility is driven by both the active tab (x-show on the tab panel)
// and the search query (x-show on the tile itself), entirely client-side
// (see app.css's ut-docs#418 comment for why: no server round trip means
// no risk of one filter silently dropping the other, the bug class
// ut-docs#419 fixes on the self-order kiosk side).
func TestButtonsHTTPList_RendersTabBarAndSearchWithMultipleCategories(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")

	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('drinks','Drinks',NULL,0,NULL)`)
	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('food','Food',NULL,1,NULL)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm1','S1','Cola', 150, 1, 'drinks')`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm2','S2','Burger', 650, 1, 'food')`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B1','Cola','itm1',0)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B2','Burger','itm2',1)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, `id="products-search"`) {
		t.Fatalf("expected a search input on the sale screen, got: %s", body)
	}
	if !strings.Contains(body, `class="tab-bar"`) {
		t.Fatalf("expected a tab bar once >=2 categories exist, got: %s", body)
	}
	if !strings.Contains(body, ">Drinks<") || !strings.Contains(body, ">Food<") {
		t.Fatalf("expected a tab per top-level category, got: %s", body)
	}
	if !strings.Contains(body, `data-name="Cola"`) || !strings.Contains(body, `data-name="Burger"`) {
		t.Fatalf("expected each tile to carry data-name for the search filter, got: %s", body)
	}
	// Exactly one tab starts active — the first group's ID drives the
	// shared x-data seed, so its own tab panel (and only its own) shows
	// x-show="tab === 'drinks'" wired to that same value. Checked as a
	// substring, not the full x-data literal (ut-docs#422 added the
	// matches/sectionHasMatch methods alongside tab/q, reformatting it
	// onto multiple lines — the seeded initial tab is what this test
	// actually pins, not the surrounding object's exact layout).
	if !strings.Contains(body, `tab: 'drinks'`) {
		t.Fatalf("expected the first root category to seed the initial active tab, got: %s", body)
	}
}

// TestButtonsHTTPList_NoTabBarWithOneCategory: a single real category has
// nothing to switch between, so no tab bar renders — search alone still
// applies. Distinct from the fully-flat (zero categories) case above.
func TestButtonsHTTPList_NoTabBarWithOneCategory(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")

	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('drinks','Drinks',NULL,0,NULL)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm1','S1','Cola', 150, 1, 'drinks')`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B1','Cola','itm1',0)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, `class="tab-bar"`) {
		t.Fatalf("did not expect a tab bar with only one top-level category, got: %s", body)
	}
	if !strings.Contains(body, `id="products-search"`) {
		t.Fatalf("expected search to still be present with only one category, got: %s", body)
	}
}

// TestButtonsHTTPList_TabsCarryColorWithMultipleCategories: independent
// review of ut-docs#418 found that TestButtonsHTTPList_RendersNestedColorCodedGroups
// (above) seeds only ONE root category, which renders via "category-group"
// (unchanged since before this feature) — not the >=2-category tab-bar
// branch real multi-category tills actually take. That left the tab bar's
// own color handling completely untested: the first draft of this feature
// dropped every category's --cat-color the moment a till had 2+ categories
// (tabs carried no color at all), and this specific test would have kept
// passing throughout since it never exercises that branch. Pins the fix in
// the branch that matters instead.
func TestButtonsHTTPList_TabsCarryColorWithMultipleCategories(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")

	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('drinks','Drinks',NULL,0,'#1D4ED8')`)
	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('food','Food',NULL,1,'#AA0011')`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm1','S1','Cola', 150, 1, 'drinks')`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm2','S2','Burger', 650, 1, 'food')`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B1','Cola','itm1',0)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B2','Burger','itm2',1)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, "ZgotmplZ") {
		t.Fatalf("html/template mangled a color value: %s", body)
	}
	if !strings.Contains(body, "--cat-color: #1D4ED8") || !strings.Contains(body, "--cat-color: #AA0011") {
		t.Fatalf("expected both explicit tab colors to survive verbatim on the tab bar, got: %s", body)
	}
}

// TestButtonsHTTPList_TopLevelTabPanelCarriesColorForDirectTiles: ut-docs#1325
// (product tiles should show their category's color). The tab bar carries
// --cat-color on the <button> itself (pinned by
// TestButtonsHTTPList_TabsCarryColorWithMultipleCategories above), but a
// top-level category's OWN buttons render inside its tab panel
// (id="cat-panel-<id>") via the "category-group-body" template — a
// completely separate element from the tab button, not a descendant of it.
// CSS custom properties only inherit down the DOM tree, so a .btn-tile
// under that panel has no --cat-color in scope at all unless the panel
// itself also carries it. Only a NESTED subcategory (rendered via
// "category-group") gets its own --cat-color — so this gap is invisible
// for any till whose top-level categories are flat (no subcategories),
// which is the common case. Pins that the panel wrapper carries the same
// color as its tab, so every .btn-tile underneath — nested or not —
// resolves the right var(--cat-color, ...) via inheritance.
func TestButtonsHTTPList_TopLevelTabPanelCarriesColorForDirectTiles(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")

	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('drinks','Drinks',NULL,0,'#1D4ED8')`)
	mustExec(t, db, `INSERT INTO categories(id,name,parent_id,sort_order,color) VALUES('food','Food',NULL,1,'#AA0011')`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm1','S1','Cola', 150, 1, 'drinks')`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('itm2','S2','Burger', 650, 1, 'food')`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B1','Cola','itm1',0)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B2','Burger','itm2',1)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d", rec.Code)
	}
	body := rec.Body.String()

	drinksPanel := extractBetween(t, body, `id="cat-panel-drinks"`, `id="cat-panel-food"`)
	if !strings.Contains(drinksPanel, "--cat-color: #1D4ED8") {
		t.Fatalf("expected the Drinks tab panel (ancestor of its own tiles) to carry --cat-color: #1D4ED8 so Cola's tile inherits it, got panel: %s", drinksPanel)
	}
	if !strings.Contains(drinksPanel, "Cola") {
		t.Fatalf("test setup sanity: expected Cola inside the Drinks panel, got: %s", drinksPanel)
	}

	foodPanel := body[strings.Index(body, `id="cat-panel-food"`):]
	if !strings.Contains(foodPanel, "--cat-color: #AA0011") {
		t.Fatalf("expected the Food tab panel (ancestor of its own tiles) to carry --cat-color: #AA0011 so Burger's tile inherits it, got panel: %s", foodPanel)
	}
	if !strings.Contains(foodPanel, "Burger") {
		t.Fatalf("test setup sanity: expected Burger inside the Food panel, got: %s", foodPanel)
	}
}

// extractBetween returns the substring of s starting at the first
// occurrence of start (inclusive) up to the first occurrence of end after
// it (exclusive) — used to scope an assertion to one tab panel's own
// markup rather than the whole document.
func extractBetween(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i == -1 {
		t.Fatalf("marker %q not found in: %s", start, s)
	}
	j := strings.Index(s[i:], end)
	if j == -1 {
		t.Fatalf("marker %q not found after %q in: %s", end, start, s)
	}
	return s[i : i+j]
}

func TestNewRenderer_ErrorOnMissingTemplate(t *testing.T) {
	_, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "does-not-exist.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err == nil {
		t.Fatalf("expected error for missing template")
	}
}
