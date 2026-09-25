package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// A 0-decimal currency (Iranian Rial/Toman, Iraqi Dinar, Afghan Afghani,
// Japanese Yen — all supported, see httpx.currencies) has no minor-unit
// subdivision at all: 1 major unit = 1 minor unit. A hardcoded *100
// conversion would inflate every price delta 100x for those shops.
func TestCatalogModifiersPanel_OptionPrice_RespectsZeroDecimalCurrency(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "IRR"}, Menu: []common.MenuItem{}})

	groupForm := "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(groupForm))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	var groupID string
	if err := db.QueryRow(`SELECT group_id FROM item_modifier_group_links WHERE item_id = 'itm1'`).Scan(&groupID); err != nil {
		t.Fatalf("read group id: %v", err)
	}

	optForm := "panelItem=itm1&itemId=itm1&groupId=" + groupID + "&name=Extra+shot&priceDeltaMajor=5000&isActive=1"
	req2 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-option", strings.NewReader(optForm))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("create option: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var delta int64
	if err := db.QueryRow(`SELECT price_delta_minor FROM item_modifier_options WHERE group_id = ?`, groupID).Scan(&delta); err != nil {
		t.Fatal(err)
	}
	if delta != 5000 {
		t.Fatalf("IRR has 0 decimals (1 major = 1 minor): want price_delta_minor 5000, got %d (looks like a hardcoded *100 was applied)", delta)
	}
}

