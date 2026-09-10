package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// GET /modifiers is now the full CRUD home for modifier groups (moved out
// of the per-item catalog panel, ut-docs#1957 — originally a read-only
// browse screen, ut-docs#1899) — this is the first place a merchant can
// manage every modifier group across the whole catalog without opening
// each item one at a time. The price is now a form field (converted to
// major units client-side by JS that doesn't run under go test), not
// server-rendered text, so this checks its minor-unit data attribute
// instead of a formatted "£0.50" string.
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
	for _, want := range []string{"Flat White", "Extras", "Extra shot", `data-minor="50"`, `id="modifiers-list"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q, got: %s", want, body)
		}
	}
	// The CRUD forms (not just names) must actually be present — this is
	// the whole point of #1957's move.
	if !strings.Contains(body, `name="minSelect"`) || !strings.Contains(body, "/api/catalog/modifier-option") {
		t.Errorf("expected the group/option CRUD forms to render on /modifiers, got: %s", body)
	}
}

// ut-docs#1914: the item name links back to /catalog with a ?item= deep
// link, since a merchant previously had to remember the item's name and
// find its row by hand — same "how do you get back there" gap #1899's own
// review flagged.
func TestModifiersPage_ItemNameLinksBackToCatalogItem(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "g1", "itm1", "Extras", true, 1, 2, 1); err != nil {
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
	if !strings.Contains(body, `href="/catalog?item=itm1"`) {
		t.Errorf("expected the item name to link back to /catalog?item=itm1, got: %s", body)
	}
	if !strings.Contains(body, `<a href="/catalog?item=itm1">Flat White</a>`) {
		t.Errorf("expected the item name itself to be the link text, got: %s", body)
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

// CRUD home (ut-docs#1957) — the opposite of the old read-only browse
// screen's contract (ut-docs#1899), which this test used to guard: hiding
// it here would leave no way to ever reactivate it, the same reason the
// per-item panel always used ListAllGroupsForItem instead of
// ListGroupsForItem. It must render marked inactive (vg-inactive), not
// indistinguishable from an active one.
func TestModifiersPage_ShowsInactiveGroupsForReactivation(t *testing.T) {
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
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Retired") {
		t.Fatal("expected the deactivated group to still be visible, so it can be reactivated")
	}
	if !strings.Contains(body, "vg-inactive") {
		t.Fatal("expected the deactivated group to be visually marked inactive")
	}
}

// ut-docs#2009: /modifiers is one of /catalog's four sub-pages (Import,
// Tax codes, Option sets, Modifiers). The other three already carry a
// persistent back-to-/catalog link in their page-head; this one only ever
// had a link inside the empty-state card, which disappears the instant a
// shop has any modifier group at all — the normal case in production, and
// exactly the "dead end, no way back except the main menu" the product
// owner reported. This asserts the persistent link exists regardless of
// whether the shop has any groups, matching tax_codes.html/option_sets.html's
// own `<a class="btn secondary" href="/catalog">` convention.
func TestModifiersPage_HasPersistentBackToCatalogLink(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "g1", "itm1", "Extras", true, 1, 2, 1); err != nil {
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
	headStart := strings.Index(body, `class="page-head"`)
	head := body[headStart:]
	// Bound the slice to the page-head <div>'s own close, not the rest of
	// the document — review finding, ut-docs#2009: an unbounded slice would
	// still pass today (the empty-state CTA's text differs from this exact
	// link), but wouldn't actually be checking "in the page-head" as the
	// test claims to.
	if end := strings.Index(head, "</div>"); end != -1 {
		head = head[:end]
	}
	backLink := `<a class="btn secondary" href="/catalog">← ` + httpx.T("en", "nav.catalog") + `</a>`
	if !strings.Contains(head, backLink) {
		t.Errorf("expected page-head to contain a persistent back-to-catalog link %q, got head region: %s", backLink, head)
	}
}
