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

// newTwoCategoryStripHTTP seeds Food (Bread) and Drinks (Cola), each item
// with its own quick button unless noColaButton, so $hasTabs is true and a
// tab bar actually renders.
func newTwoCategoryStripHTTP(t *testing.T, noColaButton bool) *ButtonsHTTP {
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
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 'cat_food', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	btns := []Button{{Label: "Bread", Code: "C1", ItemID: "i1"}}
	if !noColaButton {
		btns = append(btns, Button{Label: "Cola", Code: "C2", ItemID: "i2"})
	}
	for _, b := range btns {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}
	return &ButtonsHTTP{Store: *store, View: renderer, BrowsingMode: browsingModeStripOverflow}
}

// TestButtonsHTTPList_StripFirstCategoryTabIsDefaultSelected (ut-docs#2613,
// replacing ut-docs#2212/#2294's All-tab tests): the strip no longer renders
// a leading All tab or its dedicated #buttons-grid-all grid — the first
// category tab is first in the tablist and selected by default, its panel
// is the only uncloaked one, and no '__all__' sentinel survives anywhere in
// the Alpine wiring.
func TestButtonsHTTPList_StripFirstCategoryTabIsDefaultSelected(t *testing.T) {
	h := newTwoCategoryStripHTTP(t, false)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, unwanted := range []string{`id="cat-tab-all"`, `id="buttons-grid-all"`, `'__all__'`, `showAllGrid`, "products.all<"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("strip must not render %q any more (ut-docs#2613), got: %s", unwanted, body)
		}
	}
	if !strings.Contains(body, `tab: 'cat_food',`) {
		t.Fatalf("expected the first category's own ID to seed the initial active tab, got: %s", body)
	}
	foodIdx := strings.Index(body, `id="cat-tab-cat_food"`)
	drinkIdx := strings.Index(body, `id="cat-tab-cat_drink"`)
	if foodIdx < 0 || drinkIdx < 0 || foodIdx > drinkIdx {
		t.Fatalf("expected Food then Drinks tabs, got: %s", body)
	}
	// Food's panel is the default one (no x-cloak); Drinks' stays cloaked
	// until Alpine picks a tab.
	for marker, wantCloak := range map[string]bool{`id="cat-panel-cat_food"`: false, `id="cat-panel-cat_drink"`: true} {
		idx := strings.Index(body, marker)
		if idx < 0 {
			t.Fatalf("expected to find panel %s, got: %s", marker, body)
		}
		tag := body[idx : idx+strings.Index(body[idx:], ">")]
		if strings.Contains(tag, "x-cloak") != wantCloak {
			t.Fatalf("panel %s x-cloak = %v, want %v; tag: %s", marker, !wantCloak, wantCloak, tag)
		}
		if !strings.Contains(tag, `:role="q ? 'group' : 'tabpanel'"`) {
			t.Fatalf("panel %s role must key off the query alone now, tag: %s", marker, tag)
		}
	}
	if !strings.Contains(body, `panelVisible(id, panelEl) {
        return this.q ? this.sectionHasMatch(panelEl) : this.tab === id;
      },`) {
		t.Fatalf("expected panelVisible's no-query branch to check ONLY the panel's own tab, got: %s", body)
	}
}

// TestButtonsHTTPList_StripShowsItemWithNoQuickButtonInItsCategory
// (ut-docs#2613): with the strip's All grid gone, an active item with no
// shortcut_buttons row of its own is still on the strip — as an implicit
// tile (ut-docs#2541) in its own category's panel — so retiring All loses
// no item from the sell screen.
func TestButtonsHTTPList_StripShowsItemWithNoQuickButtonInItsCategory(t *testing.T) {
	h := newTwoCategoryStripHTTP(t, true)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	drinkIdx := strings.Index(body, `id="cat-panel-cat_drink"`)
	if drinkIdx < 0 {
		t.Fatalf("expected a Drinks category panel, got: %s", body)
	}
	if !strings.Contains(body[drinkIdx:], `data-name="Cola"`) {
		t.Fatalf("expected Cola (no explicit quick button) as an implicit tile in the Drinks panel, got: %s", body[drinkIdx:])
	}
	if strings.Contains(body, `id="buttons-grid-all"`) {
		t.Fatalf("strip must not render an All grid (ut-docs#2613), got: %s", body)
	}
}

