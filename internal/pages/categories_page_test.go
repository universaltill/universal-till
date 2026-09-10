package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

func newCategoriesTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	// Settings backs the replica check (SyncPrimaryURL) — left unset, this
	// till is a primary/standalone, matching every pre-existing test in this
	// package. See locations_page_test.go's identical comment.
	d := &common.Deps{
		Db:       db,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		AuthSvc:  auth.NewService(db),
		Settings: settings.NewStore(db),
		BtnStore: ui.NewButtonStore(db),
	}
	mux := http.NewServeMux()
	registerCategories(mux, d)
	return mux, d
}

func TestCategoriesPagePermissions(t *testing.T) {
	mux, _ := newCategoriesTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /categories = %d, want 403", rec.Code)
	}
	// ut-docs#1455: GET /categories must render the full layout on 403 too,
	// not a bare rail-less body.
	if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
		t.Fatalf("cashier's 403 on GET /categories has no nav rail:\n%s", body)
	}

	if rec := postForm(mux, "/api/categories", url.Values{"name": {"Drinks"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}
}

func TestCategoriesPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newCategoriesTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/categories", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /categories under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestCategoriesPageCreate_WhitespaceOnlyNameRejected(t *testing.T) {
	mux, _ := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/categories", url.Values{"name": {"   "}}, &manager)
	if rec.Header().Get("Location") != "/categories?err=categories.error.name_required" {
		t.Fatalf("whitespace-only name: loc=%q", rec.Header().Get("Location"))
	}
}

