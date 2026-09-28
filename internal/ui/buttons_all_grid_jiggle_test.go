package ui

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2534: jiggle edit mode in the all_filter_chips All grid. Under a
// category chip the grid shows that category's items in the GLOBAL
// quick-button order (the order the category modes already use), and a
// drag there posts only that category's tiles (scope=subset) --
// ButtonStore.UpdateOrderSubset re-deals them into the global slots they
// already hold, so no other category's order moves.

// seedInterleavedCategories: two categories whose items interleave in the
// global order (all implicit, so name order): Apple(A) Banana(B) Cherry(A)
// Date(B) Egg(A) -> codes SA1 SB1 SA2 SB2 SA3.
func seedInterleavedCategories(t *testing.T) (*sql.DB, *ButtonStore, func() []string) {
	t.Helper()
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES ('cat_a','Alpha',NULL,1),('cat_b','Beta',NULL,2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('a1','SA1','Apple',100,'cat_a',1),
		('b1','SB1','Banana',100,'cat_b',1),
		('a2','SA2','Cherry',100,'cat_a',1),
		('b2','SB2','Date',100,'cat_b',1),
		('a3','SA3','Egg',100,'cat_a',1)`)
	store := NewButtonStore(db)
	order := func() []string {
		t.Helper()
		btns, err := store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		out := make([]string, 0, len(btns))
		for _, b := range btns {
			out = append(out, b.Code)
		}
		return out
	}
	return db, store, order
}

func TestButtonStoreUpdateOrderSubset_RedealsIntoOwnSlots(t *testing.T) {
	db, store, order := seedInterleavedCategories(t)
	if got := strings.Join(order(), ","); got != "SA1,SB1,SA2,SB2,SA3" {
		t.Fatalf("unexpected starting order %s", got)
	}
	// Category Alpha dragged to Egg, Apple, Cherry: its three slots (0,2,4)
	// take them in that order; Beta's slots (1,3) and order are untouched.
	if err := store.UpdateOrderSubset(context.Background(), []string{"SA3", "SA1", "SA2"}); err != nil {
		t.Fatalf("UpdateOrderSubset: %v", err)
	}
	if got := strings.Join(order(), ","); got != "SA3,SB1,SA1,SB2,SA2" {
		t.Fatalf("order after subset reorder = %s, want SA3,SB1,SA1,SB2,SA2", got)
	}
	// The implicit tiles the reorder touched became real rows (UpdateOrder's
	// own materialisation), so the new order survives.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM shortcut_buttons`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected the moved implicit tiles materialised into shortcut_buttons rows")
	}
}

// A partial subset (only the loaded page of a long category) re-deals into
// the slots those codes hold; the rest of the category keeps its place.
func TestButtonStoreUpdateOrderSubset_PartialPageUnknownAndDuplicateCodes(t *testing.T) {
	_, store, order := seedInterleavedCategories(t)
	if err := store.UpdateOrderSubset(context.Background(), []string{"SA2", "nope", "SA1", "SA2"}); err != nil {
		t.Fatalf("UpdateOrderSubset: %v", err)
	}
	if got := strings.Join(order(), ","); got != "SA2,SB1,SA1,SB2,SA3" {
		t.Fatalf("order = %s, want SA2,SB1,SA1,SB2,SA3", got)
	}
}

func TestButtonStoreUpdateOrderSubset_NoKnownCodesIsError(t *testing.T) {
	_, store, order := seedInterleavedCategories(t)
	err := store.UpdateOrderSubset(context.Background(), []string{"nope", ""})
	if !errors.Is(err, ErrNoKnownCodes) {
		t.Fatalf("err = %v, want ErrNoKnownCodes", err)
	}
	if got := strings.Join(order(), ","); got != "SA1,SB1,SA2,SB2,SA3" {
		t.Fatalf("order changed on a refused reorder: %s", got)
	}
}

