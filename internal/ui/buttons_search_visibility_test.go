package ui

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// TestButtonsHTTPList_CategoryGroupSectionsCarrySearchVisibilityWiring
// (ut-docs#422): each category-group section must be individually
// hideable based on whether it currently has any search-matching tile,
// and every tab panel must carry a "no matches" message — otherwise a
// search that empties one subcategory but not a sibling strands a
// pointless header, and a search matching nothing anywhere in the active
// tab leaves a blank panel with no feedback. The demo/e2e-seeded catalog
// only ever surfaces one live subcategory under a tab (Food > Dairy —
// Bakery/Snack/Frozen have zero shortcut_buttons and get pruned), so this
// test seeds its own isolated two-subcategory fixture rather than relying
// on shared demo data or newButtonsHTTP's own single pre-seeded item.
func TestButtonsHTTPList_CategoryGroupSectionsCarrySearchVisibilityWiring(t *testing.T) {
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

	// A second top-level category so $hasTabs is true (BuildCategoryGroups
	// needs >=2 root groups for a tab bar to render at all), plus two real
	// subcategories under "Food" so both the stranded-header case and the
	// tab-panel no-matches case are exercised by the same fixture.
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_dairy', 'Dairy', 'cat_food', 1),
		('cat_bakery', 'Bakery', 'cat_food', 2),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Milk', 150, 'cat_dairy', 1),
		('i2', 'S2', 'Bread', 140, 'cat_bakery', 1),
		('i3', 'S3', 'Cola', 120, 'cat_drink', 1)`)
	for _, b := range []Button{
		{Label: "Milk", Code: "C1", ItemID: "i1"},
		{Label: "Bread", Code: "C2", ItemID: "i2"},
		{Label: "Cola", Code: "C3", ItemID: "i3"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Count(body, `x-show="sectionHasMatch($el)"`) < 2 {
		t.Fatalf("expected both Dairy and Bakery category-group sections to carry sectionHasMatch($el) wiring, got: %s", body)
	}
	if !strings.Contains(body, `x-show="!sectionHasMatch($el.parentElement)"`) {
		t.Fatalf("expected a no-matches message wired to sectionHasMatch($el.parentElement) inside a tab panel, got: %s", body)
	}
	// This package's tests never wire a real translator (no InitI18n call),
	// so T falls back to the raw key — assert on that rather than the
	// translated English string, which guard-i18n.sh (not this test) is
	// what actually enforces exists in every locale file.
	if !strings.Contains(body, "products.no_matches") {
		t.Fatalf("expected the products.no_matches key to render in the tab panel, got: %s", body)
	}
}

// TestButtonsHTTPList_SingleCategoryNoTabsAlsoCarriesNoMatchesMessage
// (ut-docs#422 review finding): the single-real-category, no-tab-bar
// branch reuses "category-group" too, so it inherits that template's
// x-show="sectionHasMatch($el)" the same as any tabbed subcategory — a
// search matching nothing here hides the section just like it would under
// a tab. Without a no-matches message in THIS branch as well, that would
// leave a fully blank panel with strictly less feedback than even the
// pre-#422 behavior (which at least kept the header visible) — a
// regression, not just the pre-existing $flat-branch gap this card
// explicitly left as a non-goal.
func TestButtonsHTTPList_SingleCategoryNoTabsAlsoCarriesNoMatchesMessage(t *testing.T) {
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

	// Exactly ONE real category and nothing uncategorized -> $flat is
	// false (a real category ID exists) and $hasTabs is false (only one
	// root group) -> the "else" (no tab bar) branch renders.
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_drink', 'Drinks', NULL, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Cola', 120, 'cat_drink', 1)`)
	if err := store.Add(Button{Label: "Cola", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, `class="tab-bar"`) {
		t.Fatalf("fixture should have no tab bar (single real category), got: %s", body)
	}
	if !strings.Contains(body, `x-show="sectionHasMatch($el)"`) {
		t.Fatalf("expected the lone category-group section to carry sectionHasMatch($el) wiring, got: %s", body)
	}
	if !strings.Contains(body, `x-show="!sectionHasMatch($el.parentElement)"`) {
		t.Fatalf("expected a no-matches message in the no-tab-bar branch too, got: %s", body)
	}
}

// TestButtonsHTTPList_FlatCatalogAlsoCarriesNoMatchesMessage (ut-docs#914,
// ut-docs#422 follow-up): a till where no category has ever been assigned
// to anything renders via the $flat branch — a bare .grid with no
// category-group wrapper, so it never inherited the sectionHasMatch
// wiring #422 added to the tabbed and single-real-category branches. A
// search matching nothing there left a blank grid with zero feedback.
// This is the last of the three render branches to gain the same
// no-matches message; the other two are covered by the tests above.
func TestButtonsHTTPList_FlatCatalogAlsoCarriesNoMatchesMessage(t *testing.T) {
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

	// No categories inserted at all -> BuildCategoryGroups's only group is
	// the synthetic uncategorized bucket (ID == "") -> $flat is true.
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES
		('i1', 'S1', 'Cola', 120, 1)`)
	if err := store.Add(Button{Label: "Cola", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, `class="tab-bar"`) {
		t.Fatalf("fixture should have no tab bar (flat catalog), got: %s", body)
	}
	if strings.Contains(body, `class="category-group"`) {
		t.Fatalf("fixture should render the bare $flat grid, not a category-group section, got: %s", body)
	}
	if !strings.Contains(body, `x-show="!sectionHasMatch($el.parentElement)"`) {
		t.Fatalf("expected a no-matches message in the $flat branch too, got: %s", body)
	}
	if !strings.Contains(body, "products.no_matches") {
		t.Fatalf("expected the products.no_matches key to render in the $flat branch, got: %s", body)
	}
}