func TestCategoriesPageCreateRenameDeactivate(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// Create.
	rec := postForm(mux, "/api/categories", url.Values{"name": {"Drinks"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/categories" {
		t.Fatalf("create: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// It shows up on the rendered page.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Drinks") {
		t.Fatalf("categories page missing new category: code=%d body=%s", rec.Code, rec.Body.String())
	}

	var newID string
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Drinks'`).Scan(&newID); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	// Rename.
	rec = postForm(mux, "/api/categories/"+newID, url.Values{"name": {"Beverages"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: code=%d", rec.Code)
	}
	var name string
	if err := d.Db.QueryRow(`SELECT name FROM categories WHERE id = ?`, newID).Scan(&name); err != nil || name != "Beverages" {
		t.Fatalf("rename did not take effect: name=%q err=%v", name, err)
	}

	// A category with no items deactivates cleanly.
	rec = postForm(mux, "/api/categories/"+newID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/categories" {
		t.Fatalf("deactivate: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM categories WHERE id = ?`, newID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("not deactivated: active=%d err=%v", active, err)
	}

	// Reactivate.
	rec = postForm(mux, "/api/categories/"+newID+"/active", url.Values{"active": {"1"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reactivate: code=%d", rec.Code)
	}
	if err := d.Db.QueryRow(`SELECT is_active FROM categories WHERE id = ?`, newID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("not reactivated: active=%d err=%v", active, err)
	}
}

// The AC's "deactivating a category with items in it is handled explicitly
// (blocked, with an explanation)" requirement: SetCategoryActive's
// *data.ErrCategoryHasItems surfaces as categories.error.deactivate_blocked
// with the active item count as a &count= query param, and the row is left
// untouched.
func TestCategoriesPage_DeactivateBlockedWhenCategoryHasItems(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/categories", url.Values{"name": {"Drinks"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var catID string
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Drinks'`).Scan(&catID); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('i1', 'S1', 'Cola', 100, 1, ?)`, catID); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	rec = postForm(mux, "/api/categories/"+catID+"/active", url.Values{"active": {"0"}}, &manager)
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/categories?err=categories.error.deactivate_blocked&count=1") {
		t.Fatalf("blocked deactivate: loc=%q", loc)
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM categories WHERE id = ?`, catID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("category must remain active: active=%d err=%v", active, err)
	}
}

// ut-docs#1585 family: categories syncs shop-wide as an admin table, so a
// satellite-created/edited row would silently vanish on the next admin
// pull -- every mutation route must refuse on a replica instead.
func TestCategoriesPage_MutationsRefusedOnReplica(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	rec := postForm(mux, "/api/categories", url.Values{"name": {"Satellite Category"}}, &manager)
	if rec.Header().Get("Location") != "/categories?err=categories.error.replica_use_primary" {
		t.Fatalf("create on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var count int
	if err := d.Db.QueryRow(`SELECT count(*) FROM categories WHERE name = 'Satellite Category'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("category must not be created on a replica, found %d rows", count)
	}

	// Seed a category directly (bypassing the handler) to exercise rename/
	// active/reorder refusal too.
	if _, err := d.Db.Exec(`INSERT INTO categories (id, name) VALUES ('cat-x', 'Existing')`); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/categories/cat-x", url.Values{"name": {"Renamed"}}, &manager)
	if rec.Header().Get("Location") != "/categories?err=categories.error.replica_use_primary" {
		t.Fatalf("rename on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec = postForm(mux, "/api/categories/cat-x/active", url.Values{"active": {"0"}}, &manager)
	if rec.Header().Get("Location") != "/categories?err=categories.error.replica_use_primary" {
		t.Fatalf("deactivate on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	// Reorder is fetch-driven JS, not a plain form post — it must answer
	// with a status (409, matching buttons_api.go's own reorder gate), not a
	// redirect a fetch would silently follow and read back as success.
	rec = postForm(mux, "/api/categories/reorder", url.Values{"ids": {"cat-x"}}, &manager)
	if rec.Code != http.StatusConflict {
		t.Fatalf("reorder on replica: code=%d, want 409", rec.Code)
	}
}

func TestCategoriesPageReorder(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	postForm(mux, "/api/categories", url.Values{"name": {"Drinks"}}, &manager)
	postForm(mux, "/api/categories", url.Values{"name": {"Snacks"}}, &manager)
	postForm(mux, "/api/categories", url.Values{"name": {"Bakery"}}, &manager)

	var drinksID, snacksID, bakeryID string
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Drinks'`).Scan(&drinksID); err != nil {
		t.Fatal(err)
	}
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Snacks'`).Scan(&snacksID); err != nil {
		t.Fatal(err)
	}
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Bakery'`).Scan(&bakeryID); err != nil {
		t.Fatal(err)
	}

	// Target order Snacks, Bakery, Drinks — deliberately NOT alphabetical, and
	// the sort_order integers are asserted too: the read below tie-breaks on
	// `name`, so an implementation that writes one constant sort_order to
	// every row would satisfy an alphabetical target order. See the matching
	// note on TestSetCategorySortOrder.
	rec := postForm(mux, "/api/categories/reorder", url.Values{"ids": {snacksID, bakeryID, drinksID}}, &manager)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reorder: code=%d body=%s", rec.Code, rec.Body.String())
	}

	rows, err := d.Db.Query(`SELECT id, sort_order FROM categories ORDER BY sort_order, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var order []string
	var sortOrders []int
	for rows.Next() {
		var id string
		var so int
		if err := rows.Scan(&id, &so); err != nil {
			t.Fatal(err)
		}
		order = append(order, id)
		sortOrders = append(sortOrders, so)
	}
	if len(order) != 3 || order[0] != snacksID || order[1] != bakeryID || order[2] != drinksID {
		t.Fatalf("unexpected order after reorder: %+v", order)
	}
	for i, so := range sortOrders {
		if so != i {
			t.Fatalf("sort_order[%d] = %d, want %d (positions must be distinct, not collapsed): %+v", i, so, i, sortOrders)
		}
	}
}

// ut-docs#1950: /categories is one of the /items rail's five section
// destinations. An htmx request (from that panel) must get just the
// "content" block, plus an out-of-band refresh of the rail with
// /categories marked is-current — not the full standalone page's chrome.
func TestCategoriesPage_HXRequestReturnsContentFragmentWithOOBRail(t *testing.T) {
	mux, _ := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), manager)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /categories: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, `class="nav"`) {
		t.Errorf("htmx request re-rendered the whole page shell: %s", body)
	}
	railStart := strings.Index(body, `id="items-rail"`)
	if railStart < 0 || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("fragment missing the OOB items-rail swap: %s", body)
	}
	rail := body[railStart:]
	idx := strings.Index(rail, `href="/categories"`)
	if idx < 0 {
		t.Fatalf("OOB rail missing the /categories row: %s", rail)
	}
	tagStart := strings.LastIndex(rail[:idx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("the /categories row itself is not marked is-current: %s", rail[tagStart:tagEnd])
	}
}

// A plain browser GET (no HX-Request) must still render the exact same full
// standalone page as before this card.
func TestCategoriesPage_NonHXRequestStillRendersFullPage(t *testing.T) {
	mux, _ := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /categories: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("expected the full standalone page shell, got: %s", body)
	}
}

// The card's own flagged edge case, verified rather than assumed: a bare
// 303 Location redirect does not reliably re-trigger as an in-panel htmx
// swap in every browser once /categories' own mutation forms can be
// embedded inside /items' right panel (ut-docs#1950). An htmx request must
// get "HX-Redirect" instead, which forces a full client-side navigation —
// an honest fallback, not a seamless swap, but not silently broken either.
// AC #6: a category created here must (a) appear in the catalog item-edit
// category <select> (fed by ListActiveCategories via ReadLookup) and (b)
// appear as a sale-screen category tab (ButtonStore.LoadCategories, via
// BuildCategoryGroups — which only keeps a category group that has at least
// one button in it, so this seeds one).
func TestCategoriesPage_NewCategoryAppearsInCatalogSelectAndSaleScreenTab(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	catalog.Register(mux, d)
	registerButtonsAPI(mux, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/categories", url.Values{"name": {"Hot Drinks"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var catID string
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Hot Drinks'`).Scan(&catID); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	// (a) The catalog item-edit <select name="categoryId"> offers it.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/catalog", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog: code=%d body=%s", rec.Code, rec.Body.String())
	}
	wantOption := `<option value="` + catID + `">Hot Drinks</option>`
	if !strings.Contains(rec.Body.String(), wantOption) {
		t.Fatalf("catalog item-edit category <select> missing the new category %q; want to contain %q in:\n%s", catID, wantOption, rec.Body.String())
	}

	// (b) It appears as a sale-screen category tab once it has a button.
	if _, err := d.Db.Exec(`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('i1', 'S1', 'Latte', 350, 1, ?)`, catID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons (barcode, item_id, label) VALUES ('B1', 'i1', 'Latte')`); err != nil {
		t.Fatalf("seed shortcut button: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/ui/buttons", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Hot Drinks") {
		t.Fatalf("sale-screen category tab bar missing the new category's tab; body:\n%s", rec.Body.String())
	}

	// Deactivating the category removes it from the SALE-facing surfaces
	// (sale-screen tabs, kitchen routing — ListActiveCategories) — but only
	// once its item is out of the way (SetCategoryActive's guard), matching
	// the AC's explicit "blocked while items exist" behavior end to end.
	if _, err := d.Db.Exec(`UPDATE items SET is_active = 0 WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/categories/"+catID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/categories" {
		t.Fatalf("deactivate after item retired: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// (c) It's gone from the sale-screen tab bar (ListActiveCategories).
	req = httptest.NewRequest(http.MethodGet, "/ui/buttons", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons after deactivate: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Hot Drinks") {
		t.Fatalf("deactivated category must no longer appear as a sale-screen tab; body:\n%s", rec.Body.String())
	}

	// (d) It STILL appears in the catalog item-edit <select> — unlike the
	// sale-facing surfaces, this one is deliberately unfiltered
	// (lookupUnfilteredByActive, ut-docs#1898 review finding F2): if it
	// disappeared here too, saving an item still assigned to it (e.g. via
	// admin-sync's FK-blocked retire path, which bypasses this same
	// deactivate-guard entirely) would silently null its category_id,
	// exactly the bug ListAllTaxCodes/the brands carve-out already exist to
	// prevent for tax codes and brands.
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/catalog", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), wantOption) {
		t.Fatalf("deactivated-but-still-referenced category must still appear in the catalog item-edit <select>:\n%s", rec.Body.String())
	}
}
