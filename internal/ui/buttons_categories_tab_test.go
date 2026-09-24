package ui

import (
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// newCategoriesTabTestHTTP builds a real ButtonsHTTP against a fresh test DB
// — same renderer file set TestButtonsHTTPList_AllTabIsFirstAndDefaultSelected
// (ut-docs#2212) already uses, so the two features are exercised through the
// exact same render path.
func newCategoriesTabTestHTTP(t *testing.T) (*sql.DB, *ButtonStore, *ButtonsHTTP) {
	t.Helper()
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	return db, store, &ButtonsHTTP{Store: *store, View: renderer}
}

// TestButtonsHTTPList_CategoriesTabOffByDefault (ut-docs#2283): a till that
// never opens Settings → Categories tab must render exactly today's tab bar
// — no cat-tab-categories tab, no #cat-panel-categories tile grid.
//
// Review (ut-docs#2283): this MUST seed two real, non-empty top-level
// categories first. buttons.html only renders the tab bar (and the panel
// block the Categories panel lives in) at all when $hasTabs — >= 2
// surviving top-level groups. Against the bare setupFullTestDB fixture
// .Groups is empty, so both id="" assertions below held no matter what
// CategoriesTabEnabled returned, and the test passed unchanged with the
// settings gate ripped out of ButtonStore.CategoriesTabEnabled (verified
// live). The All-tab assertion at the end is the positive control that
// keeps it from silently going vacuous again.
func TestButtonsHTTPList_CategoriesTabOffByDefault(t *testing.T) {
	db, store, h := newCategoriesTabTestHTTP(t)
	seedTwoCategoriesWithButtons(t, db, store)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Positive control: the tab bar really did render, so the two absence
	// assertions below are about the setting, not about an empty catalog.
	if !strings.Contains(body, `id="cat-tab-all"`) {
		t.Fatalf("the tab bar did not render at all — this test would be vacuous, got: %s", body)
	}
	if strings.Contains(body, `id="cat-tab-categories"`) {
		t.Fatalf("expected no Categories tab when the setting is off, got: %s", body)
	}
	if strings.Contains(body, `id="cat-panel-categories"`) {
		t.Fatalf("expected no Categories tile panel when the setting is off, got: %s", body)
	}
}

// seedTwoCategoriesWithButtons gives the render two surviving top-level
// groups, which is what buttons.html's own $hasTabs (>= 2 groups) needs
// before it renders a tab bar at all. Deliberately does NOT touch
// data.SellScreenCategoriesTabKey — callers that want the tab ON set it
// themselves.
func seedTwoCategoriesWithButtons(t *testing.T, db *sql.DB, store *ButtonStore) {
	t.Helper()
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 'cat_food', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	for _, b := range []Button{
		{Label: "Bread", Code: "C1", ItemID: "i1"},
		{Label: "Cola", Code: "C2", ItemID: "i2"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}
}

// TestButtonsHTTPList_CategoriesTabRendersFirst (ut-docs#2283): once
// enabled, the Categories tab is a real, focusable, ARIA-correct tab that
// renders BEFORE every other tab — including the ut-docs#2212 All tab, not
// just before the per-category ones.
func TestButtonsHTTPList_CategoriesTabRendersFirst(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := &ButtonsHTTP{Store: *store, View: renderer}

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 'cat_food', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	for _, b := range []Button{
		{Label: "Bread", Code: "C1", ItemID: "i1"},
		{Label: "Cola", Code: "C2", ItemID: "i2"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}
	if err := data.NewSettingsRepo(db).Set(t.Context(), data.SellScreenCategoriesTabKey, "1"); err != nil {
		t.Fatalf("enable categories tab setting: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	catsIdx := strings.Index(body, `id="cat-tab-categories"`)
	allIdx := strings.Index(body, `id="cat-tab-all"`)
	foodIdx := strings.Index(body, `id="cat-tab-cat_food"`)
	if catsIdx < 0 {
		t.Fatalf("expected a Categories tab button (id=cat-tab-categories), got: %s", body)
	}
	if allIdx < 0 || catsIdx > allIdx {
		t.Fatalf("expected the Categories tab to render before the All tab, got: %s", body)
	}
	if foodIdx < 0 || catsIdx > foodIdx {
		t.Fatalf("expected the Categories tab to render before every per-category tab, got: %s", body)
	}

	// i18n: goes through T, not a hardcoded literal.
	if !strings.Contains(body, "products.categories<") {
		t.Fatalf("expected the products.categories key to render as the Categories tab's label, got: %s", body)
	}

	// The tile grid panel: one tile per top-level category actually
	// rendered (both Food and Drinks have an active button each).
	if !strings.Contains(body, `id="cat-panel-categories"`) {
		t.Fatalf("expected a Categories tile panel (id=cat-panel-categories), got: %s", body)
	}
	// Review fix (ut-docs#2283): unlike the All tab, this tab owns exactly
	// ONE panel and points at it with aria-controls, so that panel has to be
	// a real tabpanel labelled by the tab — not the role="group" it first
	// shipped as, which left the tab's aria-controls pointing at a
	// non-tabpanel.
	if !strings.Contains(body, `id="cat-panel-categories" class="products-tab-panel" role="tabpanel" aria-labelledby="cat-tab-categories"`) {
		t.Fatalf("expected #cat-panel-categories to be a role=tabpanel labelled by #cat-tab-categories, got: %s", body)
	}
	if !strings.Contains(body, `aria-controls="cat-panel-categories"`) {
		t.Fatalf("expected the Categories tab to declare aria-controls for its own panel, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 2 {
		t.Fatalf("expected exactly 2 category tiles (Food, Drinks), got %d: %s", got, body)
	}
	if !strings.Contains(body, `data-cat-name="Food"`) || !strings.Contains(body, `data-cat-name="Drinks"`) {
		t.Fatalf("expected a Food tile and a Drinks tile, got: %s", body)
	}
}

// TestButtonsHTTPList_CategoriesTabHidesZeroActiveItemCategory
// (ut-docs#2283): a category with no active items anywhere in its own
// subtree never gets a tile — same "hide, don't disable" decision the
// per-category tab bar already applies (ui.BuildCategoryGroups prunes an
// empty branch before this template ever sees it).
func TestButtonsHTTPList_CategoriesTabHidesZeroActiveItemCategory(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := &ButtonsHTTP{Store: *store, View: renderer}

	// A third, non-empty category (Drinks) is needed alongside Food so
	// $hasTabs (>=2 SURVIVING top-level groups) is still true once Empty is
	// pruned out entirely — ui.BuildCategoryGroups drops a branch with no
	// buttons anywhere in its subtree before this template ever sees it, so
	// with only Food+Empty in the DB, Empty wouldn't just be tile-less, the
	// WHOLE tab bar (Categories tab included) would never render at all,
	// which would test the wrong thing.
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_empty', 'Empty', NULL, 2),
		('cat_drink', 'Drinks', NULL, 3)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 'cat_food', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	for _, b := range []Button{
		{Label: "Bread", Code: "C1", ItemID: "i1"},
		{Label: "Cola", Code: "C2", ItemID: "i2"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}
	if err := data.NewSettingsRepo(db).Set(t.Context(), data.SellScreenCategoriesTabKey, "1"); err != nil {
		t.Fatalf("enable categories tab setting: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, `data-cat-name="Empty"`) {
		t.Fatalf("expected the zero-active-item category to have no tile, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 2 {
		t.Fatalf("expected exactly 2 category tiles (Food, Drinks — not Empty), got %d: %s", got, body)
	}
}

// TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategories
// (ut-docs#2283): a nested subcategory never gets its own tile — the tile
// grid is top-level categories only (matches category_filter.html's own
// top-level-only convention); its items are still reachable, folded into
// its top-level parent's own item-picker modal (see buttons.html's
// openCategoryPicker comment), never a second-level modal-within-modal.
func TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategories(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := &ButtonsHTTP{Store: *store, View: renderer}

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_food_specials', 'Specials', 'cat_food', 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Pie', 140, 'cat_food_specials', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	for _, b := range []Button{
		{Label: "Pie", Code: "C1", ItemID: "i1"},
		{Label: "Cola", Code: "C2", ItemID: "i2"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}
	if err := data.NewSettingsRepo(db).Set(t.Context(), data.SellScreenCategoriesTabKey, "1"); err != nil {
		t.Fatalf("enable categories tab setting: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Food (top-level, has no OWN button, only a nested one) still gets a
	// tile — it has active items somewhere in its subtree.
	if !strings.Contains(body, `data-cat-name="Food"`) {
		t.Fatalf("expected a Food tile (has active items via its subcategory), got: %s", body)
	}
	// Specials (nested) must NOT get its own tile.
	if strings.Contains(body, `data-cat-name="Specials"`) {
		t.Fatalf("expected no separate tile for the nested Specials subcategory, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 2 {
		t.Fatalf("expected exactly 2 top-level tiles (Food, Drinks), got %d: %s", got, body)
	}
}

// panelSlice returns the substring of body starting at the panel whose id is
// "cat-panel-"+catID, up to (but not including) the next "id=\"cat-panel-"
// occurrence, or to the end of body if there is none. Test helper shared by
// the ut-docs#2498 empty-state assertions below, which need to scope their
// checks to ONE category's panel rather than match anywhere in the page.
func panelSlice(t *testing.T, body, catID string) string {
	t.Helper()
	marker := `id="cat-panel-` + catID + `"`
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("expected a panel %q, got: %s", marker, body)
	}
	rest := body[idx+1:]
	if end := strings.Index(rest, `id="cat-panel-`); end >= 0 {
		return body[idx : idx+1+end]
	}
	return body[idx:]
}

// TestButtonsHTTPList_CategoryWithActiveItemButNoButtonStillAppears
// (ut-docs#2498, the core of this card): a category with a real active item
// but NO quick button anywhere in its own subtree must still surface — both
// as its own tab in the classic per-category tab bar and as its own tile in
// the settings-gated Categories tab tile grid — instead of being silently
// pruned by BuildCategoryGroups the way it used to be (pruning used to key
// ONLY on "has a quick button somewhere in the subtree"). Its panel/section
// must render the new translated empty-state message rather than an empty
// grid, since there is nothing to grid.
//
// ut-docs#2541 review finding 5: a category whose only active item(s) are
// ALL hidden from the sell screen must not keep surviving
// BuildCategoryGroups' pruning with an empty group — CatalogRepo.
// ListCategoriesForAdmin's VisibleItemCount (unlike its admin-facing
// ItemCount) excludes hidden items, so a category like Drinks below, whose
// one item is hidden, is now pruned from the strip entirely: no tab, no
// tile, nothing to show an empty-state message for.
//
// Before this fix (previous name:
// TestButtonsHTTPList_CategoryWithActiveItemButNoButtonStillAppears): this
// same setup asserted the OPPOSITE — that Drinks kept showing with an
// empty-state message — which was itself the bug this finding fixes.
func TestButtonsHTTPList_CategoryWithOnlyHiddenItemsIsPruned(t *testing.T) {
	db, store, h := newCategoriesTabTestHTTP(t)

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 'cat_food', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	// Only Food ever gets a quick button. Drinks' one item (Cola) is
	// hidden, so Drinks has neither a button nor a VISIBLE item.
	if err := store.Add(Button{Label: "Bread", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Hide(t.Context(), "i2"); err != nil {
		t.Fatalf("Hide: %v", err)
	}
	if err := data.NewSettingsRepo(db).Set(t.Context(), data.SellScreenCategoriesTabKey, "1"); err != nil {
		t.Fatalf("enable categories tab setting: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, `id="cat-tab-cat_drink"`) {
		t.Fatalf("expected NO Drinks tab -- its only item is hidden, so it has neither a button nor a visible item, got: %s", body)
	}
	if strings.Contains(body, `data-cat-name="Drinks"`) {
		t.Fatalf("expected no Drinks tile in the Categories tab either, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 1 {
		t.Fatalf("expected exactly 1 category tile (Food only, Drinks pruned), got %d: %s", got, body)
	}
	// Food's panel DOES have a button -- unaffected by the fix.
	foodPanel := panelSlice(t, body, "cat_food")
	if strings.Contains(foodPanel, `data-testid="category-empty-state"`) {
		t.Fatalf("expected no empty-state marker in Food's panel (it has a quick button), got: %s", foodPanel)
	}
}

// TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategoryPrunedWhenHidden
// (ut-docs#2541 review finding 5, replaces the old
// TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategoryWithNoButtons):
// a nested subcategory whose only item is HIDDEN has neither a button nor a
// visible item, so it (and its now-childless, item-less parent) must be
// pruned entirely — not kept around to show an empty-state message, which
// was the pre-fix (buggy) behaviour this test used to pin.
func TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategoryPrunedWhenHidden(t *testing.T) {
	db, store, h := newCategoriesTabTestHTTP(t)

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_food_specials', 'Specials', 'cat_food', 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Pie', 140, 'cat_food_specials', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	// Cola gets a quick button (needed so Drinks survives as a second
	// top-level group alongside Food, same as the sibling flatten test);
	// Pie (Food's only item, nested under Specials) is hidden, so it has
	// zero buttons anywhere.
	if err := store.Add(Button{Label: "Cola", Code: "C2", ItemID: "i2"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Hide(t.Context(), "i1"); err != nil {
		t.Fatalf("Hide: %v", err)
	}
	if err := data.NewSettingsRepo(db).Set(t.Context(), data.SellScreenCategoriesTabKey, "1"); err != nil {
		t.Fatalf("enable categories tab setting: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Pie (Specials' only item) is hidden -> Specials has neither a button
	// nor a visible item -> pruned; Food then has no children and no direct
	// items of its own either -> pruned too. Only Drinks (Cola, a real
	// button) survives.
	if strings.Contains(body, `data-cat-name="Food"`) {
		t.Fatalf("expected no Food tile -- its only content (Specials/Pie) is hidden, got: %s", body)
	}
	if strings.Contains(body, `data-cat-name="Specials"`) {
		t.Fatalf("expected no Specials tile either, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 1 {
		t.Fatalf("expected exactly 1 top-level tile (Drinks only), got %d: %s", got, body)
	}
}

// TestButtonsHTTPList_DefaultTabPrefersGroupWithButtons (ut-docs#2498, review
// finding): with the All tab off, the sale screen's default landing tab used
// to be simply Groups[0] -- fine when every surviving group has buttons, but
// a group could survive with zero buttons anywhere (kept via an active-item
// count alone), and landing there by default would greet the operator with
// the empty-state message instead of real quick buttons on first paint. The
// template's default-tab pick (buttons.html) must skip to the first group
// that HasButtons, falling back to Groups[0] only if none do.
//
// ut-docs#2541 review finding 5 retired this test's own ORIGINAL two-group
// scenario: it used to make Household "survive with zero buttons" by hiding
// its only item (Sponge) while Food kept a real button, so the template had
// two groups to pick between. That is now exactly the case finding 5 fixed
// -- VisibleItemCount excludes hidden items, so Household is pruned outright
// rather than surviving empty, leaving only Food -- so a real, hidden-item-
// based two-group "first group has no buttons" state can no longer be
// constructed through the HTTP layer at all (every active, VISIBLE item is
// always an implicit quick button of its own now, so any surviving group
// always HasButtons=true). The underlying template selection logic (skip to
// the first HasButtons group) stays covered directly, independent of real
// hidden-item plumbing, by TestBuildCategoryGroups_HasButtonsPropagatesFromDescendant
// and its neighbours in buttons_category_groups_test.go, which build
// itemCounts by hand. This test now only pins that Household, once pruned,
// plays no part in the render and Food's tile still shows.
func TestButtonsHTTPList_DefaultTabPrefersGroupWithButtons(t *testing.T) {
	db, store, h := newCategoriesTabTestHTTP(t)
	h.HideAllTab = true

	// Household sorts first but its only item is hidden (pruned entirely);
	// Food sorts second and has a real quick button.
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_household', 'Household', NULL, 1),
		('cat_food', 'Food', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Sponge', 140, 'cat_household', 1),
		('i2', 'S2', 'Bread', 120, 'cat_food', 1)`)
	if err := store.Add(Button{Label: "Bread", Code: "C1", ItemID: "i2"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Hide(t.Context(), "i1"); err != nil {
		t.Fatalf("Hide: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, "Household") || strings.Contains(body, "Sponge") {
		t.Fatalf("expected Household pruned entirely (its only item is hidden), got: %s", body)
	}
	if !strings.Contains(body, "Bread") {
		t.Fatalf("expected Food's own tile (Bread) to still render, got: %s", body)
	}
}