// The Variants panel carries no modifier UI at all (ut-docs#2211 — the item
// editor's own Modifiers tab does; the CRUD itself lives on /modifiers since
// ut-docs#1957); creating a group still lands in the DB.
func TestCatalogModifiersPanel_CreateGroup(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// Variants panel: no Manage button, no CRUD forms (ut-docs#1957/#2211).
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("panel: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `id="manage-modifiers-btn"`) {
		t.Fatal("Variants panel must not carry a Manage Modifiers button any more (ut-docs#2211)")
	}
	if strings.Contains(rec.Body.String(), `name="minSelect"`) {
		t.Fatal("panel must not embed the group CRUD form directly any more (ut-docs#1957)")
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
	if err := db.QueryRow(`SELECT g.name, g.min_select, g.max_select FROM item_modifier_groups g JOIN item_modifier_group_links l ON l.group_id = g.id WHERE l.item_id = 'itm1'`).Scan(&name, &minSelect, &maxSelect); err != nil {
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
	if err := db.QueryRow(`SELECT g.required, g.min_select FROM item_modifier_groups g JOIN item_modifier_group_links l ON l.group_id = g.id WHERE l.item_id = 'itm1'`).Scan(&required, &minSelect); err != nil {
		t.Fatalf("read group: %v", err)
	}
	if required != 1 || minSelect < 1 {
		t.Fatalf("required group should have min_select >= 1, got required=%d min_select=%d", required, minSelect)
	}
}

// min_select/max_select have no natural ceiling in the DB CHECK constraint
// (min_select >= 0 AND max_select >= min_select), so an absurd value
// (e.g. 999999999) would otherwise be accepted and render a nonsensical
// "choose between 0 and 999999999" picker on the sale screen — ut-docs#2376.
func TestCatalogModifiersPanel_RejectsSelectAboveCap(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	for _, tc := range []struct {
		name string
		form string
	}{
		{"max_select above cap", "panelItem=itm1&itemId=itm1&name=TooMany&isActive=1&minSelect=0&maxSelect=51"},
		{"min_select above cap", "panelItem=itm1&itemId=itm1&name=TooMany&isActive=1&required=1&minSelect=51&maxSelect=51"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(tc.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_groups WHERE name = 'TooMany'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("no group must be created for a refused min/max, found %d", count)
	}
}

// The cap is inclusive — exactly maxModifierSelect must still be accepted,
// not just values below it.
func TestCatalogModifiersPanel_AcceptsSelectAtCap(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm1&itemId=itm1&name=AtCap&isActive=1&minSelect=0&maxSelect=50"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var maxSelect int
	if err := db.QueryRow(`SELECT max_select FROM item_modifier_groups WHERE name = 'AtCap'`).Scan(&maxSelect); err != nil {
		t.Fatal(err)
	}
	if maxSelect != 50 {
		t.Fatalf("want max_select=50, got %d", maxSelect)
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
	if err := db.QueryRow(`SELECT group_id FROM item_modifier_group_links WHERE item_id = 'itm1'`).Scan(&groupID); err != nil {
		t.Fatalf("read group id: %v", err)
	}

	// ut-docs#1957: the real form now sits inside the nested "Manage
	// customization groups" dialog (or /modifiers), so it submits with
	// Hx-Target naming ITS OWN container — asserting against that fragment
	// (not the old #catalog-variants one, which no longer carries option
	// names at all) is what actually exercises renderModifierMutationResult's
	// dispatch rather than just its no-header fallback.
	optForm := "panelItem=itm1&itemId=itm1&groupId=" + groupID + "&name=Extra+shot&priceDeltaMajor=0.50&isActive=1"
	req2 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-option", strings.NewReader(optForm))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("Hx-Target", "item-modifiers-list")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("create option: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), `id="item-modifiers-list"`) {
		t.Fatal("expected the item-scoped modal fragment, not the old #catalog-variants panel")
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

	// Update: deactivate it, submitting the form EXACTLY the way a real
	// browser does for an unchecked checkbox — the "Active" box has a
	// hidden isActive=0 fallback ahead of it in the DOM (see
	// catalog_variants.html), so unchecking it means only that hidden "0"
	// is submitted, never an explicit "isActive=0" the caller typed
	// themselves. A version of this test that hand-writes "isActive=0"
	// would pass even if the real form were broken (no hidden fallback,
	// or Form.Get picking the wrong value out of a multi-value field) —
	// see TestFormCheckboxActive_MatchesRealBrowserSubmission below for
	// that failure mode isolated directly against the parsing helper.
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

	// Re-activate, submitting BOTH values the way a real browser does for
	// a CHECKED box: the hidden isActive=0 fallback AND the checkbox's
	// "1", in DOM order (hidden first, checkbox second) — this is the
	// case that would silently fail if the handler read only the first
	// submitted value instead of scanning for "1" among all of them.
	reactivateForm := "panelItem=itm1&itemId=itm1&groupId=" + groupID + "&id=" + optID + "&name=Extra+shot&priceDeltaMajor=0.50&isActive=0&isActive=1"
	req4 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-option", strings.NewReader(reactivateForm))
	req4.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec4 := httptest.NewRecorder()
	mux.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("reactivate option: want 200, got %d: %s", rec4.Code, rec4.Body.String())
	}
	if err := db.QueryRow(`SELECT is_active FROM item_modifier_options WHERE id = ?`, optID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("want option reactivated (real-browser checked-checkbox submission: hidden 0 AND checkbox 1 both present), is_active=%d", active)
	}
}

// A deactivated group/option must still show in the ADMIN surfaces (so a
// manager can reactivate it) even though it's hidden from the sale-time
// picker — this is the whole reason ListAllGroupsForItem exists, distinct
// from ListGroupsForItem. ut-docs#1957 moved that admin surface off the
// Variants panel onto the item editor's Modifiers tab (a nested dialog
// until ut-docs#2211) — GET /api/catalog/modifier-groups-panel.
func TestCatalogModifiersPanel_ShowsDeactivatedGroupsForReactivation(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedModifierGroup(t, db, "g1", "itm1", "Retired", false, 0, 1, 1, false)

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// The Variants panel names no groups at all (ut-docs#2211).
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Retired") {
		t.Fatal("the Variants panel must not list a modifier group by name")
	}

	// The Modifiers tab's own fragment lists it.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/catalog/modifier-groups-panel?item_id=itm1", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Retired") {
		t.Fatal("the Modifiers tab must show a deactivated group so it can be reactivated")
	}
	if !strings.Contains(rec2.Body.String(), `id="item-modifiers-list"`) {
		t.Fatal("expected the Modifiers tab fragment container")
	}
}

// ut-docs#1667: item_modifier_groups/item_modifier_options are now synced
// shop-wide (sync_admin_repo.go's adminTables), so a write accepted on a
// satellite would silently vanish on the next admin pull — both mutation
// endpoints must refuse up front instead, same pattern as
// TestRegistersPage_MutationsRefusedOnReplica.
func TestCatalogModifiersPanel_MutationsRefusedOnReplica(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})

	// NewCatalogTestDB's schema deliberately omits tables this specific
	// handler group doesn't otherwise need — settings included. Real schema
	// mirrored from internal/db/migrations/001_init.sql, same convention as
	// this package's own TestCatalogReplicaBannerNeverLinksAcrossDevices.
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}, Settings: st})

	wantMsg := "manage Modifiers on the primary till" // ut-docs#2211 rename

	groupForm := "panelItem=itm1&itemId=itm1&name=Extras&isActive=1&minSelect=0&maxSelect=2"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(groupForm))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("create group on replica: want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), wantMsg) {
		t.Fatalf("create group on replica: body missing the localized replica_use_primary message, got %q", rec.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_group_links WHERE item_id = 'itm1'`).Scan(&count); err != nil {
		t.Fatalf("count groups: %v", err)
	}
	if count != 0 {
		t.Fatalf("modifier group must not be created on a replica, found %d rows", count)
	}

	// A group and option that existed before this till became a replica
	// (e.g. synced down from the primary) must also refuse an UPDATE
	// against them, not just create — same shape as
	// TestRegistersPage_MutationsRefusedOnReplica's rename/deactivate cases
	// on a pre-existing register. This is the more likely real replica
	// action (editing an already-synced modifier), and the exact gap a
	// future "editing an existing row is fine" refactor could reopen
	// silently if only create were covered.
	testsupport.SeedModifierGroup(t, db, "grp-existing", "itm1", "Extras", false, 0, 2, 0, true)
	if _, err := db.Exec(`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order, is_active) VALUES ('opt-existing','grp-existing','Extra shot',50,0,1)`); err != nil {
		t.Fatalf("seed existing option: %v", err)
	}

	updateGroupForm := "panelItem=itm1&itemId=itm1&id=grp-existing&name=Renamed&isActive=1&minSelect=0&maxSelect=2"
	req2 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group", strings.NewReader(updateGroupForm))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("update group on replica: want 409, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), wantMsg) {
		t.Fatalf("update group on replica: body missing the localized replica_use_primary message, got %q", rec2.Body.String())
	}
	var groupName string
	if err := db.QueryRow(`SELECT name FROM item_modifier_groups WHERE id = 'grp-existing'`).Scan(&groupName); err != nil || groupName != "Extras" {
		t.Fatalf("modifier group must not be renamed on a replica: name=%q err=%v", groupName, err)
	}

	createOptForm := "panelItem=itm1&itemId=itm1&groupId=grp-existing&name=Extra+shot&priceDeltaMajor=0.50&isActive=1"
	req3 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-option", strings.NewReader(createOptForm))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusConflict {
		t.Fatalf("create option on replica: want 409, got %d: %s", rec3.Code, rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), wantMsg) {
		t.Fatalf("create option on replica: body missing the localized replica_use_primary message, got %q", rec3.Body.String())
	}
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_options WHERE group_id = 'grp-existing'`).Scan(&count); err != nil {
		t.Fatalf("count options: %v", err)
	}
	if count != 1 {
		t.Fatalf("modifier option must not be created on a replica, found %d rows (want only the pre-existing one)", count)
	}

	updateOptForm := "panelItem=itm1&itemId=itm1&groupId=grp-existing&id=opt-existing&name=Renamed&priceDeltaMajor=0.50&isActive=1"
	req4 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-option", strings.NewReader(updateOptForm))
	req4.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec4 := httptest.NewRecorder()
	mux.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusConflict {
		t.Fatalf("update option on replica: want 409, got %d: %s", rec4.Code, rec4.Body.String())
	}
	if !strings.Contains(rec4.Body.String(), wantMsg) {
		t.Fatalf("update option on replica: body missing the localized replica_use_primary message, got %q", rec4.Body.String())
	}
	var optName string
	if err := db.QueryRow(`SELECT name FROM item_modifier_options WHERE id = 'opt-existing'`).Scan(&optName); err != nil || optName != "Extra shot" {
		t.Fatalf("modifier option must not be renamed on a replica: name=%q err=%v", optName, err)
	}
}

