package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ADR-0101 (ut-docs#2399): the handler-level contract of a shop-wide
// modifier group — created with no item, assigned to categories and items
// by selection from its /modifiers card, deletable everywhere.

func postModifiers(t *testing.T, mux *http.ServeMux, path, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Hx-Target", "modifiers-list")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Creating with no itemId at all makes a standalone group: a row, no link,
// listed on the re-rendered #modifiers-list as unassigned.
func TestModifierGroupCreate_WithoutItemMakesStandaloneGroup(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := postModifiers(t, mux, "/api/catalog/modifier-group", "name=Sauces&isActive=1&minSelect=0&maxSelect=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "modifiers-changed" {
		t.Fatalf("HX-Trigger = %q, want modifiers-changed", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="modifiers-list"`) || !strings.Contains(body, "Sauces") {
		t.Fatalf("expected the shop-wide fragment listing the new group: %s", body)
	}
	var groups, links int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_groups WHERE name = 'Sauces'`).Scan(&groups); err != nil || groups != 1 {
		t.Fatalf("group rows = %d err=%v, want 1", groups, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_group_links`).Scan(&links); err != nil || links != 0 {
		t.Fatalf("a standalone create must write no link row, got %d (err=%v)", links, err)
	}
}

// A create that DOES carry an itemId (the pre-ADR-0101 form shape, still
// accepted) creates the group and links it to that item.
func TestModifierGroupCreate_WithItemCreatesAndLinks(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := postModifiers(t, mux, "/api/catalog/modifier-group", "itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var links int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_group_links l JOIN item_modifier_groups g ON g.id = l.group_id WHERE l.item_id = 'itm1' AND g.name = 'Extras'`).Scan(&links); err != nil || links != 1 {
		t.Fatalf("expected the new group linked to itm1, got %d (err=%v)", links, err)
	}
}

func TestModifierGroupCreate_RequiresName(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})
	if rec := postModifiers(t, mux, "/api/catalog/modifier-group", "isActive=1&minSelect=0&maxSelect=2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name: want 400, got %d", rec.Code)
	}
}

// attach-category / detach-category: one link row at a time from the
// group's card, re-rendering #modifiers-list with the checkbox state
// following the link, and firing the sale-screen refresh trigger.
func TestModifierGroupCategoryToggle_LinksAndUnlinks(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedCategory(t, db, "cat1", "Drinks", true)
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "g1", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := postModifiers(t, mux, "/api/catalog/modifier-group/attach-category", "groupId=g1&categoryId=cat1")
	if rec.Code != http.StatusOK {
		t.Fatalf("attach-category: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "modifiers-changed" {
		t.Fatalf("attach-category HX-Trigger = %q, want modifiers-changed", got)
	}
	if !strings.Contains(rec.Body.String(), `name="categoryId" value="cat1" checked`) {
		t.Fatalf("re-rendered card must show the category checked: %s", rec.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM category_modifier_group_links WHERE category_id = 'cat1' AND group_id = 'g1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("category link rows = %d err=%v, want 1", n, err)
	}
	// Re-attaching settles safely (ON CONFLICT DO UPDATE), no duplicate.
	if rec := postModifiers(t, mux, "/api/catalog/modifier-group/attach-category", "groupId=g1&categoryId=cat1"); rec.Code != http.StatusOK {
		t.Fatalf("re-attach: want 200, got %d", rec.Code)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id = 'g1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("category link rows after re-attach = %d err=%v, want 1", n, err)
	}

	rec = postModifiers(t, mux, "/api/catalog/modifier-group/detach-category", "groupId=g1&categoryId=cat1")
	if rec.Code != http.StatusOK {
		t.Fatalf("detach-category: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `name="categoryId" value="cat1" checked`) {
		t.Fatalf("re-rendered card must show the category unchecked: %s", rec.Body.String())
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id = 'g1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("category link rows after detach = %d err=%v, want 0", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("detach-category must never touch the group row: n=%d err=%v", n, err)
	}

	// Validation: both ids required; an unknown id is a 400, not a 500.
	if rec := postModifiers(t, mux, "/api/catalog/modifier-group/attach-category", "groupId=g1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing categoryId: want 400, got %d", rec.Code)
	}
	if rec := postModifiers(t, mux, "/api/catalog/modifier-group/attach-category", "groupId=g1&categoryId=no-such"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown category: want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// delete: the group row goes, and with it every option, item link,
// category link and opt-out; the items and categories themselves stay.
func TestModifierGroupDelete_RemovesGroupEverywhere(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "TEA", Name: "Latte", BasePrice: 350, IsActive: true})
	testsupport.SeedCategory(t, db, "cat1", "Drinks", true)
	testsupport.SeedModifierGroup(t, db, "g1", "itm1", "Milk", false, 0, 1, 0, true)
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateOption(t.Context(), "o1", "g1", "Oat", 40, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(t.Context(), "cat1", "g1", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.OptOutItemFromGroup(t.Context(), "itm2", "g1"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := postModifiers(t, mux, "/api/catalog/modifier-group/delete", "groupId=g1")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "modifiers-changed" {
		t.Fatalf("delete HX-Trigger = %q, want modifiers-changed", got)
	}
	if strings.Contains(rec.Body.String(), `data-group-id="g1"`) {
		t.Fatal("the deleted group must not render in the re-rendered list")
	}
	for _, tc := range []struct {
		q    string
		want int
	}{
		{`SELECT COUNT(*) FROM item_modifier_groups`, 0},
		{`SELECT COUNT(*) FROM item_modifier_options`, 0},
		{`SELECT COUNT(*) FROM item_modifier_group_links`, 0},
		{`SELECT COUNT(*) FROM category_modifier_group_links`, 0},
		{`SELECT COUNT(*) FROM item_modifier_group_opt_outs`, 0},
		{`SELECT COUNT(*) FROM items`, 2},
		{`SELECT COUNT(*) FROM categories`, 1},
	} {
		var n int
		if err := db.QueryRow(tc.q).Scan(&n); err != nil || n != tc.want {
			t.Fatalf("%s = %d err=%v, want %d", tc.q, n, err, tc.want)
		}
	}
	if rec := postModifiers(t, mux, "/api/catalog/modifier-group/delete", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing groupId: want 400, got %d", rec.Code)
	}
}

// All three new routes are catalog mutations of admin-synced tables and
// must refuse on a replica, same convention as every other group mutation
// (TestModifierGroupAttachDetach_RefusedOnReplica).
func TestModifierGroupCategoryAndDelete_RefusedOnReplica(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedCategory(t, db, "cat1", "Drinks", true)
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "g1", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}, Settings: st})

	for _, tc := range []struct{ path, form string }{
		{"/api/catalog/modifier-group/attach-category", "groupId=g1&categoryId=cat1"},
		{"/api/catalog/modifier-group/detach-category", "groupId=g1&categoryId=cat1"},
		{"/api/catalog/modifier-group/delete", "groupId=g1"},
		{"/api/catalog/modifier-group", "name=Standalone&isActive=1&minSelect=0&maxSelect=1"},
	} {
		if rec := postModifiers(t, mux, tc.path, tc.form); rec.Code != http.StatusConflict {
			t.Fatalf("%s on replica: want 409, got %d: %s", tc.path, rec.Code, rec.Body.String())
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_groups`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("group rows after refused mutations = %d err=%v, want 1 (untouched)", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM category_modifier_group_links`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("category links after refused attach = %d err=%v, want 0", n, err)
	}
}

// The option create/update route no longer needs an itemId: /modifiers'
// option forms carry none, since the group has no item.
func TestModifierOptionCreate_NeedsNoItemID(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "g1", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})

	rec := postModifiers(t, mux, "/api/catalog/modifier-option", "groupId=g1&name=Oat&priceDeltaMajor=0.40&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var delta int64
	if err := db.QueryRow(`SELECT price_delta_minor FROM item_modifier_options WHERE group_id = 'g1'`).Scan(&delta); err != nil || delta != 40 {
		t.Fatalf("option delta = %d err=%v, want 40", delta, err)
	}
	if !strings.Contains(rec.Body.String(), `data-minor="40"`) {
		t.Fatalf("re-rendered card must show the new option: %s", rec.Body.String())
	}
}