// TestButtonsHTTPList_AllGridExcludesInactiveItems (ut-docs#2294, ut-docs#2281
// context — "the All grid must filter is_active = 1 from day one"): a
// deactivated item must never show as an All-grid tile. Since ut-docs#2613
// the only All grid is all_filter_chips' own.
func TestButtonsHTTPList_AllGridExcludesInactiveItems(t *testing.T) {
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
	h := &ButtonsHTTP{Store: *store, View: renderer, BrowsingMode: browsingModeAllFilterChips}

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
	if !strings.Contains(body, `id="buttons-grid-all"`) || !strings.Contains(body, "Bread") {
		t.Fatalf("expected the active item to still render in the All grid, got: %s", body)
	}
}

// TestButtonsHTTPList_StripWithNoCategoriesRendersFlatImplicitTiles
// (ut-docs#2613, replacing ut-docs#2294's "All tab renders even with no
// quick-button categories"): a shop with no categories and no quick
// buttons configured still sees its catalog on the strip — as the flat,
// tab-less grid of implicit tiles (ut-docs#2541) — not an All tab and not
// the empty state.
func TestButtonsHTTPList_StripWithNoCategoriesRendersFlatImplicitTiles(t *testing.T) {
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
		t.Fatalf("expected the empty state NOT to fire (there IS an active item), got: %s", body)
	}
	for _, unwanted := range []string{`id="cat-tab-all"`, `id="buttons-grid-all"`, `class="tab-bar"`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("flat strip must not render %q, got: %s", unwanted, body)
		}
	}
	if !strings.Contains(body, `data-name="Loose Sweet"`) {
		t.Fatalf("expected the item as a flat implicit tile, got: %s", body)
	}
}

// TestButtonsHTTPList_EmptyStateFiresWithNoActiveItems (ut-docs#2294
// regression, rewritten for ut-docs#2541 and ut-docs#2613): a strip till
// with ZERO active catalog items must fall back to the empty state (the
// "add some in Quick Buttons" CTA), never a silently blank screen.
//
// ut-docs#2541 renamed and rewrote this test: it used to seed one ACTIVE
// item with no shortcut_buttons row and assert the empty state fired
// anyway ("no quick buttons configured" was the bar) — but every active
// item is a quick button by default now (ButtonStore.Load's implicit
// merge), so that item would render as an uncategorized tile and the empty
// state would correctly NOT fire. The empty state's real trigger is now
// "zero active items at all", which is what this rewrite seeds.
func TestButtonsHTTPList_EmptyStateFiresWithNoActiveItems(t *testing.T) {
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
		t.Fatalf("expected the empty-state CTA to fire (zero active items), got: %s", body)
	}
}

// TestButtonsHTTPList_ActiveItemWithNoButtonRendersAsImplicitTile
// (ut-docs#2541): the card's whole point at the ButtonsHTTP.List level — an
// active item with NO shortcut_buttons row of its own is not invisible
// (the pre-#2541 behavior TestButtonsHTTPList_EmptyStateStillFires...
// above used to pin) and is not merely "still counted for the category to
// survive pruning" (ut-docs#2498's own item-count path) — it renders as a
// real tile, even with no quick buttons configured at all.
func TestButtonsHTTPList_ActiveItemWithNoButtonRendersAsImplicitTile(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")

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
		// ut-docs#2613: all_filter_chips is the one mode that still renders
		// the All grid, so that's the render whose cost must not scale.
		h := &ButtonsHTTP{Store: *store, View: renderer, BrowsingMode: browsingModeAllFilterChips}

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
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, parent_id TEXT, sort_order INTEGER NOT NULL DEFAULT 0, color TEXT, is_active INTEGER NOT NULL DEFAULT 1, image_path TEXT, icon TEXT, sell_screen_hidden INTEGER NOT NULL DEFAULT 0);`,
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
