package ui

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// TestButtonsHTTPList_AllTabIsFirstAndDefaultSelected (ut-docs#2212,
// reworked by ut-docs#2294): the tabbed view ($hasTabs) must render a
// leading "All" tab, selected by default, so an operator who has tapped
// into a category always has an escape hatch back to the whole catalogue
// without hunting for which category everything happens to live under.
// ut-docs#2294 changed WHAT selecting it shows: All used to reuse the
// existing per-category panels; it now has its own dedicated grid (every
// active catalog item, not just quick-button ones) — see the wiring
// assertions below, and buttons_all_tab_test.go's other new tests for the
// "every active item, including one with no quick button" behavior itself.
func TestButtonsHTTPList_AllTabIsFirstAndDefaultSelected(t *testing.T) {
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

	// Two top-level categories, same shape as
	// TestButtonsHTTPList_TabbedPanelsCarryCrossCategorySearchWiring, so
	// $hasTabs is true and a tab bar actually renders.
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

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The default `tab` value is the All sentinel, not the first real
	// category's ID — this is what actually makes "All" the pre-selected
	// tab on first paint.
	if !strings.Contains(body, `tab: '__all__',`) {
		t.Fatalf("expected the Alpine component's default tab to be the All sentinel, got: %s", body)
	}

	// The All tab itself: a real, focusable, ARIA-correct tab, appearing
	// BEFORE either category tab (first in DOM order).
	allIdx := strings.Index(body, `id="cat-tab-all"`)
	foodIdx := strings.Index(body, `id="cat-tab-cat_food"`)
	drinkIdx := strings.Index(body, `id="cat-tab-cat_drink"`)
	if allIdx < 0 {
		t.Fatalf("expected an All tab button (id=cat-tab-all), got: %s", body)
	}
	if allIdx > foodIdx || allIdx > drinkIdx {
		t.Fatalf("expected the All tab to render before every category tab, got: %s", body)
	}
	if !strings.Contains(body, `:aria-selected="tab === '__all__'"`) {
		t.Fatalf("expected the All tab's aria-selected to be keyed off the All sentinel, got: %s", body)
	}
	// The All tab owns no single panel of the OLD reused-panel kind (it
	// has its own dedicated grid instead — see #buttons-grid-all below),
	// so it must not claim one via aria-controls — same "drop the
	// attribute rather than assert a false relationship" precedent this
	// file already applies during search.
	allTagEnd := strings.Index(body[allIdx:], ">")
	if allTagEnd < 0 {
		t.Fatalf("could not find the end of the All tab's opening tag, got: %s", body)
	}
	allTag := body[allIdx : allIdx+allTagEnd]
	if strings.Contains(allTag, "aria-controls") {
		t.Fatalf("expected the All tab to carry no aria-controls, got tag: %s", allTag)
	}

	// ut-docs#2294: the All tab's OWN dedicated grid — not a reuse of the
	// category panels — carries both items, visible by default (no
	// x-cloak, since All is the default tab) and gated on showAllGrid().
	allGridIdx := strings.Index(body, `id="buttons-grid-all"`)
	if allGridIdx < 0 {
		t.Fatalf("expected a dedicated All grid (#buttons-grid-all), got: %s", body)
	}
	allGridTagEnd := strings.Index(body[allGridIdx:], ">")
	allGridTag := body[allGridIdx : allGridIdx+allGridTagEnd]
	if !strings.Contains(allGridTag, `x-show="showAllGrid()"`) {
		t.Fatalf("expected the All grid to be gated on showAllGrid(), got tag: %s", allGridTag)
	}
	if strings.Contains(allGridTag, "x-cloak") {
		t.Fatalf("expected the All grid to carry no x-cloak (it's the default tab's content), got tag: %s", allGridTag)
	}
	// Scope to the All grid's own content only: from its opening tag up to
	// the first category panel that follows it (id="cat-panel-..." is an
	// unambiguous boundary — it never appears inside the All grid itself).
	allGridEnd := strings.Index(body[allGridIdx:], `id="cat-panel-`)
	if allGridEnd < 0 {
		t.Fatalf("expected at least one category panel after the All grid, got: %s", body)
	}
	allGridSection := body[allGridIdx : allGridIdx+allGridEnd]
	if !strings.Contains(allGridSection, "Bread") || !strings.Contains(allGridSection, "Cola") {
		t.Fatalf("expected both items in the All grid, got: %s", allGridSection)
	}

	// Each category panel must now be x-cloak'd (they are NOT the default
	// tab any more — All is, and it owns its own grid, not these) and
	// must render ONLY when its own tab is picked, never under All
	// (panelVisible no longer OR's in the All sentinel — see
	// panelVisible's own comment in buttons.html for why: showing a
	// category panel under All as well as the All grid would duplicate
	// every quick-button tile on screen at once).
	for _, marker := range []string{`id="cat-panel-cat_food"`, `id="cat-panel-cat_drink"`} {
		idx := strings.Index(body, marker)
		if idx < 0 {
			t.Fatalf("expected to find panel %s, got: %s", marker, body)
		}
		tagEnd := strings.Index(body[idx:], ">")
		tag := body[idx : idx+tagEnd]
		if !strings.Contains(tag, "x-cloak") {
			t.Fatalf("expected panel %s to carry x-cloak (All, not this panel, is the default), got tag: %s", marker, tag)
		}
	}
	if !strings.Contains(body, `panelVisible(id, panelEl) {
        return this.q ? this.sectionHasMatch(panelEl) : this.tab === id;
      },`) {
		t.Fatalf("expected panelVisible's no-query branch to check ONLY the panel's own tab, not the All sentinel, got: %s", body)
	}

	// i18n: the All tab's own label goes through T, not a hardcoded
	// literal (no InitI18n call in this package's tests, so T falls back
	// to the raw key — guard-i18n.sh is what enforces the translation
	// itself exists in every locale file).
	if !strings.Contains(body, "products.all<") && !strings.Contains(body, ">products.all<") {
		t.Fatalf("expected the products.all key to render as the All tab's label, got: %s", body)
	}
}

