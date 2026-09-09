package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestItemsPage_RendersFiveSectionsWithNameAndSubtitle(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{"Library", "Categories", "Inventory", "Modifiers", "Option sets"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected section name %q in body", want)
		}
	}
	for _, want := range []string{
		"Your products and services",
		"Organise your items",
		"Track what&#39;s in stock", // html/template escapes the apostrophe
		"Add extras to items at checkout",
		"Quickly create item variations",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected subtitle %q in body", want)
		}
	}
}

func TestItemsPage_LibraryAndInventoryAndOptionSetsAreLiveLinks(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Count(body, `href="/catalog"`) != 2 { // Library + Option sets both point here today
		t.Errorf("expected two links to /catalog (Library, Option sets), got body: %s", body)
	}
	if !strings.Contains(body, `href="/inventory"`) {
		t.Errorf("expected a link to /inventory, got body: %s", body)
	}
}

func TestItemsPage_CategoriesAndModifiersAreDisabledNotDeadLinks(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Count(body, `aria-disabled="true"`) != 2 {
		t.Errorf("expected exactly 2 disabled sections (Categories, Modifiers), got body: %s", body)
	}
	if !strings.Contains(body, "Coming soon") {
		t.Errorf("expected a coming-soon marker on the disabled sections, got body: %s", body)
	}
}

func TestMenuPage_TopLevelTileIsItemsNotCatalogOrInventory(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, baseMenu)
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	// Scoped to the ☰ Menu launcher's own tile grid (class="menu-tile"),
	// not the whole page: nav.html's shared side rail keeps its own,
	// separate, deliberate one-tap "/inventory" shortcut on every page
	// (ut-docs#1332/#1349) — untouched by this card, out of scope.
	gridStart := strings.Index(body, `class="menu-grid"`)
	if gridStart < 0 {
		t.Fatalf("menu-grid not found in body: %s", body)
	}
	grid := body[gridStart:]

	if !strings.Contains(grid, `class="menu-tile" href="/items"`) {
		t.Errorf("expected the Items tile in the menu grid, got: %s", grid)
	}
	if strings.Contains(grid, `href="/catalog"`) {
		t.Errorf("did not expect a standalone Catalog tile in the menu grid, got: %s", grid)
	}
	if strings.Contains(grid, `href="/inventory"`) {
		t.Errorf("did not expect a standalone Inventory tile in the menu grid, got: %s", grid)
	}
}
