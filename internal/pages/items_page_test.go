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

	// Library reuses "nav.catalog" (= "Catalog") rather than a separate
	// "Library" name, so /catalog's own <h1> and this section's name match
	// (independent-review fix, ut-docs#1897).
	for _, want := range []string{"Catalog", "Categories", "Inventory", "Modifiers", "Option sets"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected section name %q in body", want)
		}
	}
	for _, want := range []string{
		"Your products and services",
		"Organise your items",
		"Track what&#39;s in stock", // html/template escapes the apostrophe
		"Add extras to items at checkout",
		// ut-docs#1900: was "Open an item to edit its variations" while the
		// generator had no screen of its own; now it describes the feature.
		"Generate item variants from reusable lists",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected subtitle %q in body", want)
		}
	}
}

func TestItemsPage_LibraryAndInventoryAndOptionSetsAndModifiersAreLiveLinks(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Count(body, `href="/catalog"`) != 1 { // Library only
		t.Errorf("expected exactly one link to /catalog (Library), got body: %s", body)
	}
	// ut-docs#1900: wired up in the same merge that landed
	// /catalog/option-sets — this row pointed at /catalog only because the
	// dedicated screen didn't exist yet (same story as Modifiers below).
	if !strings.Contains(body, `href="/catalog/option-sets"`) {
		t.Errorf("expected a link to /catalog/option-sets, got body: %s", body)
	}
	if !strings.Contains(body, `href="/inventory"`) {
		t.Errorf("expected a link to /inventory, got body: %s", body)
	}
	// ut-docs#1898: /categories shipped, so its section must be a real link
	// here. This section list is the only navigation to that page (it gets
	// no top-level nav tile of its own), so a regression back to a disabled
	// "coming soon" row makes the whole screen reachable by typed URL only.
	if !strings.Contains(body, `href="/categories"`) {
		t.Errorf("expected a link to /categories, got body: %s", body)
	}
	// ut-docs#1899: wired up in the same merge that landed /modifiers —
	// this row was disabled only because the screen didn't exist yet.
	if !strings.Contains(body, `href="/modifiers"`) {
		t.Errorf("expected a link to /modifiers, got body: %s", body)
	}
}

// TestItemsPage_NoSectionIsDisabled supersedes the two now-stale tests this
// replaced (TestItemsPage_ModifiersIsDisabledNotADeadLink and
// TestItemsPage_OnlyCategoriesIsDisabledNotDeadLink) — each was pinned to a
// transient state where exactly one section still lacked its own screen.
// ut-docs#1898 (categories) and ut-docs#1899 (modifiers) merged the same
// day and both wired up their Href, so all five sections are live links
// now; the disabled/"coming soon" rendering path itself stays in
// itemsSection/registerItemsPage for whenever the next section lands
// ahead of its own screen, it's just untested by name until that happens.
func TestItemsPage_NoSectionIsDisabled(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Count(body, `aria-disabled="true"`) != 0 {
		t.Errorf("expected no disabled sections, got body: %s", body)
	}
	if strings.Contains(body, "Coming soon") {
		t.Errorf("expected no coming-soon marker, got body: %s", body)
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
