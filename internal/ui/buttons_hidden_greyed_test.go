package ui

import (
	"context"
	"errors"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2698: two sell-screen states, one per quick-button action.
// Hide keeps the tile in the rendered grid (greyed in edit mode, out of
// sight at rest, same spot); the trash badge removes it from the quick
// buttons entirely (absent in edit mode too) without deactivating the item;
// adding it back from search clears both.

// TestButtonStoreLoad_HiddenTilesKeptInPlaceAndMarked: the grid load (Load)
// carries hidden tiles -- explicit rows in their own sort position, implicit
// ones in their name-ordered spot -- marked Hidden, so data-pos stays aligned
// with what UpdateOrder compares against. A removed item is absent.
func TestButtonStoreLoad_HiddenTilesKeptInPlaceAndMarked(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i3','S3','Cola', 150, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i4','S4','Donut', 90, 1)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B3','Cola','i3',0)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B2','Bread','i2',1)`)

	store := NewButtonStore(db)
	ctx := context.Background()
	if err := store.Hide(ctx, "i3"); err != nil { // explicit row, first
		t.Fatalf("Hide i3: %v", err)
	}
	if err := store.Hide(ctx, "i1"); err != nil { // implicit tile
		t.Fatalf("Hide i1: %v", err)
	}
	if err := store.RemoveFromQuickButtons(ctx, "i4"); err != nil {
		t.Fatalf("RemoveFromQuickButtons i4: %v", err)
	}

	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	type row struct {
		id     string
		hidden bool
	}
	var got []row
	for _, b := range btns {
		got = append(got, row{b.ItemID, b.Hidden})
	}
	want := []row{{"i3", true}, {"i2", false}, {"i1", true}}
	if len(got) != len(want) {
		t.Fatalf("Load = %+v, want %+v (removed i4 absent)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Load = %+v, want %+v", got, want)
		}
	}

	// LoadAllActive is the at-rest All grid/category-tile source: hidden and
	// removed are both out.
	all, err := store.LoadAllActive(ctx)
	if err != nil {
		t.Fatalf("LoadAllActive: %v", err)
	}
	if len(all) != 1 || all[0].ItemID != "i2" {
		t.Fatalf("LoadAllActive = %+v, want only Bread", all)
	}
}

// TestButtonStoreRemoveFromQuickButtons_KeepsItemActiveAndSellable: the
// trash badge never deactivates the item -- it still resolves for
// scan/search -- and Add brings it back.
func TestButtonStoreRemoveFromQuickButtons_KeepsItemActiveAndSellable(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','APL-01','Apple Juice', 100, 1)`)
	store := NewButtonStore(db)
	ctx := context.Background()

	if err := store.RemoveFromQuickButtons(ctx, "i1"); err != nil {
		t.Fatalf("RemoveFromQuickButtons: %v", err)
	}
	var active int
	if err := db.QueryRow(`SELECT is_active FROM items WHERE id='i1'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("item must stay active, is_active=%d err=%v", active, err)
	}
	btns, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 0 {
		t.Fatalf("a removed item must be absent from the grid (edit mode too), got %+v", btns)
	}
	res, err := store.SearchSellable(ctx, "Apple", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !res[0].Removed {
		t.Fatalf("search must still find the removed item, marked Removed, got %+v", res)
	}

	if err := store.Add(Button{Label: "Apple Juice", Code: "APL-01", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	btns, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 || btns[0].ItemID != "i1" || btns[0].Hidden {
		t.Fatalf("Add must put it back as a visible quick button, got %+v", btns)
	}

	if err := store.RemoveFromQuickButtons(ctx, " "); err == nil {
		t.Fatal("expected an error for a blank itemId")
	}
	if err := store.RemoveFromQuickButtons(ctx, "never-existed"); !errors.Is(err, data.ErrItemNotFound) {
		t.Fatalf("unknown id: want ErrItemNotFound, got %v", err)
	}
}

// TestButtonStoreAdd_ClearsHiddenAndRemoved: Add clears both flags.
func TestButtonStoreAdd_ClearsHiddenAndRemoved(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, sell_screen_hidden, sell_screen_removed) VALUES('i1','S1','Apple', 100, 1, 1, 1)`)
	store := NewButtonStore(db)
	if err := store.Add(Button{Label: "Apple", Code: "B1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	var hidden, removed int
	if err := db.QueryRow(`SELECT sell_screen_hidden, sell_screen_removed FROM items WHERE id='i1'`).Scan(&hidden, &removed); err != nil {
		t.Fatal(err)
	}
	if hidden != 0 || removed != 0 {
		t.Fatalf("Add must clear both flags, hidden=%d removed=%d", hidden, removed)
	}
}

// TestButtonStoreAdd_HideThenAddWithChangedCodeKeepsOneRow (ut-docs#2698
// review F1): Hide keeps the item's shortcut_buttons row, and search offers
// "Add to quick buttons" on a hidden result. If the item's resolvable tile
// code changed since that row was written (a barcode plugin enabled after
// the row was materialised with the SKU), Add must reuse the existing row
// -- same position, not hidden -- instead of upserting a SECOND row keyed on
// the new code, which would show two live tiles for one item.
func TestButtonStoreAdd_HideThenAddWithChangedCodeKeepsOneRow(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('S1','Apple','i1',0)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('S2','Bread','i2',1)`)
	store := NewButtonStore(db)
	ctx := context.Background()

	if err := store.Hide(ctx, "i1"); err != nil {
		t.Fatalf("Hide: %v", err)
	}
	if err := store.Add(Button{Label: "Apple", Code: "5012345678900", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shortcut_buttons WHERE item_id='i1'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("want exactly one shortcut_buttons row for i1, got %d", rows)
	}
	var code string
	var sort int
	if err := db.QueryRow(`SELECT barcode, sort_order FROM shortcut_buttons WHERE item_id='i1'`).Scan(&code, &sort); err != nil {
		t.Fatal(err)
	}
	if code != "S1" || sort != 0 {
		t.Fatalf("want the kept row (S1, sort 0), got (%s, %d)", code, sort)
	}
	btns, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 2 || btns[0].ItemID != "i1" || btns[0].Hidden || btns[1].ItemID != "i2" {
		t.Fatalf("want [i1 visible, i2], got %+v", btns)
	}
}

// TestButtonStoreUnhideAll_LeavesRemovedAlone: UnhideAll clears hidden
// only; a removed item stays off the grid.
func TestButtonStoreUnhideAll_LeavesRemovedAlone(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, sell_screen_hidden) VALUES('i1','S1','Apple', 100, 1, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, sell_screen_removed) VALUES('i2','S2','Bread', 200, 1, 1)`)
	store := NewButtonStore(db)
	n, err := store.UnhideAll(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("UnhideAll = (%d, %v), want (1, nil)", n, err)
	}
	btns, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(btns) != 1 || btns[0].ItemID != "i1" || btns[0].Hidden {
		t.Fatalf("expected only Apple back and visible, got %+v", btns)
	}
}

