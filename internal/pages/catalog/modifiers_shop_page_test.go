package catalog

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// GET /modifiers is the shop-wide home of the modifier group itself
// (ADR-0101, ut-docs#2399): one card per group — active or not, assigned
// or not — with its CRUD form, its options and an "Assigned to" block
// naming the categories and items it is linked to. Before this it listed
// groups UNDER each item (a group shared by three items was three cards),
// which is exactly the "a modifier can only exist attached to a catalog
// item" premise the product owner reported and ADR-0101 withdraws. The
// price is a form field (converted to major units client-side by JS that
// doesn't run under go test), so this checks its minor-unit data attribute
// instead of a formatted "£0.50" string.
func TestModifiersPage_ListsEveryGroupOnceWithAssignments(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "TEA", Name: "Latte", BasePrice: 350, IsActive: true})
	testsupport.SeedCategory(t, db, "cat1", "Drinks", true)
	testsupport.SeedCategory(t, db, "cat2", "Food", true)

	repo := data.NewModifierRepo(db)
	gid, err := repo.CreateGroup(t.Context(), "g1", "Extras", true, 1, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(t.Context(), "o1", gid, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"itm1", "itm2"} {
		if err := repo.LinkGroupToItem(t.Context(), item, gid, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.LinkGroupToCategory(t.Context(), "cat1", gid, 0); err != nil {
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
	if n := strings.Count(body, `data-group-id="g1"`); n != 1 {
		t.Fatalf("a group linked to two items must render exactly ONE card, got %d: %s", n, body)
	}
	for _, want := range []string{
		"Extras", "Extra shot", `data-minor="50"`, `id="modifiers-list"`,
		// The CRUD forms (not just names) must actually be present.
		`name="minSelect"`, "/api/catalog/modifier-option",
		// Delete-everywhere, behind a confirm.
		`hx-post="/api/catalog/modifier-group/delete"`, `hx-confirm=`,
		// Assigned items, each a chip with a remove control posting to detach.
		`data-item-id="itm1"`, `data-item-id="itm2"`, "Flat White", "Latte",
		`hx-post="/api/catalog/modifier-group/detach"`,
		// Every category as a checkbox; the linked one checked.
		`name="categoryId" value="cat1" checked`, `name="categoryId" value="cat2"`,
		`hx-post="/api/catalog/modifier-group/attach-category"`,
		// The per-card "add item" search resolves against the shared datalist.
		`list="modifiers-items-list"`, `hx-post="/api/catalog/modifier-group/attach"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q, got: %s", want, body)
		}
	}
	if strings.Contains(body, `name="categoryId" value="cat2" checked`) {
		t.Error("the unlinked category must render unchecked")
	}
	// The old per-item header is gone: nothing groups cards by item.
	if strings.Contains(body, `<h3 style="margin:.2rem 0"><a href="/catalog?item=`) {
		t.Error("the per-item <h3> header of the pre-ADR-0101 layout must not render")
	}
}

// The create form at the top needs a name and rules only — no item picker
// (ADR-0101 Decision 2). The item search input lives inside each group's
// own "Add item" row instead.
func TestModifiersPage_CreateFormHasNoItemPicker(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/modifiers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	start := strings.Index(body, `modifiers-new-group-card`)
	if start == -1 {
		t.Fatalf("create card missing: %s", body)
	}
	form := body[start:]
	form = form[:strings.Index(form, "</form>")]
	if !strings.Contains(form, `hx-post="/api/catalog/modifier-group"`) || !strings.Contains(form, `name="name"`) {
		t.Fatalf("create form must post a name to /api/catalog/modifier-group: %s", form)
	}
	if strings.Contains(form, `name="itemId"`) || strings.Contains(form, `list="modifiers-items-list"`) {
		t.Fatalf("the create form must not carry an item picker (ADR-0101): %s", form)
	}
}

// A group with no assignment is a valid, listed state — shown with one
// muted line saying it won't be offered at checkout until it is assigned.
func TestModifiersPage_UnassignedGroupShowsHint(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "g-alone", "Sauces", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/modifiers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-group-id="g-alone"`) || !strings.Contains(body, "Sauces") {
		t.Fatalf("an unassigned group must still be listed: %s", body)
	}
	// html/template escapes the apostrophe in "won't" as &#39;, so compare
	// against the escaped form of the translated string.
	hint := html.EscapeString(httpx.T("en", "modifiers.unassigned"))
	if !strings.Contains(body, hint) {
		t.Fatalf("expected the unassigned hint %q: %s", hint, body)
	}
	if strings.Contains(body, "No Modifiers yet") {
		t.Fatal("the empty state must not show when a group exists, even unassigned")
	}
}

// ut-docs#1914 kept: an assigned item's chip links back to /catalog with
// its detail panel pre-opened (openCatalogRow's ?item= deep link), so the
// merchant never has to go find the item by hand.
func TestModifiersPage_AssignedItemLinksBackToCatalogItem(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedModifierGroup(t, db, "g1", "itm1", "Extras", true, 1, 2, 1, true)

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/modifiers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// ut-docs#2211: lands on the item editor's Modifiers tab.
	if !strings.Contains(rec.Body.String(), `<a href="/catalog?item=itm1&tab=modifiers">Flat White</a>`) {
		t.Errorf("expected the assigned item chip to link to /catalog?item=itm1&tab=modifiers, got: %s", rec.Body.String())
	}
}

// ut-docs#2211: the rail entry (uislot.CoreItems, "items.modifiers.name")
// and the page you actually land on used to say two different things —
// "Modifiers" in the rail, "Customization options" on the page itself.
// Pins the page's own <h1> against drifting away from the rail label again.
func TestModifiersPage_HeadingMatchesRailLabel(t *testing.T) {
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
	if !strings.Contains(rec.Body.String(), "<h1>Modifiers</h1>") {
		t.Errorf("expected the page heading to say \"Modifiers\" (matching the rail label), got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Customization options") {
		t.Errorf("the old \"Customization options\" wording must not regress, got: %s", rec.Body.String())
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
	// ut-docs#2211: renamed from "No customization groups" so the empty
	// state matches the feature's actual name everywhere else (the rail
	// label, the page heading).
	if !strings.Contains(rec.Body.String(), "No Modifiers") {
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

	repo := data.NewModifierRepo(db)
	gid, err := repo.CreateGroup(t.Context(), "g1", "Retired", false, 0, 1, 1)
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
// own `<a class="btn secondary" href="/items">` convention (ut-docs#2090:
// retargeted from bare /catalog to /items — bare /catalog renders
// standalone with no /items rail, so it used to trade one railless page
// for another; /items is the shell itself, default section Catalog).
func TestModifiersPage_HasPersistentBackToCatalogLink(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	repo := data.NewModifierRepo(db)
	if _, err := repo.CreateGroup(t.Context(), "g1", "Extras", true, 1, 2, 1); err != nil {
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
	backLink := `<a class="btn secondary" href="/items">← ` + httpx.T("en", "nav.catalog") + `</a>`
	if !strings.Contains(head, backLink) {
		t.Errorf("expected page-head to contain a persistent back-to-catalog link %q, got head region: %s", backLink, head)
	}
}
