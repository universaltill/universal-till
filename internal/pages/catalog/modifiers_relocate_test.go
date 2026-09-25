package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#1957: /api/catalog/modifier-group and /api/catalog/modifier-option
// share the exact same create/update logic regardless of caller — only the
// POST-mutation re-render target is context-aware, picked by reading back
// the mutating request's own Hx-Target header (set by whichever form
// submitted it — see modifier_group_admin.html). A request whose Hx-Target
// names the /modifiers page's own container gets the shop-wide fragment
// back, not the old per-item #catalog-variants panel and not the nested
// dialog's item-scoped one.
func TestModifierGroupHandler_HxTargetModifiersList_RendersShopWideFragment(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Hx-Target", "modifiers-list")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="modifiers-list"`) {
		t.Fatal("expected the shop-wide #modifiers-list fragment")
	}
	if strings.Contains(body, `id="catalog-variants"`) {
		t.Fatal("must not fall back to the old #catalog-variants panel when Hx-Target names modifiers-list")
	}
	if strings.Contains(body, `id="item-modifiers-list"`) {
		t.Fatal("must not answer with the item-scoped Modifiers-tab fragment when Hx-Target names modifiers-list")
	}
	if !strings.Contains(body, "Extras") {
		t.Fatal("expected the newly created group to appear in the shop-wide fragment")
	}
}

// The item-scoped Modifiers-tab fragment (#item-modifiers-list) must
// show ONLY the mutated item's own groups — not the whole shop's — proving
// this is genuinely a separate, scoped re-render path and not an accidental
// alias for the shop-wide one.
func TestModifierGroupHandler_HxTargetModifierGroupsModalList_ScopedToOneItem(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "TEA", Name: "Latte", BasePrice: 350, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// Seed itm2 with its own group first — it must NOT leak into itm1's
	// scoped fragment below.
	otherForm := "panelItem=itm2&itemId=itm2&name=Milk&isActive=1&minSelect=0&maxSelect=1"
	otherReq := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(otherForm))
	otherReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), otherReq)

	form := "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Hx-Target", "item-modifiers-list")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="item-modifiers-list"`) {
		t.Fatal("expected the item-scoped Modifiers-tab fragment")
	}
	if !strings.Contains(body, "Extras") {
		t.Fatal("expected itm1's own newly created group")
	}
	// ut-docs#2046 / ut-docs#2330: the fragment stays scoped to itm1's OWN
	// linked groups — itm2's "Milk" must never appear as one of itm1's own
	// rows (those render the group's name as a read-only <span>) — but it
	// now legitimately appears as an attach-picker checkbox, since that
	// picker's whole job is offering every OTHER active shop group not yet
	// linked to this item.
	if strings.Contains(body, `class="modifier-admin-group-name">Milk<`) {
		t.Fatal("must not include itm2's group as one of itm1's own linked groups")
	}
	if !strings.Contains(body, `class="modifier-attach-option"><input type="checkbox" name="groupId" value=`) || !strings.Contains(body, " Milk</label>") {
		t.Fatal("expected itm2's group to be offered as a checkbox in the attach-existing picker")
	}
}

// GET /api/catalog/modifier-groups-panel is the Modifiers tab's own
// lazy-load endpoint (opened from catalog_variants.html's "Manage
// customization groups" button) — it must answer the same item-scoped
// fragment shape a mutation targeting #item-modifiers-list does.
func TestModifierGroupsPanel_GET_RendersItemScopedFragment(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/modifier-groups-panel?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="item-modifiers-list"`) {
		t.Fatal("expected the item-scoped Modifiers-tab fragment container")
	}
	if !strings.Contains(body, "Extras") {
		t.Fatal("expected itm1's group to appear")
	}
}

// The item editor's Modifiers tab (ut-docs#2211; a nested dialog's
// compact summary before that) lists every one of the item's groups, and
// never uses page navigation (kiosk chromeless build, see base.html).
func TestItemModifiersTab_ListsEveryGroupNoNavigation(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	for _, form := range []string{
		"panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2",
		"panelItem=itm1&itemId=itm1&name=Milk+type&isActive=1&minSelect=0&maxSelect=1",
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/modifier-groups-panel?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, name := range []string{"Extras", "Milk type"} {
		if !strings.Contains(body, `class="modifier-admin-group-name">`+name+`<`) {
			t.Errorf("Modifiers tab must list the item's group %q", name)
		}
	}
	if strings.Contains(body, `target="_blank"`) || strings.Contains(body, "window.open") {
		t.Fatal("the Modifiers tab must never use page navigation (kiosk is chromeless)")
	}
}