// TestBuildCategoryGroups_HiddenOnlyCategoryIsEmptyAtRest: a category whose
// only tiles are hidden survives (edit mode needs its greyed tiles) but
// reports nothing visible, so the template treats it as empty at rest.
func TestBuildCategoryGroups_HiddenOnlyCategoryIsEmptyAtRest(t *testing.T) {
	cats := []data.CategoryNode{{ID: "c1", Name: "Drinks"}, {ID: "c2", Name: "Food"}}
	btns := []Button{
		{Label: "Cola", Code: "B1", ItemID: "i1", CategoryID: "c1", Hidden: true, QuickButton: true},
		{Label: "Bun", Code: "B2", ItemID: "i2", CategoryID: "c2"},
		{Label: "Pie", Code: "B3", ItemID: "i3", CategoryID: "c2", Hidden: true},
	}
	groups := BuildCategoryGroups(btns, cats, map[string]int{"c2": 1})
	byID := map[string]*CategoryGroup{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	drinks, food := byID["c1"], byID["c2"]
	if drinks == nil || food == nil {
		t.Fatalf("both groups must survive (edit mode shows hidden tiles), got %+v", groups)
	}
	if drinks.HasVisible || drinks.HasButtons || drinks.VisibleButtons() != 0 {
		t.Fatalf("hidden-only Drinks: HasVisible=%v HasButtons=%v visible=%d, want false/false/0", drinks.HasVisible, drinks.HasButtons, drinks.VisibleButtons())
	}
	if !food.HasVisible || !food.HasButtons || food.VisibleButtons() != 1 {
		t.Fatalf("Food: HasVisible=%v HasButtons=%v visible=%d, want true/true/1", food.HasVisible, food.HasButtons, food.VisibleButtons())
	}
	if !food.Buttons[1].Hidden {
		t.Fatalf("the hidden tile must stay marked in its group, got %+v", food.Buttons)
	}
	if got := countButtonsPerCategory(groups); got["c1"] != 0 || got["c2"] != 1 {
		t.Fatalf("per-category counts must count visible tiles only, got %+v", got)
	}
}

// tileCell returns the product-tile wrapper (.tile-cell ... up to the next
// cell) that holds data-item-id=itemID, or "".
func tileCell(body, itemID string) string {
	idx := strings.Index(body, `data-item-id="`+itemID+`"`)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(body[:idx], `<div class="tile-cell`)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[idx:], `<div class="tile-cell`)
	if end < 0 {
		return body[start:]
	}
	return body[start : idx+end]
}

