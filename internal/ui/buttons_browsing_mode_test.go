package ui

import (
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2499: sale.browsing_mode — one sell screen, three browsing
// shapes. These tests drive the real rendered HTML through ButtonsHTTP.List
// (and the two fragments the new modes fetch on demand) against one shared
// fixture:
//
//	Food (top-level)      Bread   — quick button
//	  └ Dairy (nested)    Butter  — active, NO quick button
//	Drinks (top-level)    Cola    — quick button
//	Household (top-level) Soap    — active, NO quick button anywhere in its subtree
//	(uncategorized)       Loose   — active, no category, NO quick button
//	Household             Old Mop — INACTIVE
//
// so "every active item" and "just the quick buttons" give visibly
// different answers per category, which is exactly what ut-docs#2372 (the
// popup must list ALL of a category's active items) and the chip/tile
// modes (a category with items but no quick button must still be
// browsable) are about.
func newBrowsingModeTestHTTP(t *testing.T) (*sql.DB, *ButtonStore, *ButtonsHTTP) {
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
		('cat_dairy', 'Dairy', 'cat_food', 1),
		('cat_drink', 'Drinks', NULL, 2),
		('cat_house', 'Household', NULL, 3)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i_bread', 'S1', 'Bread', 140, 'cat_food', 1),
		('i_butter', 'S2', 'Butter', 210, 'cat_dairy', 1),
		('i_cola', 'S3', 'Cola', 120, 'cat_drink', 1),
		('i_soap', 'S4', 'Soap', 99, 'cat_house', 1),
		('i_loose', 'S5', 'Loose Sweet', 10, NULL, 1),
		('i_mop', 'S6', 'Old Mop', 500, 'cat_house', 0)`)
	// Quick buttons in a deliberate Designer order (Cola before Bread) so
	// "quick buttons first, in their Designer order" is distinguishable
	// from alphabetical.
	for _, b := range []Button{
		{Label: "Cola", Code: "C_COLA", ItemID: "i_cola"},
		{Label: "Bread", Code: "C_BREAD", ItemID: "i_bread"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}
	return db, store, &ButtonsHTTP{Store: *store, View: renderer}
}

func renderList(t *testing.T, h *ButtonsHTTP) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// mustContainAll / mustContainNone keep the per-mode assertions readable:
// each mode is defined as much by what it does NOT render as by what it
// does (the whole point is that the three shapes never overlap).
func mustContainAll(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Fatalf("expected %q in the render, got: %s", w, body)
		}
	}
}

func mustContainNone(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Fatalf("did not expect %q in the render, got: %s", u, body)
		}
	}
}

// category_tabs (the default): a grid of category tiles — one per top-level
// category with at least one ACTIVE item anywhere in its subtree, quick
// button or not (Household has no quick button at all and still gets a
// tile; the ut-docs#2283 Categories tab, which only ever tiled
// quick-button categories, would have dropped it) — each fetching its
// popup body from /ui/buttons/category on tap. No tab strip, no All grid,
// no chips; the strip's own search icon is still there.
func TestButtonsHTTPList_CategoryTabsModeRendersTiles(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "category_tabs"
	body := renderList(t, h)

	mustContainAll(t, body,
		`id="browsing-category-tiles"`,
		`data-cat-name="Food"`, `data-cat-name="Drinks"`, `data-cat-name="Household"`,
		`hx-get="/ui/buttons/category?id=cat_food"`,
		`hx-get="/ui/buttons/category?id=cat_house"`,
		`hx-target="#category-items-modal-body"`,
		`id="products-search"`,
	)
	// The uncategorized bucket gets a tile too (Loose Sweet has no
	// category) — its popup asks for id="" the same way BuildCategoryGroups
	// keys that bucket.
	mustContainAll(t, body, `hx-get="/ui/buttons/category?id="`, "products.uncategorized<")
	if got := strings.Count(body, `class="category-tile"`); got != 4 {
		t.Fatalf("expected 4 category tiles (Food, Drinks, Household, Uncategorized), got %d: %s", got, body)
	}
	// Per-tile count is ITEMS (every active item in the subtree), not quick
	// buttons: Food = Bread + Butter (nested Dairy). The test i18n renders a
	// key as its name, so the count line reads as the key + the printf arg.
	mustContainAll(t, body, `category-tile-count">categories.item_count%!(EXTRA int=2)<`)
	mustContainNone(t, body, "products.category_button_count")
	mustContainNone(t, body,
		`class="tab-bar"`, `id="cat-tab-all"`, `id="cat-tab-more"`, `id="buttons-grid-all"`,
		`id="browsing-category-chips"`, `id="cat-tab-categories"`, `id="cat-panel-categories"`,
	)
	// The nested Dairy subcategory never gets its own tile (top-level only,
	// same rule the retired Categories tab had) — its Butter is reachable
	// through Food's popup instead (see TestButtonsHTTPCategoryItems_*).
	mustContainNone(t, body, `data-cat-name="Dairy"`)
	// Category tiles are not quick-button tiles: jiggle mode keys off
	// .btn-tile[data-code], and there must be none for it to arm on here.
	mustContainNone(t, body, `class="btn-tile`)
}

// all_filter_chips: the All grid (every active item, first page) with a
// chip row above it — an "All" chip pressed by default plus one chip per
// top-level category with active items. Chips are real buttons with
// aria-pressed and a checkmark (never colour alone), each re-fetching the
// grid filtered to that category. No tab strip, no category tiles.
func TestButtonsHTTPList_AllFilterChipsModeRendersChipsOverAllGrid(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "all_filter_chips"
	body := renderList(t, h)

	mustContainAll(t, body,
		`id="browsing-category-chips"`, `role="group"`,
		`data-cat-all`, `aria-pressed="true"`,
		`data-cat-id="cat_food"`, `data-cat-id="cat_drink"`, `data-cat-id="cat_house"`,
		`hx-get="/ui/buttons/all/more?offset=0&category=cat_house"`,
		`hx-target="#buttons-grid-all"`,
		`class="chip-check"`,
		`id="buttons-grid-all"`,
		// every active item is in the grid, quick button or not
		`data-name="Butter"`, `data-name="Soap"`, `data-name="Loose Sweet"`,
		`id="products-search"`,
	)
	mustContainNone(t, body,
		`class="tab-bar"`, `id="cat-tab-all"`, `id="cat-tab-more"`,
		`id="browsing-category-tiles"`, `class="category-tile"`,
		`data-name="Old Mop"`, // inactive
	)
	// Exactly one chip per top-level category with active items, plus the
	// uncategorized bucket (Loose Sweet has no category — it must stay
	// reachable through a chip too), plus All.
	if got := strings.Count(body, `data-cat-id="`); got != 4 {
		t.Fatalf("expected 4 category chips (Food, Drinks, Household, Uncategorized), got %d: %s", got, body)
	}
	mustContainAll(t, body, `data-cat-id=""`, `hx-get="/ui/buttons/all/more?offset=0&category="`)
	if strings.Contains(body, `data-cat-id="cat_dairy"`) {
		t.Fatalf("nested Dairy must fold into Food's chip, not get its own: %s", body)
	}
}

// strip_overflow: today's sell screen, byte-for-byte in spirit — the All
// tab first and default-selected, one tab per quick-button category, the
// ut-docs#2307 "…" button, no chips, no tiles, and no trace of the retired
// Categories tab.
func TestButtonsHTTPList_StripOverflowModeIsTodaysStrip(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "strip_overflow"
	body := renderList(t, h)

	mustContainAll(t, body,
		`tab: '__all__',`, `id="cat-tab-all"`, `id="cat-tab-cat_food"`, `id="cat-tab-cat_drink"`,
		`id="cat-tab-more"`, `id="category-overflow-dialog"`, `id="buttons-grid-all"`,
		`id="cat-panel-cat_food"`,
		// Household has active items but no quick button: since
		// ut-docs#2498 (merged to main alongside this card) the strip keeps
		// such a category as its own tab with an empty-state message rather
		// than pruning it.
		`id="cat-tab-cat_house"`,
	)
	mustContainNone(t, body,
		`id="browsing-category-chips"`, `id="browsing-category-tiles"`, `class="category-tile"`,
		`id="cat-tab-categories"`, `id="cat-panel-categories"`,
	)
}

// The Designer's live replica (EditMode) always renders the quick-button
// strip whatever the shop's browsing mode is — it exists to arrange quick
// buttons, and neither the tile popup (index.html's modal, which the
// Designer page doesn't have) nor the All grid is a quick-button surface.
func TestButtonsHTTPList_EditModeAlwaysRendersStrip(t *testing.T) {
	for _, mode := range []string{"category_tabs", "all_filter_chips"} {
		_, _, h := newBrowsingModeTestHTTP(t)
		h.BrowsingMode = mode
		h.EditMode = true
		h.HideAllTab = true
		body := renderList(t, h)
		mustContainAll(t, body, `id="cat-tab-cat_food"`, `id="cat-tab-cat_drink"`, `data-testid="designer-categories"`)
		mustContainNone(t, body, `id="browsing-category-chips"`, `id="browsing-category-tiles"`, `id="buttons-grid-all"`)
	}
}

// A ButtonsHTTP literal that never sets BrowsingMode (every pre-#2499 test
// in this package, and the AllMore/Search/CategoryItems fragment handlers,
// none of which render the strip) renders the strip — the same "the Go
// zero value keeps the historical shape" convention HideAllTab's inverted
// naming documents. internal/pages/buttons_api.go is the one production
// caller and always passes the clamped live mode.
func TestButtonsHTTPList_ZeroValueBrowsingModeRendersStrip(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	body := renderList(t, h)
	mustContainAll(t, body, `id="cat-tab-all"`, `id="cat-tab-more"`)
	mustContainNone(t, body, `id="browsing-category-chips"`, `id="browsing-category-tiles"`)
}

// The popup body (ut-docs#2372, absorbed): EVERY active item in the
// category's subtree — quick buttons first, in their Designer order, then
// the rest A–Z — as real sellable tiles, with the popup's own search box;
// inactive items and other categories excluded.
func TestButtonsHTTPCategoryItems_AllActiveItemsQuickButtonsFirst(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	rec := httptest.NewRecorder()
	h.CategoryItems(rec, httptest.NewRequest("GET", "/ui/buttons/category?id=cat_food", nil))
	if rec.Code != 200 {
		t.Fatalf("CategoryItems = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustContainAll(t, body, `data-name="Bread"`, `data-name="Butter"`, `class="category-items-search"`, `hx-post="/api/pos/scan"`)
	mustContainNone(t, body, `data-name="Cola"`, `data-name="Soap"`, `data-name="Loose Sweet"`, `data-name="Old Mop"`)
	// "Bread" < "Butter" alphabetically anyway, so the quick-buttons-first
	// ordering is pinned by the next test with a quick button that sorts
	// LATER than a plain item.
	// Each item renders exactly once (its wrapper cell carries data-name
	// for the popup's own search; the tile inside carries it too, as every
	// product-tile-result does — count the cells).
	if strings.Count(body, `class="category-items-cell" data-name="Bread"`) != 1 || strings.Count(body, `class="category-items-cell" data-name="Butter"`) != 1 {
		t.Fatalf("each item must render exactly once in the popup, got: %s", body)
	}
	// The popup's tiles are search-result-shaped tiles (no per-tile
	// jiggle badges: this isn't a quick-button grid, ut-docs#2499 AC 6).
	mustContainNone(t, body, `data-testid="tile-badge-remove"`)
}

func TestButtonsHTTPCategoryItems_QuickButtonOrderBeatsAlphabetical(t *testing.T) {
	db, store, h := newBrowsingModeTestHTTP(t)
	// Household: Zinc Polish is a quick button, Soap is not; alphabetically
	// Soap < Zinc Polish, so a quick-buttons-first render puts Zinc first.
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i_zinc', 'S7', 'Zinc Polish', 300, 'cat_house', 1),
		('i_apron', 'S8', 'Apron', 800, 'cat_house', 1)`)
	if err := store.Add(Button{Label: "Zinc Polish", Code: "C_ZINC", ItemID: "i_zinc"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	h.Store = *store
	rec := httptest.NewRecorder()
	h.CategoryItems(rec, httptest.NewRequest("GET", "/ui/buttons/category?id=cat_house", nil))
	body := rec.Body.String()
	zinc := strings.Index(body, `data-name="Zinc Polish"`)
	apron := strings.Index(body, `data-name="Apron"`)
	soap := strings.Index(body, `data-name="Soap"`)
	if zinc < 0 || apron < 0 || soap < 0 {
		t.Fatalf("expected Zinc Polish, Apron and Soap in Household's popup, got: %s", body)
	}
	if !(zinc < apron && apron < soap) {
		t.Fatalf("want quick button first (Zinc Polish), then the rest A–Z (Apron, Soap); got positions zinc=%d apron=%d soap=%d", zinc, apron, soap)
	}
	mustContainNone(t, body, `data-name="Old Mop"`)
}

// id="" is the uncategorized bucket (same convention as
// BuildCategoryGroups' synthetic group); an unknown id renders an empty
// popup, never an error — a stale tile after a category was removed must
// not 500 the sell screen.
func TestButtonsHTTPCategoryItems_UncategorizedAndUnknown(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	rec := httptest.NewRecorder()
	h.CategoryItems(rec, httptest.NewRequest("GET", "/ui/buttons/category?id=", nil))
	if rec.Code != 200 {
		t.Fatalf("CategoryItems(uncategorized) = %d", rec.Code)
	}
	mustContainAll(t, rec.Body.String(), `data-name="Loose Sweet"`)
	mustContainNone(t, rec.Body.String(), `data-name="Bread"`, `data-name="Soap"`)

	rec = httptest.NewRecorder()
	h.CategoryItems(rec, httptest.NewRequest("GET", "/ui/buttons/category?id=cat_gone", nil))
	if rec.Code != 200 {
		t.Fatalf("CategoryItems(unknown) = %d", rec.Code)
	}
	mustContainNone(t, rec.Body.String(), `data-name="`)
	mustContainAll(t, rec.Body.String(), "products.no_matches<")
}

// The All grid's fragment route doubles as the chip filter: ?category=X
// narrows LoadAllActive to that category's subtree (nested Dairy folds
// into Food), still paginated; no ?category (or an empty one) is the whole
// catalog exactly as before ut-docs#2499.
func TestButtonsHTTPAllMore_CategoryFilter(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	rec := httptest.NewRecorder()
	h.AllMore(rec, httptest.NewRequest("GET", "/ui/buttons/all/more?offset=0&category=cat_food", nil))
	if rec.Code != 200 {
		t.Fatalf("AllMore = %d", rec.Code)
	}
	body := rec.Body.String()
	mustContainAll(t, body, `data-name="Bread"`, `data-name="Butter"`)
	mustContainNone(t, body, `data-name="Cola"`, `data-name="Soap"`, `data-name="Loose Sweet"`, `data-testid="all-more-btn"`)

	rec = httptest.NewRecorder()
	h.AllMore(rec, httptest.NewRequest("GET", "/ui/buttons/all/more?offset=0", nil))
	mustContainAll(t, rec.Body.String(), `data-name="Bread"`, `data-name="Cola"`, `data-name="Soap"`, `data-name="Loose Sweet"`)
}

// Review finding (ut-docs#2499): a category tile's popup must never sit
// silently blank — the tile carries translated loading/error text and
// wires htmx's response-error/send-error to show the error line in the
// popup body if the fetch fails.
func TestButtonsHTTPList_CategoryTilePopupHasLoadingAndErrorStates(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "category_tabs"
	body := renderList(t, h)
	mustContainAll(t, body,
		`data-loading-text="common.loading"`,
		`data-error-text="common.error.server"`,
		`hx-on::response-error=`,
		`hx-on::send-error=`,
	)
}
