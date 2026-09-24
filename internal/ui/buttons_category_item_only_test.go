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

// TestButtonsHTTPList_CategoryWithActiveItemButNoButtonStillAppears
// (ut-docs#2498): a category with a real active item but NO quick button
// anywhere in its subtree still gets its own strip tab, and its panel
// renders the translated empty-state message rather than an empty grid.
func TestButtonsHTTPList_CategoryWithActiveItemButNoButtonStillAppears(t *testing.T) {
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

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `id="cat-tab-cat_drink"`) {
		t.Fatalf("expected a Drinks tab (has an active item, Cola) even though it has no quick button, got: %s", body)
	}
	drinksPanel := panelSlice(t, body, "cat_drink")
	if !strings.Contains(drinksPanel, `data-testid="category-empty-state"`) {
		t.Fatalf("expected the Drinks panel to render the empty-state marker, got: %s", drinksPanel)
	}
	if !strings.Contains(drinksPanel, "products.category_no_buttons<") {
		t.Fatalf("expected the products.category_no_buttons key to render in the Drinks panel, got: %s", drinksPanel)
	}
	foodPanel := panelSlice(t, body, "cat_food")
	if strings.Contains(foodPanel, `data-testid="category-empty-state"`) {
		t.Fatalf("expected no empty-state marker in Food's panel (it has a quick button), got: %s", foodPanel)
	}
}

// TestButtonsHTTPList_NestedSubcategoryWithNoButtonsShowsOneEmptyState
// (ut-docs#2498): a top-level category whose only item sits in a nested
// subcategory with no quick button keeps its strip tab, and its panel shows
// the empty-state message exactly once (under the subcategory's own header,
// not a second bare copy from the parent).
func TestButtonsHTTPList_NestedSubcategoryWithNoButtonsShowsOneEmptyState(t *testing.T) {
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
	if got := strings.Count(foodPanel, `data-testid="category-empty-state"`); got != 1 {
		t.Fatalf("expected exactly 1 empty-state message in Food's panel, got %d: %s", got, foodPanel)
	}
}

// TestButtonsHTTPList_DefaultTabPrefersGroupWithButtons (ut-docs#2498): with
// the All tab off, the default landing tab skips an items-only category
// (zero quick buttons) in favour of the first one that HasButtons.
func TestButtonsHTTPList_DefaultTabPrefersGroupWithButtons(t *testing.T) {
	db, store, h := newItemOnlyStripTestHTTP(t)
	h.HideAllTab = true

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_household', 'Household', NULL, 1),
		('cat_food', 'Food', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Sponge', 140, 'cat_household', 1),
		('i2', 'S2', 'Bread', 120, 'cat_food', 1)`)
	if err := store.Add(Button{Label: "Bread", Code: "C1", ItemID: "i2"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `tab: 'cat_food'`) {
		t.Fatalf("expected the default tab to be Food (has a quick button), not Household (items-only), got: %s", rec.Body.String())
	}
}
