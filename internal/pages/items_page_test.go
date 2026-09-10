package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/itemsnav"
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

// ut-docs#1950 AC #1/#4: the rail must be a narrow column of compact,
// single-line rows (.items-row), not the old large card tiles (.card).
func TestItemsPage_RailRowsAreCompactNotCardTiles(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerItemsPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="items-rail"`) {
		t.Fatalf("expected the compact rail, got: %s", body)
	}
	if strings.Count(body, `class="items-row`) < 5 {
		t.Errorf("expected 5 compact .items-row rows, got body: %s", body)
	}
	// The old layout's large tiles must be gone from the rail.
	railStart := strings.Index(body, `id="items-rail"`)
	railEnd := strings.Index(body[railStart:], "</nav>") + railStart
	rail := body[railStart:railEnd]
	if strings.Contains(rail, `class="card"`) {
		t.Errorf("rail still renders large card tiles, got: %s", rail)
	}
}

// ut-docs#1950 AC #3: a bare /items load must not leave the right panel
// empty — the first section (Library/Catalog) renders inline.
func TestItemsPage_DefaultLoadEmbedsFirstSectionInPanel(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	panelStart := strings.Index(body, `id="items-panel"`)
	if panelStart < 0 {
		t.Fatalf("expected #items-panel, got: %s", body)
	}
	panel := body[panelStart:]
	// The catalog page's own table markup (catalog_table.html) must be
	// present inline — not just an empty placeholder waiting on JS.
	if !strings.Contains(panel, "catalog") && !strings.Contains(panel, "Catalog") {
		t.Errorf("expected /catalog's own content embedded in the panel, got: %s", panel)
	}
	// The catalog fragment must not drag its own full-page chrome in with
	// it (that would double up nav/status bar inside /items' own page).
	if strings.Count(body, `class="nav"`) != 1 {
		t.Errorf("expected exactly one nav rail (the /items page's own), got body: %s", body)
	}
	// AC #2: the rail shows which section is selected.
	railStart := strings.Index(body, `id="items-rail"`)
	rail := body[railStart:]
	if !strings.Contains(rail[:strings.Index(rail, "</nav>")], `href="/catalog"`) {
		t.Fatalf("rail missing the /catalog row: %s", rail)
	}
	catalogLinkIdx := strings.Index(rail, `href="/catalog"`)
	tagStart := strings.LastIndex(rail[:catalogLinkIdx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("expected the /catalog rail row to be marked is-current on default load: %s", rail[tagStart:tagEnd])
	}
}

// Review of ut-docs#1950: the embedded default panel must NOT bring the
// section handler's own out-of-band rail copy along with it. /items already
// draws the rail immediately above the panel, so a second one is a
// duplicate DOM id AND paints the whole section list a second time inside
// the right panel — a bare GET /items rendered 2 rails and 10 rows instead
// of 1 and 5. The embed sub-request signals this with itemsnav.EmbedHeader;
// itemsnav.WriteRailOOB skips the OOB copy when it is set.
func TestItemsPage_DefaultLoadRendersExactlyOneRail(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if n := strings.Count(body, `id="items-rail"`); n != 1 {
		t.Errorf(`expected exactly 1 id="items-rail" on a bare /items load, got %d`, n)
	}
	if n := strings.Count(body, `class="items-row`); n != len(itemsnav.Sections) {
		t.Errorf("expected exactly %d .items-row rows, got %d", len(itemsnav.Sections), n)
	}
	// An hx-swap-oob element sitting in the INITIAL page markup is always a
	// mistake: it is meaningful only in a swapped htmx response.
	if strings.Contains(body, `hx-swap-oob`) {
		t.Errorf("bare /items page must not contain an hx-swap-oob element, got: %s", body)
	}
	// ...and specifically not inside the panel.
	panel := body[strings.Index(body, `id="items-panel"`):]
	if strings.Contains(panel, `id="items-rail"`) {
		t.Errorf("the embedded panel must not contain a second copy of the rail: %s", panel)
	}
}

// A REAL htmx panel swap (no embed header) must still get the out-of-band
// rail — the fix above must not disable the mechanism it exists for.
func TestItemsSection_RealHTMXSwapStillGetsOOBRail(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	catalog.Register(mux, dp)
	registerItemsPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /catalog: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="items-rail"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("a real htmx swap must still carry the OOB rail, got: %s", body)
	}
}