// TestButtonsHTTPList_HiddenTileRenderedGreyedWithUnhideBadge: the sale
// screen's render carries the hidden tile (so client-side jiggle mode can
// show it) with the rest-hiding class, data-hidden, and an Unhide badge in
// place of Hide; the removed item is not rendered at all. The Designer
// (EditMode) renders it greyed without the rest-hiding class.
func TestButtonsHTTPList_HiddenTileRenderedGreyedWithUnhideBadge(t *testing.T) {
	for _, edit := range []bool{false, true} {
		h, db := newButtonsHTTPWithDB(t, "buttons.html")
		h.Granted = true
		h.EditMode = edit
		mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
		mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Bread', 200, 1)`)
		mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i3','S3','Cake', 300, 1)`)
		if err := h.Store.Hide(t.Context(), "i1"); err != nil {
			t.Fatal(err)
		}
		if err := h.Store.RemoveFromQuickButtons(t.Context(), "i3"); err != nil {
			t.Fatal(err)
		}

		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
		if rec.Code != 200 {
			t.Fatalf("edit=%v List = %d", edit, rec.Code)
		}
		body := rec.Body.String()
		hidden := tileCell(body, "i1")
		if hidden == "" {
			t.Fatalf("edit=%v: the hidden tile must be in the DOM, got: %s", edit, body)
		}
		if !strings.Contains(hidden, "tile--hidden") || !strings.Contains(hidden, "data-hidden") {
			t.Fatalf("edit=%v: hidden tile must carry .tile--hidden + data-hidden, got: %s", edit, hidden)
		}
		restHidden := strings.Contains(hidden, "tile--rest-hidden")
		if restHidden == edit {
			t.Fatalf("edit=%v: tile--rest-hidden present=%v (sale screen hides it at rest, the Designer never does): %s", edit, restHidden, hidden)
		}
		if !strings.Contains(hidden, `data-testid="tile-badge-unhide"`) || !strings.Contains(hidden, `hx-post="/api/buttons/unhide"`) {
			t.Fatalf("edit=%v: hidden tile needs the Unhide badge posting /api/buttons/unhide, got: %s", edit, hidden)
		}
		if strings.Contains(hidden, `data-testid="tile-badge-hide"`) {
			t.Fatalf("edit=%v: hidden tile must not offer Hide again, got: %s", edit, hidden)
		}
		if !regexp.MustCompile(`aria-label="[^"]*tile_sheet.unhide_named`).MatchString(hidden) {
			t.Fatalf("edit=%v: Unhide badge needs its named aria-label, got: %s", edit, hidden)
		}
		if !strings.Contains(hidden, "tile-hidden-mark") {
			t.Fatalf("edit=%v: greyed tile needs the eye-off glyph (not colour alone), got: %s", edit, hidden)
		}

		visible := tileCell(body, "i2")
		if visible == "" || strings.Contains(visible, "tile--hidden") || !strings.Contains(visible, `data-testid="tile-badge-hide"`) {
			t.Fatalf("edit=%v: the visible tile must render normally with a Hide badge, got: %s", edit, visible)
		}
		if strings.Contains(body, `data-item-id="i3"`) {
			t.Fatalf("edit=%v: a removed item must not be rendered at all, got: %s", edit, body)
		}
		// The trash badge removes from the quick buttons -- never the
		// catalog-deactivating delete-item route.
		if !strings.Contains(visible, `hx-post="/api/buttons/remove-from-grid"`) || strings.Contains(body, "/api/buttons/delete-item") {
			t.Fatalf("edit=%v: trash badge must post /api/buttons/remove-from-grid, got: %s", edit, visible)
		}
	}
}

