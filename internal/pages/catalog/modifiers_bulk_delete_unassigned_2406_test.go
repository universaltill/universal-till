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

// ut-docs#2406 (follow-up from the ADR-0101/#2399 review's finding L5): a
// shop that cleans up many single-use-group items ends up with a pile of
// unassigned cards on /modifiers and only the per-group Delete to clear
// them one at a time. This file is the handler-level contract of the bulk
// "delete all unassigned" action and the count the page renders alongside
// it.

// The page shows the count of unassigned groups and renders the bulk
// control only when there is at least one; a shop with none gets no
// clutter, and the count itself is derived from the SAME test the
// unassigned hint already uses (neither Categories nor Items), never a
// second definition.
func TestModifiersPage_ShowsUnassignedCount(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "assigned", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(t.Context(), "itm1", "assigned", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGroup(t.Context(), "orphan1", "Orphan One", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGroup(t.Context(), "orphan2", "Orphan Two", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/modifiers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /modifiers = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "2 unassigned group") {
		t.Fatalf("expected the unassigned count (2) rendered, got:\n%s", body)
	}
	if !strings.Contains(body, `id="modifiers-filter-unassigned"`) {
		t.Fatal("expected the 'show only unassigned' filter checkbox")
	}
	if !strings.Contains(body, `/api/catalog/modifier-group/delete-unassigned`) {
		t.Fatal("expected the bulk-delete button's hx-post target")
	}
	if !strings.Contains(body, `class="tag modifier-assign-item`) {
		// sanity: the assigned group's item chip still renders as normal
		t.Fatal("expected the assigned group's item chip to still render")
	}
}

// Zero unassigned groups: no count, no filter, no bulk-delete button — the
// per-group controls and the create form are unaffected.
func TestModifiersPage_NoUnassignedGroups_HidesBulkDeleteToolbar(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedModifierGroup(t, db, "g1", "itm1", "Milk", false, 0, 1, 0, true)
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/modifiers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `id="modifiers-filter-unassigned"`) {
		t.Fatal("no unassigned groups: the filter/bulk-delete toolbar must not render")
	}
	if strings.Contains(body, `/api/catalog/modifier-group/delete-unassigned`) {
		t.Fatal("no unassigned groups: the bulk-delete button must not render")
	}
}

// The bulk action removes every unassigned group and leaves every assigned
// one — including its options and category link — completely untouched,
// and re-renders #modifiers-list (same dispatch as every other mutation
// here) with the HX-Trigger the sale screen's tile gate listens for.
func TestModifierGroupDeleteUnassigned_RemovesOnlyUnassigned(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedCategory(t, db, "cat1", "Drinks", true)
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "keep", "Sauces", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(t.Context(), "o-keep", "keep", "Extra", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(t.Context(), "cat1", "keep", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGroup(t.Context(), "orphan", "Once Used", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := postModifiers(t, mux, "/api/catalog/modifier-group/delete-unassigned", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete-unassigned: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "modifiers-changed" {
		t.Fatalf("delete-unassigned HX-Trigger = %q, want modifiers-changed", got)
	}
	body := rec.Body.String()
	if strings.Contains(body, `data-group-id="orphan"`) {
		t.Fatal("the unassigned group must not render in the re-rendered list")
	}
	if !strings.Contains(body, `data-group-id="keep"`) {
		t.Fatal("the assigned group must still render")
	}
	for _, tc := range []struct {
		q    string
		want int
	}{
		{`SELECT COUNT(*) FROM item_modifier_groups`, 1},
		{`SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'keep'`, 1},
		{`SELECT COUNT(*) FROM item_modifier_options WHERE group_id = 'keep'`, 1},
		{`SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id = 'keep'`, 1},
	} {
		var n int
		if err := db.QueryRow(tc.q).Scan(&n); err != nil || n != tc.want {
			t.Fatalf("%s = %d err=%v, want %d", tc.q, n, err, tc.want)
		}
	}

	// Nothing left unassigned: a second call is a harmless no-op, not an
	// error, and the survivor is still untouched.
	rec2 := postModifiers(t, mux, "/api/catalog/modifier-group/delete-unassigned", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("second delete-unassigned: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_groups`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("group rows after second call = %d err=%v, want 1", n, err)
	}
}

// Same replica gate as every other admin-synced modifier mutation
// (TestModifierGroupCategoryAndDelete_RefusedOnReplica).
func TestModifierGroupDeleteUnassigned_RefusedOnReplica(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "orphan", "Once Used", false, 0, 1, 0); err != nil {
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

	rec := postModifiers(t, mux, "/api/catalog/modifier-group/delete-unassigned", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete-unassigned on replica: want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_groups`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("group rows after refused mutation = %d err=%v, want 1 (untouched)", n, err)
	}
}
