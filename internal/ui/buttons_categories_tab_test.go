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

// panelSlice returns the substring of body starting at the panel whose id is
// "cat-panel-"+catID, up to (but not including) the next "id=\"cat-panel-"
// occurrence, or to the end of body if there is none. Test helper shared by
// the ut-docs#2498 empty-state assertions below, which need to scope their
// checks to ONE category's panel rather than match anywhere in the page.
func panelSlice(t *testing.T, body, catID string) string {
	t.Helper()
	marker := `id="cat-panel-` + catID + `"`
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("expected a panel %q, got: %s", marker, body)
	}
	rest := body[idx+1:]
	if end := strings.Index(rest, `id="cat-panel-`); end >= 0 {
		return body[idx : idx+1+end]
	}
	return body[idx:]
}

// TestButtonsHTTPList_CategoryWithActiveItemButNoButtonStillAppears
// (ut-docs#2498, the core of this card): a category with a real active item
// but NO quick button anywhere in its own subtree must still surface — both
// as its own tab in the classic per-category tab bar and as its own tile in
// the settings-gated Categories tab tile grid — instead of being silently
// pruned by BuildCategoryGroups the way it used to be (pruning used to key
// ONLY on "has a quick button somewhere in the subtree"). Its panel/section
// must render the new translated empty-state message rather than an empty
// grid, since there is nothing to grid.
func TestButtonsHTTPList_CategoryWithActiveItemButNoButtonStillAppears(t *testing.T) {
	db, store, h := newCategoriesTabTestHTTP(t)

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 'cat_food', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	// Only Food ever gets a quick button. Drinks has a real active item
	// (Cola) but NO quick button at all -- exactly the card's bug scenario.
	if err := store.Add(Button{Label: "Bread", Code: "C1", ItemID: "i1"}); err != nil {
		t.Fatalf("Add: %v", err)
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

	// Classic per-category tab bar: Drinks gets its own tab even with zero
	// quick buttons, because it has an active item.
	if !strings.Contains(body, `id="cat-tab-cat_drink"`) {
		t.Fatalf("expected a Drinks tab (has an active item, Cola) even though it has no quick button, got: %s", body)
	}
	// Categories tab tile grid: same category, via a tile this time.
	if !strings.Contains(body, `data-cat-name="Drinks"`) {
		t.Fatalf("expected a Drinks tile in the Categories tab, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 2 {
		t.Fatalf("expected exactly 2 category tiles (Food, Drinks), got %d: %s", got, body)
	}

	// Drinks' own panel has zero buttons -- it must render the translated
	// empty-state message instead of an empty grid.
	drinksPanel := panelSlice(t, body, "cat_drink")
	if !strings.Contains(drinksPanel, `data-testid="category-empty-state"`) {
		t.Fatalf("expected the Drinks panel to render the empty-state marker, got: %s", drinksPanel)
	}
	// i18n: goes through T, not a hardcoded literal (no InitI18n call in
	// this package's tests, so T falls back to the raw key — same
	// convention TestButtonsHTTPList_CategoriesTabRendersFirst already
	// uses for products.categories above).
	if !strings.Contains(drinksPanel, "products.category_no_buttons<") {
		t.Fatalf("expected the products.category_no_buttons key to render in the Drinks panel, got: %s", drinksPanel)
	}
	// Food's panel DOES have a button -- no empty-state marker there.
	foodPanel := panelSlice(t, body, "cat_food")
	if strings.Contains(foodPanel, `data-testid="category-empty-state"`) {
		t.Fatalf("expected no empty-state marker in Food's panel (it has a quick button), got: %s", foodPanel)
	}
}

// TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategoryWithNoButtons
// (ut-docs#2498): extends
// TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategories' shape to the
// zero-buttons-anywhere case — a nested subcategory whose only item has NO
// quick button at all must still keep its top-level ancestor reachable (via
// the ancestor's active-item count propagating up through the subcategory),
// exactly as before but through the NEW survival path this card adds, and
// the ancestor's own panel must show the empty-state message.
func TestButtonsHTTPList_CategoriesTabFlattensNestedSubcategoryWithNoButtons(t *testing.T) {
	db, store, h := newCategoriesTabTestHTTP(t)

	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_food_specials', 'Specials', 'cat_food', 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Pie', 140, 'cat_food_specials', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	// Cola gets a quick button (needed so Drinks survives as a second
	// top-level group alongside Food, same as the sibling flatten test);
	// Pie (Food's only item, nested under Specials) never gets one at all.
	if err := store.Add(Button{Label: "Cola", Code: "C2", ItemID: "i2"}); err != nil {
		t.Fatalf("Add: %v", err)
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

	if !strings.Contains(body, `data-cat-name="Food"`) {
		t.Fatalf("expected a Food tile (has an active item via its subcategory, even with zero quick buttons anywhere), got: %s", body)
	}
	if strings.Contains(body, `data-cat-name="Specials"`) {
		t.Fatalf("expected no separate tile for the nested Specials subcategory, got: %s", body)
	}
	if got := strings.Count(body, `class="category-tile"`); got != 2 {
		t.Fatalf("expected exactly 2 top-level tiles (Food, Drinks), got %d: %s", got, body)
	}

	foodPanel := panelSlice(t, body, "cat_food")
	if !strings.Contains(foodPanel, `data-testid="category-empty-state"`) {
		t.Fatalf("expected Food's panel to render the empty-state marker (zero buttons anywhere in its subtree), got: %s", foodPanel)
	}
	// Review finding (ut-docs#2498): Food itself has no direct buttons or
	// items -- only its nested Specials child does -- so Food's OWN
	// top-level section (category-group-body-tabbed) must NOT also emit a
	// second, headerless copy of the empty-state message; only Specials'
	// own headed "category-group" section should show it. Before this fix
	// the message rendered twice: once bare (Food's own branch, which has
	// no <h3> outside search/All) and once under Specials' real header --
	// confusing, since the bare copy names no category at all.
	if got := strings.Count(foodPanel, `data-testid="category-empty-state"`); got != 1 {
		t.Fatalf("expected exactly 1 empty-state message in Food's panel (Specials' own, not a duplicate bare one from Food itself), got %d: %s", got, foodPanel)
	}
}

// TestButtonsHTTPList_DefaultTabPrefersGroupWithButtons (ut-docs#2498, review
// finding): with the All tab off, the sale screen's default landing tab used
// to be simply Groups[0] -- fine when every surviving group has buttons, but
// since this card a group can survive with zero buttons anywhere (kept via
// an active-item count alone). Landing there by default would greet the
// operator with the empty-state message instead of real quick buttons on
// first paint, even though a perfectly usable category sits right next to
// it. The default must skip to the first group that HasButtons, falling
// back to Groups[0] only if none do.
func TestButtonsHTTPList_DefaultTabPrefersGroupWithButtons(t *testing.T) {
	db, store, h := newCategoriesTabTestHTTP(t)
	h.HideAllTab = true

	// Household sorts first but has an active item and NO quick button;
	// Food sorts second and has a real quick button. Category sort_order
	// (not alphabetical) is what .Groups is ordered by, so Household really
	// is Groups[0] here.
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_household', 'Household', NULL, 1),
		('cat_food', 'Food', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Sponge', 140, 'cat_household', 1),
		('i2', 'S2', 'Bread', 120, 'cat_food', 1)`)
	if err := store.Add(Button{Label: "Bread", Code: "C1", ItemID: "i2"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `tab: 'cat_food'`) {
		t.Fatalf("expected the default tab to be Food (has a quick button), not Household (items-only, sorts first), got: %s", body)
	}
}