// Attaching an existing group to a second item (ut-docs#2046 / ADR-0090 §5):
// no new group row is created, LinkGroupToItem's link row is added, and the
// item-scoped fragment reflects it immediately.
func TestModifierGroupAttach_LinksExistingGroupToSecondItem(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-a", SKU: "SKU-A", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-b", SKU: "SKU-B", Name: "Latte", BasePrice: 350, IsActive: true})
	testsupport.SeedModifierGroup(t, db, "g-milk", "itm-a", "Milk", false, 0, 1, 0, true)

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm-b&itemId=itm-b&groupId=g-milk"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/attach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Hx-Target", "item-modifiers-list")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Milk") {
		t.Fatal("expected itm-b's fragment to now show the attached Milk group")
	}

	var groupCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_groups WHERE id = 'g-milk'`).Scan(&groupCount); err != nil || groupCount != 1 {
		t.Fatalf("attach must not create a second group row: count=%d err=%v", groupCount, err)
	}
	var linkCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&linkCount); err != nil || linkCount != 2 {
		t.Fatalf("expected 2 links (itm-a original + itm-b attach): count=%d err=%v", linkCount, err)
	}
}

func TestModifierGroupAttach_RequiresItemAndGroupID(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/attach", strings.NewReader("itemId=itm-b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing groupId: want 400, got %d", rec.Code)
	}
}

// Detaching removes only the one item's link — the group and its other
// links (and options) survive untouched (ut-docs#2046).
func TestModifierGroupDetach_RemovesOnlyThisItemsLink(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-a", SKU: "SKU-A", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-b", SKU: "SKU-B", Name: "Latte", BasePrice: 350, IsActive: true})
	testsupport.SeedModifierGroup(t, db, "g-milk", "itm-a", "Milk", false, 0, 1, 0, true)
	if _, err := db.Exec(`INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES ('itm-b','g-milk',0)`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm-b&itemId=itm-b&groupId=g-milk"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/detach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Hx-Target", "item-modifiers-list")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Milk is no longer one of itm-b's OWN linked groups (those render as a
	// read-only <span> name, ut-docs#2330) — but it legitimately reappears
	// as an attach-picker checkbox, since it's now unlinked from itm-b and
	// still active elsewhere (itm-a).
	if strings.Contains(rec.Body.String(), `class="modifier-admin-group-name">Milk<`) {
		t.Fatal("itm-b's fragment must no longer show the detached group as one of its own linked groups")
	}
	if !strings.Contains(rec.Body.String(), `<input type="checkbox" name="groupId" value="g-milk">`) {
		t.Fatal("expected the just-detached group to reappear as a checkbox in itm-b's attach picker")
	}

	var linkCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&linkCount); err != nil || linkCount != 1 {
		t.Fatalf("expected exactly 1 remaining link (itm-a's), got %d (err=%v)", linkCount, err)
	}
	var stillLinkedTo string
	if err := db.QueryRow(`SELECT item_id FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&stillLinkedTo); err != nil || stillLinkedTo != "itm-a" {
		t.Fatalf("itm-a's link must survive: got %q (err=%v)", stillLinkedTo, err)
	}
	var groupCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_groups WHERE id = 'g-milk'`).Scan(&groupCount); err != nil || groupCount != 1 {
		t.Fatalf("detach must never delete the group row: count=%d err=%v", groupCount, err)
	}
}

// Detaching a group's LAST remaining link is allowed since ADR-0101
// (ut-docs#2399): the group becomes unassigned — its row and options
// survive, it stays listed on /modifiers (which reads the group table, not
// the links), and it is simply not offered at checkout. The ut-docs#2046
// refusal existed only because the old per-item listing could not show an
// orphan.
func TestModifierGroupDetach_LastLinkLeavesGroupUnassigned(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-a", SKU: "SKU-A", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedModifierGroup(t, db, "g-milk", "itm-a", "Milk", false, 0, 1, 0, true)
	if _, err := db.Exec(`INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order, is_active) VALUES ('o-oat','g-milk','Oat',40,1,1)`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm-a&itemId=itm-a&groupId=g-milk"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/detach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Hx-Target", "item-modifiers-list")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "only item using this group") {
		t.Fatal("the last-link refusal notice must be gone (ADR-0101)")
	}
	// Milk is now unlinked but still active, so it legitimately reappears
	// in itm-a's attach picker.
	if !strings.Contains(rec.Body.String(), `<input type="checkbox" name="groupId" value="g-milk">`) {
		t.Fatal("expected the just-detached group to reappear as a checkbox in itm-a's attach picker")
	}
	var linkCount, groupCount, optionCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&linkCount); err != nil || linkCount != 0 {
		t.Fatalf("the last link must be gone: count=%d err=%v", linkCount, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_groups WHERE id = 'g-milk'`).Scan(&groupCount); err != nil || groupCount != 1 {
		t.Fatalf("detach must never delete the group row: count=%d err=%v", groupCount, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_options WHERE group_id = 'g-milk'`).Scan(&optionCount); err != nil || optionCount != 1 {
		t.Fatalf("detach must never delete the group's options: count=%d err=%v", optionCount, err)
	}

	// And /modifiers still lists it — as unassigned.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/modifiers", nil))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `data-group-id="g-milk"`) {
		t.Fatalf("/modifiers must still list an unassigned group: %d %s", rec2.Code, rec2.Body.String())
	}
}

// A stale attach-picker — open since before another tab deactivated the
// group, or attached it to this same item — must not be able to attach an
// inactive group, or silently re-order an already-linked one via
// LinkGroupToItem's own ON CONFLICT DO UPDATE (independent-review finding).
func TestModifierGroupAttach_RefusesStalePickerSubmission(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-a", SKU: "SKU-A", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-b", SKU: "SKU-B", Name: "Latte", BasePrice: 350, IsActive: true})
	testsupport.SeedModifierGroup(t, db, "g-retired", "itm-a", "Retired", false, 0, 1, 0, false) // inactive

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	form := "panelItem=itm-b&itemId=itm-b&groupId=g-retired"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/attach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("attaching an inactive group: want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var linkCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_group_links WHERE item_id = 'itm-b'`).Scan(&linkCount); err != nil || linkCount != 0 {
		t.Fatalf("an inactive group must never be attached, found %d link(s)", linkCount)
	}

	// Also refuse a group already linked to this item — a resubmit from a
	// stale picker must not silently re-order the existing link.
	form2 := "panelItem=itm-a&itemId=itm-a&groupId=g-retired"
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-c", SKU: "SKU-C", Name: "Mocha", BasePrice: 380, IsActive: true})
	if _, err := db.Exec(`UPDATE item_modifier_groups SET is_active = 1 WHERE id = 'g-retired'`); err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/attach", strings.NewReader(form2))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("re-attaching an already-linked group: want 409, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var sortOrder int
	if err := db.QueryRow(`SELECT sort_order FROM item_modifier_group_links WHERE item_id = 'itm-a' AND group_id = 'g-retired'`).Scan(&sortOrder); err != nil || sortOrder != 0 {
		t.Fatalf("the existing link's sort_order must not be silently changed, got %d (err=%v)", sortOrder, err)
	}
}