// TestButtonsHTTPList_AllTabShowsItemWithNoQuickButton (ut-docs#2294, the
// core of the card): the whole point of the All tab is that it lists EVERY
// active catalog item, not only ones with a shortcut_buttons row. Seeds one
// item WITH a quick button and one WITHOUT, and pins that both show in the
// All grid while only the quick-button one shows in its category's own
// (quick-button-only) panel — proving All's data source genuinely changed,
// not just its default-tab wiring.
func TestButtonsHTTPList_AllTabShowsItemWithNoQuickButton(t *testing.T) {
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
	// Only Bread ever gets a quick button. Cola never does — it's the
	// item the pre-ut-docs#2294 All tab could never show.
	if err := store.Add(Button{Label: "Bread", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	allGridIdx := strings.Index(body, `id="buttons-grid-all"`)
	if allGridIdx < 0 {
		t.Fatalf("expected a dedicated All grid (#buttons-grid-all), got: %s", body)
	}
	// Scope to the All grid's own content only — see
	// TestButtonsHTTPList_AllTabIsFirstAndDefaultSelected's identical
	// scoping for why "id=\"cat-panel-\"" is the right boundary.
	allGridEnd := strings.Index(body[allGridIdx:], `id="cat-panel-`)
	if allGridEnd < 0 {
		t.Fatalf("expected at least one category panel after the All grid, got: %s", body)
	}
	allGrid := body[allGridIdx : allGridIdx+allGridEnd]
	if !strings.Contains(allGrid, "Bread") {
		t.Fatalf("expected the quick-button item to be in the All grid too, got: %s", allGrid)
	}
	if !strings.Contains(allGrid, "Cola") {
		t.Fatalf("expected the NO-quick-button item to be in the All grid — this is the card's whole point, got: %s", allGrid)
	}

	// Drinks (Cola's category) DOES now get its own category tab/panel
	// (ut-docs#2498): BuildCategoryGroups no longer prunes a branch purely
	// for having no quick buttons — it also survives via an active-item
	// count, and Cola is an active item in Drinks with no quick button.
	// This test predates #2498 and used to assert the opposite (the bug
	// the card fixed: a category with real items but no quick buttons was
	// invisible everywhere, not just absent from All). The panel now
	// renders, but empty of quick-button tiles — see
	// TestButtonsHTTPList_CategoryWithActiveItemButNoButtonStillAppears
	// in buttons_category_item_only_test.go for the dedicated empty-state
	// coverage.
	if !strings.Contains(body, `id="cat-panel-cat_drink"`) {
		t.Fatalf("expected a Drinks category panel (has an active item, Cola) even with no quick button in it, got: %s", body)
	}
}

// TestButtonsHTTPList_AllTabExcludesInactiveItems (ut-docs#2294, ut-docs#2281
// context — "the All grid must filter is_active = 1 from day one"): a
// deactivated item must never show as an All-tab tile.
func TestButtonsHTTPList_AllTabExcludesInactiveItems(t *testing.T) {
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
		('i2', 'S2', 'Discontinued Soda', 120, 'cat_drink', 0)`)
	if err := store.Add(Button{Label: "Bread", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, "Discontinued Soda") {
		t.Fatalf("expected a deactivated item to stay off the All grid, got: %s", body)
	}
	if !strings.Contains(body, "Bread") {
		t.Fatalf("expected the active item to still render, got: %s", body)
	}
}

// TestButtonsHTTPList_AllTabRendersEvenWithNoQuickButtonCategories
// (ut-docs#2294): the All tab must work for a shop with zero (or one)
// quick-button categories configured — its whole value is that it does NOT
// depend on the Designer having been used at all. Before this card, a till
// with no quick buttons hit buttons.html's own empty state
// ({{ if not .Groups }}) and showed NOTHING, All tab included.
func TestButtonsHTTPList_AllTabRendersEvenWithNoQuickButtonCategories(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")

	// No categories, no shortcut_buttons rows at all — just a catalog.
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','S1','Loose Sweet', 10, 1)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, "products.empty<") {
		t.Fatalf("expected the empty state NOT to fire (there IS an active item, just no quick buttons), got: %s", body)
	}
	if !strings.Contains(body, `id="cat-tab-all"`) {
		t.Fatalf("expected the All tab to render even with zero quick-button categories, got: %s", body)
	}
	if !strings.Contains(body, "Loose Sweet") {
		t.Fatalf("expected the item in the All grid, got: %s", body)
	}
}

// TestButtonsHTTPList_EmptyStateStillFiresWithSettingOffAndNoActiveItems
// (ut-docs#2294 regression, rewritten for ut-docs#2541): a till with ZERO
// active catalog items and the All-tab setting OFF must still fall back to
// the pre-#2212 empty state (the "add some in Quick Buttons" CTA). An
// earlier draft of this card gated the empty state on raw AllButtons
// non-emptiness rather than on $showAllTab (which also factors in the
// setting), which would have rendered a silently blank screen with nothing
// sellable and no CTA whenever an operator turned the setting off on a
// till that had never set up any quick buttons.
//
// ut-docs#2541 renamed and rewrote this test: it used to seed one ACTIVE
// item with no shortcut_buttons row and assert the empty state fired
// anyway ("no quick buttons configured" was the bar) — but every active
// item is a quick button by default now (ButtonStore.Load's implicit
// merge), so that item would render as an uncategorized tile and the empty
// state would correctly NOT fire. The empty state's real trigger is now
// "zero active items at all", which is what this rewrite seeds.
func TestButtonsHTTPList_EmptyStateStillFiresWithSettingOffAndNoActiveItems(t *testing.T) {
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
	h := &ButtonsHTTP{Store: *store, View: renderer, HideAllTab: true}

	// No items at all (active or otherwise) — Groups ends up empty
	// (BuildCategoryGroups has nothing to bucket, and Load's implicit merge
	// has nothing to add either).
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, "products.empty<") {
		t.Fatalf("expected the empty-state CTA to fire (zero active items, All off), got: %s", body)
	}
}

// TestButtonsHTTPList_ActiveItemWithNoButtonRendersAsImplicitTile
// (ut-docs#2541): the card's whole point at the ButtonsHTTP.List level — an
// active item with NO shortcut_buttons row of its own is not invisible
// (the pre-#2541 behavior TestButtonsHTTPList_EmptyStateStillFires...
// above used to pin) and is not merely "still counted for the category to
// survive pruning" (ut-docs#2498's own item-count path) — it renders as a
// real tile, even with the All tab off and even with no quick buttons
// configured at all.
func TestButtonsHTTPList_ActiveItemWithNoButtonRendersAsImplicitTile(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")
	h.HideAllTab = true

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Loose Sweet', 10, 1)`)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, "products.empty<") {
		t.Fatalf("expected the empty state NOT to fire (there is an active item, now an implicit quick button), got: %s", body)
	}
	if !strings.Contains(body, "Loose Sweet") {
		t.Fatalf("expected the button-less active item to render as an implicit tile, got: %s", body)
	}
}

