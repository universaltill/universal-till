package ui

import (
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// newCategoriesTabTestHTTP builds a real ButtonsHTTP against a fresh test DB
// — same renderer file set TestButtonsHTTPList_AllTabIsFirstAndDefaultSelected
// (ut-docs#2212) already uses, so the two features are exercised through the
// exact same render path.
func newCategoriesTabTestHTTP(t *testing.T) (*sql.DB, *ButtonStore, *ButtonsHTTP) {
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

// TestButtonsHTTPList_CategoriesTabOffByDefault (ut-docs#2283): a till that
// never opens Settings → Categories tab must render exactly today's tab bar
// — no cat-tab-categories tab, no #cat-panel-categories tile grid.
//
// Review (ut-docs#2283): this MUST seed two real, non-empty top-level
// categories first. buttons.html only renders the tab bar (and the panel
// block the Categories panel lives in) at all when $hasTabs — >= 2
// surviving top-level groups. Against the bare setupFullTestDB fixture
// .Groups is empty, so both id="" assertions below held no matter what
// CategoriesTabEnabled returned, and the test passed unchanged with the
// settings gate ripped out of ButtonStore.CategoriesTabEnabled (verified
// live). The All-tab assertion at the end is the positive control that
// keeps it from silently going vacuous again.
func TestButtonsHTTPList_CategoriesTabOffByDefault(t *testing.T) {
	db, store, h := newCategoriesTabTestHTTP(t)
	seedTwoCategoriesWithButtons(t, db, store)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Positive control: the tab bar really did render, so the two absence
	// assertions below are about the setting, not about an empty catalog.
	if !strings.Contains(body, `id="cat-tab-all"`) {
		t.Fatalf("the tab bar did not render at all — this test would be vacuous, got: %s", body)
	}
	if strings.Contains(body, `id="cat-tab-categories"`) {
		t.Fatalf("expected no Categories tab when the setting is off, got: %s", body)
	}
	if strings.Contains(body, `id="cat-panel-categories"`) {
		t.Fatalf("expected no Categories tile panel when the setting is off, got: %s", body)
	}
}

// seedTwoCategoriesWithButtons gives the render two surviving top-level
// groups, which is what buttons.html's own $hasTabs (>= 2 groups) needs
// before it renders a tab bar at all. Deliberately does NOT touch
// data.SellScreenCategoriesTabKey — callers that want the tab ON set it
// themselves.
func seedTwoCategoriesWithButtons(t *testing.T, db *sql.DB, store *ButtonStore) {
	t.Helper()
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
}

// TestButtonsHTTPList_CategoriesTabRendersFirst (ut-docs#2283): once
// enabled, the Categories tab is a real, focusable, ARIA-correct tab that
// renders BEFORE every other tab — including the ut-docs#2212 All tab, not
// just before the per-category ones.
func TestButtonsHTTPList_CategoriesTabRendersFirst(t *testing.T) {
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
	for _, b := range []Button{
		{Label: "Bread", Code: "C1", ItemID: "i1"},
		{Label: "Cola", Code: "C2", ItemID: "i2"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}
	if err := data.NewSettingsRepo(db).Set(t.Context(), data.SellScreenCategoriesTabKey, "1"); err != nil {
		t.Fatalf("enable categories tab setting: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	catsIdx := strings.Index(body, `id="cat-tab-categories"`)
	allIdx := strings.Index(body, `id="cat-tab-all"`)
	foodIdx := strings.Index(body, `id="cat-tab-cat_food"`)
	if catsIdx < 0 {
		t.Fatalf("expected a Categories tab button (id=cat-tab-categories), got: %s", body)
	}
	if allIdx < 0 || catsIdx > allIdx {
		t.Fatalf("expected the Categories tab to render before the All tab, got: %s", body)
	}
	if foodIdx < 0 || catsIdx > foodIdx {
		t.Fatalf("expected the Categories tab to render before every per-category tab, got: %s", body)
	}

	// i18n: goes through T, not a hardcoded literal.
	if !strings.Contains(body, "products.categories<") {
		t.Fatalf("expected the products.categories key to render as the Categories tab's label, got: %s", body)
	}

	// The tile grid panel: one tile per top-level category actually
	// rendered (both Food and Drinks have an active button each).
	if !strings.Contains(body, `id="cat-panel-categories"`) {
		t.Fatalf("expected a Categories tile panel (id=cat-panel-categories), got: %s", body)
	}
	// Review fix (ut-docs#2283): unlike the All tab, this tab owns exactly
	// ONE panel and points at it with aria-controls, so that panel has to be
	// a real tabpanel labelled by the tab — not the role="group" it first
	// shipped as, which left the tab's aria-controls pointing at a
	// non-tabpanel.
	if !strings.Contains(body, `id="cat-panel-categories" class="products-tab-panel" role="tabpanel" aria-labelledby="cat-tab-categories"`) {
		t.Fatalf("expected #cat-panel-categories to be a role=tabpanel labelled by #cat-tab-categories, got: %s", body)
	}
	if !strings.Contains(body, `aria-controls="cat-panel-categories"`) {
		t.Fatalf("expected the Categories tab to declare aria-controls for its own panel, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 2 {
		t.Fatalf("expected exactly 2 category tiles (Food, Drinks), got %d: %s", got, body)
	}
	if !strings.Contains(body, `data-cat-name="Food"`) || !strings.Contains(body, `data-cat-name="Drinks"`) {
		t.Fatalf("expected a Food tile and a Drinks tile, got: %s", body)
	}
}

// TestButtonsHTTPList_CategoriesTabHidesZeroActiveItemCategory
// (ut-docs#2283): a category with no active items anywhere in its own
// subtree never gets a tile — same "hide, don't disable" decision the
// per-category tab bar already applies (ui.BuildCategoryGroups prunes an
// empty branch before this template ever sees it).
func TestButtonsHTTPList_CategoriesTabHidesZeroActiveItemCategory(t *testing.T) {
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

	// A third, non-empty category (Drinks) is needed alongside Food so
	// $hasTabs (>=2 SURVIVING top-level groups) is still true once Empty is
	// pruned out entirely — ui.BuildCategoryGroups drops a branch with no
	// buttons anywhere in its subtree before this template ever sees it, so
	// with only Food+Empty in the DB, Empty wouldn't just be tile-less, the
	// WHOLE tab bar (Categories tab included) would never render at all,
	// which would test the wrong thing.
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_empty', 'Empty', NULL, 2),
		('cat_drink', 'Drinks', NULL, 3)`)
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
	if err := data.NewSettingsRepo(db).Set(t.Context(), data.SellScreenCategoriesTabKey, "1"); err != nil {
		t.Fatalf("enable categories tab setting: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if strings.Contains(body, `data-cat-name="Empty"`) {
		t.Fatalf("expected the zero-active-item category to have no tile, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 2 {
		t.Fatalf("expected exactly 2 category tiles (Food, Drinks — not Empty), got %d: %s", got, body)
	}
}

// TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategories
// (ut-docs#2283): a nested subcategory never gets its own tile — the tile
// grid is top-level categories only (matches category_filter.html's own
// top-level-only convention); its items are still reachable, folded into
// its top-level parent's own item-picker modal (see buttons.html's
// openCategoryPicker comment), never a second-level modal-within-modal.
func TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategories(t *testing.T) {
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
		('cat_food_specials', 'Specials', 'cat_food', 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Pie', 140, 'cat_food_specials', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	for _, b := range []Button{
		{Label: "Pie", Code: "C1", ItemID: "i1"},
		{Label: "Cola", Code: "C2", ItemID: "i2"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}
	if err := data.NewSettingsRepo(db).Set(t.Context(), data.SellScreenCategoriesTabKey, "1"); err != nil {
		t.Fatalf("enable categories tab setting: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Food (top-level, has no OWN button, only a nested one) still gets a
	// tile — it has active items somewhere in its subtree.
	if !strings.Contains(body, `data-cat-name="Food"`) {
		t.Fatalf("expected a Food tile (has active items via its subcategory), got: %s", body)
	}
	// Specials (nested) must NOT get its own tile.
	if strings.Contains(body, `data-cat-name="Specials"`) {
		t.Fatalf("expected no separate tile for the nested Specials subcategory, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 2 {
		t.Fatalf("expected exactly 2 top-level tiles (Food, Drinks), got %d: %s", got, body)
	}
}