// Attach/detach are catalog mutations synced shop-wide (ut-docs#1667), same
// as create/update — both must refuse on a replica, same convention as
// TestCatalogModifiersPanel_MutationsRefusedOnReplica.
func TestModifierGroupAttachDetach_RefusedOnReplica(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-a", SKU: "SKU-A", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm-b", SKU: "SKU-B", Name: "Latte", BasePrice: 350, IsActive: true})
	testsupport.SeedModifierGroup(t, db, "g-milk", "itm-a", "Milk", false, 0, 1, 0, true)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}, Settings: st})

	attachForm := "panelItem=itm-b&itemId=itm-b&groupId=g-milk"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/attach", strings.NewReader(attachForm))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("attach on replica: want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var linkCount int
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&linkCount); err != nil || linkCount != 1 {
		t.Fatalf("attach must not link on a replica: count=%d err=%v", linkCount, err)
	}

	detachForm := "panelItem=itm-a&itemId=itm-a&groupId=g-milk"
	req2 := httptest.NewRequest(http.MethodPost, "/api/catalog/modifier-group/detach", strings.NewReader(detachForm))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("detach on replica: want 409, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&linkCount); err != nil || linkCount != 1 {
		t.Fatalf("detach must not unlink on a replica: count=%d err=%v", linkCount, err)
	}
}

// The parsing helper itself, in isolation, against every shape a real
// browser submission of a hidden-fallback-plus-checkbox pair can take.
func TestFormCheckboxActive_MatchesRealBrowserSubmission(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"unchecked: only the hidden 0 is submitted", "isActive=0", false},
		{"checked: hidden 0 then checkbox 1, in DOM order", "isActive=0&isActive=1", true},
		{"new-row form: only a hidden 1, no pairing", "isActive=1", true},
		{"field missing entirely (no hidden-fallback pattern used)", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := formCheckboxActive(req); got != tc.want {
				t.Errorf("formCheckboxActive(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
