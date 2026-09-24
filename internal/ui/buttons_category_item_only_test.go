package ui

import (
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2498's strip-mode regression tests, carried over from the retired
// buttons_categories_tab_test.go when ut-docs#2499 merged on top of it: the
// ut-docs#2283 Categories tab those tests also asserted on is gone (its
// tile grid is now the category_tabs browsing mode, covered in
// buttons_browsing_mode_test.go), but #2498's own fix — a category with
// active items and zero quick buttons still surfaces on the strip, with an
// empty-state message, and never becomes the default tab over a category
// that has buttons — is still live in strip_overflow mode and keeps its
// coverage here. The zero-value BrowsingMode renders the strip (see
// ButtonsHTTP.BrowsingMode), which is what these tests exercise.

func newItemOnlyStripTestHTTP(t *testing.T) (*sql.DB, *ButtonStore, *ButtonsHTTP) {
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

// panelSlice returns the substring of body starting at the panel whose id is
// "cat-panel-"+catID, up to (but not including) the next "id=\"cat-panel-"
// occurrence, or to the end of body if there is none.
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

// ut-docs#2541 review finding 5 (ported onto ut-docs#2499's strip-mode
// copy of these #2498 tests): every active, VISIBLE item is an implicit
// quick button now, so the #2498 "category with active items but zero
// buttons" state can only be reached by hiding the category's items — and
// CatalogRepo.ListCategoriesForAdmin's VisibleItemCount (unlike the admin-
// facing ItemCount) excludes hidden items, so such a category is pruned
// from the strip outright instead of surviving with an empty-state message.
// Previous name: TestButtonsHTTPList_CategoryWithActiveItemButNoButtonStillAppears,
// which pinned the opposite (the bug finding 5 fixed).
func TestButtonsHTTPList_CategoryWithOnlyHiddenItemsIsPruned(t *testing.T) {
	db, store, h := newItemOnlyStripTestHTTP(t)

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 'cat_food', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	if err := store.Add(Button{Label: "Bread", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Hide(t.Context(), "i2"); err != nil {
		t.Fatalf("Hide: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, `id="cat-tab-cat_drink"`) {
		t.Fatalf("expected NO Drinks tab -- its only item is hidden, got: %s", body)
	}
	// ut-docs#2613: with Drinks pruned and no All tab any more, Food is the
	// only group left — the single-category branch (Food's own headed
	// group, no tab bar), not a tabbed panel.
	if strings.Contains(body, `class="tab-bar"`) {
		t.Fatalf("expected no tab bar with a single surviving category, got: %s", body)
	}
	if !strings.Contains(body, `data-name="Bread"`) || strings.Contains(body, `data-testid="category-empty-state"`) {
		t.Fatalf("expected Food's Bread tile and no empty-state marker (it has a quick button), got: %s", body)
	}
}

// TestButtonsHTTPList_ItemOnlyCategoryShowsImplicitTile (ut-docs#2541): a
// category whose active item has no shortcut_buttons row still gets its own
// strip tab (the #2498 guarantee) — and now its panel holds that item as an
// implicit tile rather than the #2498 empty-state message.
func TestButtonsHTTPList_ItemOnlyCategoryShowsImplicitTile(t *testing.T) {
	db, store, h := newItemOnlyStripTestHTTP(t)

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_food_specials', 'Specials', 'cat_food', 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Pie', 140, 'cat_food_specials', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	if err := store.Add(Button{Label: "Cola", Code: "C2", ItemID: "i2"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `id="cat-tab-cat_food"`) {
		t.Fatalf("expected a Food tab (active item via its subcategory), got: %s", body)
	}
	foodPanel := panelSlice(t, body, "cat_food")
	if !strings.Contains(foodPanel, `data-name="Pie"`) {
		t.Fatalf("expected Pie as an implicit tile in Food's panel, got: %s", foodPanel)
	}
	if strings.Contains(foodPanel, `data-testid="category-empty-state"`) {
		t.Fatalf("expected no empty-state marker -- Pie is an implicit tile, got: %s", foodPanel)
	}
}

// TestButtonsHTTPList_DefaultTabPrefersGroupWithButtons (ut-docs#2498;
// ut-docs#2541 review finding 5): a category whose only item is hidden is
// pruned outright, so the landing tab is Food (the
// first surviving group; Drinks, an implicit-tile-only group, follows). The template's "skip to the first HasButtons
// group" pick itself stays covered by buttons_category_groups_test.go,
// which builds itemCounts by hand — a surviving zero-button group can no
// longer be built through the HTTP layer.
func TestButtonsHTTPList_DefaultTabPrefersGroupWithButtons(t *testing.T) {
	db, store, h := newItemOnlyStripTestHTTP(t)

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_household', 'Household', NULL, 1),
		('cat_food', 'Food', NULL, 2),
		('cat_drink', 'Drinks', NULL, 3)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Sponge', 140, 'cat_household', 1),
		('i2', 'S2', 'Bread', 120, 'cat_food', 1),
		('i3', 'S3', 'Cola', 110, 'cat_drink', 1)`)
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
	if strings.Contains(body, `id="cat-tab-cat_household"`) {
		t.Fatalf("expected Household pruned (its only item is hidden), got: %s", body)
	}
	if !strings.Contains(body, `tab: 'cat_food'`) {
		t.Fatalf("expected the default tab to be Food, got: %s", body)
	}
}