// TestButtonsHTTPList_AllTabHiddenWhenSettingOff (ut-docs#2294): with
// HideAllTab set (settings.sale.show_all_tab off), no All tab renders at
// all, and the first CATEGORY tab (not the All sentinel) is default-
// selected — the exact pre-ut-docs#2212 behavior the card asks for.
func TestButtonsHTTPList_AllTabHiddenWhenSettingOff(t *testing.T) {
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
	h := &ButtonsHTTP{Store: *store, View: renderer, HideAllTab: true}

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

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, `id="cat-tab-all"`) {
		t.Fatalf("expected no All tab when the setting is off, got: %s", body)
	}
	if strings.Contains(body, "products.all<") || strings.Contains(body, ">products.all<") {
		t.Fatalf("expected the products.all label to be entirely absent, got: %s", body)
	}
	// First real category (Food) is now the default-selected tab — the
	// pre-#2212 default this setting restores.
	if !strings.Contains(body, `tab: 'cat_food',`) {
		t.Fatalf("expected the first category's own ID to seed the initial active tab, got: %s", body)
	}
	// No dedicated All grid either.
	if strings.Contains(body, `id="buttons-grid-all"`) {
		t.Fatalf("expected no All grid to render when the setting is off, got: %s", body)
	}
}

// TestButtonsHTTPSearch_FindsItemWithNoQuickButton (ut-docs#2294): the
// sell-screen's live search must find an item that has NO quick button —
// proving it genuinely queries the catalog server-side (ButtonStore.
// SearchSellable / POSRepo.SearchItemsForShortcuts), not just filtering
// whatever tiles happened to already be rendered client-side.
func TestButtonsHTTPSearch_FindsItemWithNoQuickButton(t *testing.T) {
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

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES
		('i1', 'S1', 'Sourdough Loaf', 320, 1)`)

	rec := httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest("GET", "/ui/buttons/search?q=Sourdough", nil))
	if rec.Code != 200 {
		t.Fatalf("Search = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sourdough Loaf") {
		t.Fatalf("expected the no-quick-button item to be findable via search, got: %s", body)
	}
	if !strings.Contains(body, `£3.20`) {
		t.Fatalf("expected the search result tile to carry a price, got: %s", body)
	}

	// An empty query renders no results and no stray "no matches" message
	// (the operator hasn't typed anything yet).
	rec2 := httptest.NewRecorder()
	h.Search(rec2, httptest.NewRequest("GET", "/ui/buttons/search?q=", nil))
	if rec2.Code != 200 {
		t.Fatalf("Search(empty) = %d: %s", rec2.Code, rec2.Body.String())
	}
	if strings.TrimSpace(rec2.Body.String()) != "" {
		t.Fatalf("expected an empty query to render nothing, got: %s", rec2.Body.String())
	}

	// A miss renders the shared no-matches message, not a blank response.
	rec3 := httptest.NewRecorder()
	h.Search(rec3, httptest.NewRequest("GET", "/ui/buttons/search?q=doesnotexist", nil))
	if rec3.Code != 200 {
		t.Fatalf("Search(miss) = %d: %s", rec3.Code, rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), "products.no_matches") {
		t.Fatalf("expected the no-matches message on a miss, got: %s", rec3.Body.String())
	}
}

// TestButtonsHTTPList_AllTabDoesNotScaleWithCatalogSize (ut-docs#2294,
// renamed + strengthened in review, SF-3): the All grid's SELECT count for
// one /ui/buttons render must not scale with the size of the active
// catalog — CatalogRepo.ListItems/ItemBarcodes/ItemThumbnails/
// ItemIDsWithVariants/ItemCurrentPrices/ItemIDsWithModifiers are each ONE
// batched query regardless of row count, same "prove it costs the same
// regardless of shape/size" technique as resolver_querycount_test.go's
// TestResolve_DoesNotScaleWithShape (see that file's own doc comment for
// the counting-connector mechanics reused here via
// openCountingConn/seedQCAllTabFixture, both file-local to this package).
//
// That scaling assertion alone proves the grid has no N+1 over items, but
// says nothing about WHEN it re-renders — a regression that added
// "basket-changed" to the .products hx-trigger, or had /api/pos/scan
// OOB-swap #buttons-grid, would sail straight past it while this test kept
// passing. So this test also pins the actual mechanism the card's
// requirement rests on ("must not re-query the whole catalog on every
// basket change, render once per sell-screen load"): buttons.html's
// #buttons-grid-all(...) at the .products root's own hx-trigger, verified
// against the rendered body, is the ONLY thing that ever re-fetches
// /ui/buttons, and it fires on modifiers-changed, never on a basket/scan
// event.
func TestButtonsHTTPList_AllTabDoesNotScaleWithCatalogSize(t *testing.T) {
	countFor := func(t *testing.T, n int) (int64, string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "all_tab_querycount.db")
		counter := new(int64)
		countingDB := openCountingConn(t, path, counter)
		seedQCAllTabFixture(t, countingDB, n)

		store := NewButtonStore(countingDB)
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

		atomic.StoreInt64(counter, 0)
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
		if rec.Code != 200 {
			t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
		}
		return atomic.LoadInt64(counter), rec.Body.String()
	}

	small, smallBody := countFor(t, 3)
	large, _ := countFor(t, 30)
	if small < 1 {
		t.Fatalf("harness counted %d SELECTs -- it stopped counting (assertion would be vacuous)", small)
	}
	if small != large {
		t.Fatalf("List() SELECT count scales with catalog size: 3 items -> %d SELECTs, 30 items -> %d SELECTs (want equal -- the All grid must cost the same regardless of catalog size, i.e. never re-query per item/basket state)", small, large)
	}

	// Pin the mechanism: /ui/buttons re-fetches on modifiers-changed (and,
	// since ut-docs#2285, buttons-changed -- a tile move/remove/edit, also
	// unrelated to basket state) -- never on a basket/scan/sale event.
	const wantTrigger = `hx-get="/ui/buttons" hx-trigger="modifiers-changed from:body, buttons-changed from:body"`
	if !strings.Contains(smallBody, wantTrigger) {
		t.Fatalf("expected the .products root's own re-fetch trigger %q in the rendered body -- got a different mechanism, which this test can no longer see is safe", wantTrigger)
	}
	for _, basketEvent := range []string{"basket-changed", "sale-changed", "scan-changed", "from:#basket"} {
		if strings.Contains(smallBody, basketEvent) {
			t.Fatalf("rendered body wires %q into a trigger -- the All grid must never re-query on a basket mutation, only on modifiers-changed", basketEvent)
		}
	}
}

// seedQCAllTabFixture seeds n active items (every third one also gets a
// quick button, so both LoadAllActive's own batched lookups and Load's
// shortcut-driven ones have real rows to work with) plus the schema
// ButtonStore.LoadAllActive/Load need — same table set as this package's
// own seedQCResolverFixture (resolver_querycount_test.go), copied rather
// than shared since that helper seeds a fixed, unrelated fixture.
func seedQCAllTabFixture(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT, name TEXT, description TEXT, base_price INTEGER NOT NULL, tax_code_id TEXT, category_id TEXT, brand_id TEXT, unit TEXT NOT NULL DEFAULT 'each', color TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0, is_sample_data INTEGER NOT NULL DEFAULT 0, stock_untracked INTEGER NOT NULL DEFAULT 0, sell_screen_hidden INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, parent_id TEXT, sort_order INTEGER NOT NULL DEFAULT 0, color TEXT, is_active INTEGER NOT NULL DEFAULT 1, image_path TEXT);`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, role TEXT NOT NULL, path TEXT NOT NULL);`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, is_primary INTEGER DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, is_primary INTEGER DEFAULT 0);`,
		`CREATE TABLE shortcut_buttons (barcode TEXT PRIMARY KEY, label TEXT, item_id TEXT, image_path TEXT, sort_order INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, rate_basis_points INTEGER NOT NULL, takeaway_rate_basis_points INTEGER);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, sku TEXT UNIQUE, name TEXT, price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_modifier_groups (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, name TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_modifier_group_links (item_id TEXT NOT NULL, group_id TEXT NOT NULL, sort_order INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (item_id, group_id));`,
		`CREATE TABLE category_modifier_group_links (category_id TEXT NOT NULL, group_id TEXT NOT NULL, sort_order INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (category_id, group_id));`,
		`CREATE TABLE item_modifier_group_opt_outs (item_id TEXT NOT NULL, group_id TEXT NOT NULL, PRIMARY KEY (item_id, group_id));`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	ctx := context.Background()
	for i := 0; i < n; i++ {
		id := "itm" + strconv.Itoa(i)
		if _, err := db.ExecContext(ctx, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES (?, ?, ?, ?, 1)`,
			id, "SKU"+strconv.Itoa(i), "Item "+strconv.Itoa(i), 100+i); err != nil {
			t.Fatalf("insert item %d: %v", i, err)
		}
		if i%3 == 0 {
			if _, err := db.ExecContext(ctx, `INSERT INTO shortcut_buttons(barcode, label, item_id, sort_order) VALUES (?, ?, ?, ?)`,
				"BC"+strconv.Itoa(i), "Item "+strconv.Itoa(i), id, i); err != nil {
				t.Fatalf("insert shortcut for item %d: %v", i, err)
			}
		}
	}
}