// TestButtonsHTTPList_HiddenOnlyCategoryTabMarked: a category whose only
// tiles are hidden keeps its tab (edit mode reaches the greyed tiles) but the
// tab carries the hidden-only class CSS uses to keep it out of sight at rest,
// and the default tab is a category with something visible.
func TestButtonsHTTPList_HiddenOnlyCategoryTabMarked(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")
	h.Granted = true
	mustExec(t, db, `INSERT INTO categories(id, name, sort_order) VALUES('c1','Drinks',0)`)
	mustExec(t, db, `INSERT INTO categories(id, name, sort_order) VALUES('c2','Food',1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('i1','S1','Cola', 100, 1, 'c1')`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, category_id) VALUES('i2','S2','Bun', 200, 1, 'c2')`)
	if err := h.Store.Hide(t.Context(), "i1"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	body := rec.Body.String()
	tab := regexp.MustCompile(`<button[^>]*id="cat-tab-c1"[^>]*>`).FindString(body)
	if tab == "" || !strings.Contains(tab, "tab-hidden-only") {
		t.Fatalf("Drinks tab must be present and marked tab-hidden-only, got %q", tab)
	}
	food := regexp.MustCompile(`<button[^>]*id="cat-tab-c2"[^>]*>`).FindString(body)
	if food == "" || strings.Contains(food, "tab-hidden-only") {
		t.Fatalf("Food tab must be a normal tab, got %q", food)
	}
	if !strings.Contains(body, `tab: 'c2'`) {
		t.Fatalf("the default tab must be Food (the one with a visible tile), got: %s", body)
	}
}

// TestButtonsHTTPList_EverythingHiddenIsEmptyAtRest: with every tile hidden
// the sale screen shows its empty state at rest (nothing sellable to tap),
// while the Designer still renders the grid with the greyed tiles.
func TestButtonsHTTPList_EverythingHiddenIsEmptyAtRest(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Apple', 100, 1)`)
	if err := h.Store.Hide(t.Context(), "i1"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="products-add-link"`) || strings.Contains(body, `id="buttons-grid"`) {
		t.Fatalf("sale screen with everything hidden must show the empty state, got: %s", body)
	}

	h.Granted, h.EditMode = true, true
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons?mode=edit", nil))
	if !strings.Contains(rec.Body.String(), `data-item-id="i1"`) {
		t.Fatalf("the Designer must still render the greyed tile, got: %s", rec.Body.String())
	}
}

// TestButtonsHTTPSearch_OffersAddForHiddenAndRemoved: a sell-screen search
// result that isn't a visible quick button (hidden or removed) carries an
// "Add to quick buttons" action posting /api/buttons/add (shown by CSS only
// in edit mode); a visible quick button's result doesn't.
func TestButtonsHTTPSearch_OffersAddForHiddenAndRemoved(t *testing.T) {
	h, db := newButtonsHTTPWithDB(t, "buttons.html")
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Tea Green', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Tea Black', 100, 1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i3','S3','Tea Mint', 100, 1)`)
	if err := h.Store.Hide(t.Context(), "i1"); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.RemoveFromQuickButtons(t.Context(), "i2"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest("GET", "/ui/buttons/search?q=Tea", nil))
	body := rec.Body.String()
	adds := regexp.MustCompile(`data-testid="search-add-quick-(i\d)"`).FindAllStringSubmatch(body, -1)
	got := map[string]bool{}
	for _, m := range adds {
		got[m[1]] = true
	}
	if !got["i1"] || !got["i2"] || got["i3"] || len(adds) != 2 {
		t.Fatalf("expected Add actions for i1 (hidden) and i2 (removed) only, got %v in: %s", got, body)
	}
	if !strings.Contains(body, `hx-post="/api/buttons/add"`) {
		t.Fatalf("the Add action must post /api/buttons/add, got: %s", body)
	}
}
