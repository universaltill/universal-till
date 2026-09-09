package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// GET /modifiers (ut-docs#1899) is the shop-wide browse screen the item
// detail panel's per-item modifier admin has no equivalent of today — this
// is the first place a merchant can see every modifier group across the
// whole catalog without opening each item one at a time.
func TestModifiersPage_ListsGroupsAcrossItemsWithItemName(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "TEA", Name: "Latte", BasePrice: 350, IsActive: true})

	repo := data.NewModifierRepo(db)
	gid, err := repo.CreateGroup(t.Context(), "g1", "itm1", "Extras", true, 1, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(t.Context(), "o1", gid, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/modifiers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Flat White", "Extras", "Extra shot", "£0.50"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q, got: %s", want, body)
		}
	}
}

func TestModifiersPage_EmptyShopShowsEmptyState(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/modifiers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "No customization groups") {
		t.Errorf("expected an empty-state message, got: %s", rec.Body.String())
	}
}

// A deactivated group must not show up on the shop-wide browse screen
// either — same visibility contract as everywhere else modifiers render.
func TestModifiersPage_SkipsInactiveGroups(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	repo := data.NewModifierRepo(db)
	gid, err := repo.CreateGroup(t.Context(), "g1", "itm1", "Retired", false, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateGroup(t.Context(), gid, "Retired", false, 0, 1, 1, false); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/modifiers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "Retired") {
		t.Errorf("expected the deactivated group to be absent, got: %s", rec.Body.String())
	}
}
