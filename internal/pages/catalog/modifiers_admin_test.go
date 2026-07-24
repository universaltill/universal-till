package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// The item-detail panel shows the modifiers admin section (ADR-0020), and
// creating a group is reflected both in the panel and directly in the DB.
func TestCatalogModifiersPanel_CreateGroup(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// Panel loads with the "no customization options yet" hint and an
	// add-group form.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("panel: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/api/catalog/modifier-group") {
		t.Fatal("panel missing the add-group form")
	}

	form := "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2&sortOrder=1"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("create group: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Extras") {
		t.Fatal("panel response missing the newly created group")
	}

	var name string
	var minSelect, maxSelect int
	if err := db.QueryRow(`SELECT name, min_select, max_select FROM item_modifier_groups WHERE item_id = 'itm1'`).Scan(&name, &minSelect, &maxSelect); err != nil {
		t.Fatalf("read group: %v", err)
	}
	if name != "Extras" || minSelect != 0 || maxSelect != 2 {
		t.Fatalf("group not created correctly: name=%q min=%d max=%d", name, minSelect, maxSelect)
	}
}

// A required group with no explicit min_select must be stored with
// min_select >= 1 — a "required" group that lets zero selections through
// would be a contradiction the sale-time picker can't sensibly enforce.
func TestCatalogModifiersPanel_RequiredGroupForcesMinSelectAtLeastOne(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm1&itemId=itm1&name=Size&required=1&isActive=1&minSelect=0&maxSelect=1"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var required, minSelect int
	if err := db.QueryRow(`SELECT required, min_select FROM item_modifier_groups WHERE item_id = 'itm1'`).Scan(&required, &minSelect); err != nil {
		t.Fatalf("read group: %v", err)
	}
	if required != 1 || minSelect < 1 {
		t.Fatalf("required group should have min_select >= 1, got required=%d min_select=%d", required, minSelect)
	}
}

// Creating and editing an option: price is entered in major units (the
// shop's display currency) and stored as minor units.
func TestCatalogModifiersPanel_CreateAndUpdateOption(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	groupForm := "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(groupForm))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var groupID string
	if err := db.QueryRow(`SELECT id FROM item_modifier_groups WHERE item_id = 'itm1'`).Scan(&groupID); err != nil {
		t.Fatalf("read group id: %v", err)
	}

	optForm := "panelItem=itm1&itemId=itm1&groupId=" + groupID + "&name=Extra+shot&priceDeltaMajor=0.50&isActive=1"
	req2 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-option", strings.NewReader(optForm))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("create option: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Extra shot") {
		t.Fatal("panel response missing the newly created option")
	}

	var optID string
	var delta int64
	if err := db.QueryRow(`SELECT id, price_delta_minor FROM item_modifier_options WHERE group_id = ?`, groupID).Scan(&optID, &delta); err != nil {
		t.Fatalf("read option: %v", err)
	}
	if delta != 50 {
		t.Fatalf("want price_delta_minor 50 (0.50 major), got %d", delta)
	}

	// Update: deactivate it (soft-deactivate convention, same as items/variants).
	updateForm := "panelItem=itm1&itemId=itm1&groupId=" + groupID + "&id=" + optID + "&name=Extra+shot&priceDeltaMajor=0.50&isActive=0"
	req3 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-option", strings.NewReader(updateForm))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("deactivate option: want 200, got %d: %s", rec3.Code, rec3.Body.String())
	}
	var active int
	if err := db.QueryRow(`SELECT is_active FROM item_modifier_options WHERE id = ?`, optID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("want option deactivated, is_active=%d", active)
	}
}

// A deactivated group/option must still show in the ADMIN panel (so a
// manager can reactivate it) even though it's hidden from the sale-time
// picker — this is the whole reason ListAllGroupsForItem exists, distinct
// from ListGroupsForItem.
func TestCatalogModifiersPanel_ShowsDeactivatedGroupsForReactivation(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order, is_active) VALUES ('g1','itm1','Retired',0,0,1,1,0)`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Retired") {
		t.Fatal("admin panel must show a deactivated group so it can be reactivated")
	}
}