// Under a category chip, AllMore renders that category in the global
// quick-button order (not A-Z), each tile carrying its global index as
// data-pos, plus the filter marker app.js keys drag/arrow-key moves off.
// Plain All stays alphabetical with no marker.
func TestButtonsHTTPAllMore_FilteredUsesQuickButtonOrderAndPos(t *testing.T) {
	_, store, h := newBrowsingModeTestHTTP(t)
	// Global order: Cola, Butter, Bread, Loose Sweet, Soap -- in Food,
	// Butter now comes before Bread (alphabetically it's the reverse).
	if err := store.UpdateOrder(context.Background(), []string{"C_COLA", "S2", "C_BREAD", "S5", "S4"}); err != nil {
		t.Fatalf("UpdateOrder: %v", err)
	}
	rec := httptest.NewRecorder()
	h.AllMore(rec, httptest.NewRequest("GET", "/ui/buttons/all/more?offset=0&category=cat_food", nil))
	body := rec.Body.String()
	butter, bread := strings.Index(body, `data-name="Butter"`), strings.Index(body, `data-name="Bread"`)
	if butter < 0 || bread < 0 || butter > bread {
		t.Fatalf("expected Butter before Bread (quick-button order), got: %s", body)
	}
	mustContainAll(t, body, `data-code="S2" data-item-id="i_butter" data-pos="1"`, `data-code="C_BREAD" data-item-id="i_bread" data-pos="2"`, `data-all-filter="cat_food"`)
	mustContainNone(t, body, `data-name="Cola"`)

	rec = httptest.NewRecorder()
	h.AllMore(rec, httptest.NewRequest("GET", "/ui/buttons/all/more?offset=0&category=all", nil))
	body = rec.Body.String()
	butter, bread = strings.Index(body, `data-name="Butter"`), strings.Index(body, `data-name="Bread"`)
	if butter < 0 || bread < 0 || bread > butter {
		t.Fatalf("expected plain All to stay alphabetical (Bread before Butter), got: %s", body)
	}
	mustContainNone(t, body, `data-all-filter`)
}

// The All grid carries data-edit-allowed only for a session holding
// catalog_management -- a cashier's long press there must not arm.
func TestButtonsHTTPList_AllGridEditAllowedMarker(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "all_filter_chips"
	mustContainAll(t, renderList(t, h), `id="buttons-grid-all" class="grid">`)
	h.Granted = true
	mustContainAll(t, renderList(t, h), `id="buttons-grid-all" class="grid" data-edit-allowed>`)
}

// Review fix: an IMPLICIT hidden item (sell_screen_hidden, no
// shortcut_buttons row) still holds a slot in Load()'s grid order -- the
// order UpdateOrder/UpdateOrderSubset compare against. The All grid under a
// chip lists visible items only, but each tile's data-pos must be its index
// in THAT order, not in one with the hidden item dropped; and a subset
// reorder of the visible tiles must leave the hidden one in its slot.
func TestAllGridFiltered_PosMatchesLoadWithImplicitHiddenItem(t *testing.T) {
	db, store, order := seedInterleavedCategories(t)
	mustExec(t, db, `UPDATE items SET sell_screen_hidden = 1 WHERE id = 'a2'`) // Cherry, implicit
	if got := strings.Join(order(), ","); got != "SA1,SB1,SA2,SB2,SA3" {
		t.Fatalf("Load() must keep the hidden item in its slot, got %s", got)
	}
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
	rec := httptest.NewRecorder()
	h.AllMore(rec, httptest.NewRequest("GET", "/ui/buttons/all/more?offset=0&category=cat_a", nil))
	body := rec.Body.String()
	// Apple is Load() index 0, Egg index 4 (Cherry, hidden, keeps 2).
	mustContainAll(t, body, `data-code="SA1" data-item-id="a1" data-pos="0"`, `data-code="SA3" data-item-id="a3" data-pos="4"`)
	mustContainNone(t, body, `data-name="Cherry"`)

	// The visible tiles, reordered around the hidden one: Egg, Apple.
	if err := store.UpdateOrderSubset(context.Background(), []string{"SA3", "SA1"}); err != nil {
		t.Fatalf("UpdateOrderSubset: %v", err)
	}
	if got := strings.Join(order(), ","); got != "SA3,SB1,SA2,SB2,SA1" {
		t.Fatalf("order = %s, want SA3,SB1,SA2,SB2,SA1 (hidden Cherry keeps slot 2)", got)
	}
}

// UX review: in the all_filter_chips mode the jiggle bar's title names what
// that grid's edit mode really does (choose a category to reorder its items;
// the pencil edits) -- the quick-button wording elsewhere. Other modes keep
// buttons.edit_mode.title.
func TestButtonsHTTPList_JiggleBarTitlePerMode(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.Granted = true
	// No translator is wired in this package's tests, so T renders the key.
	const quick = `<span class="jiggle-bar-title">buttons.edit_mode.title</span>`
	const allGrid = `<span class="jiggle-bar-title">buttons.edit_mode.title_all_grid</span>`
	h.BrowsingMode = "all_filter_chips"
	body := renderList(t, h)
	mustContainAll(t, body, allGrid)
	mustContainNone(t, body, quick)
	for _, mode := range []string{"strip_overflow", "category_tabs"} {
		h.BrowsingMode = mode
		body := renderList(t, h)
		mustContainAll(t, body, quick)
		mustContainNone(t, body, allGrid)
	}
}
