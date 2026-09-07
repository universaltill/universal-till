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
	if err := db.QueryRow(`SELECT id FROM item_modifier_groups WHERE item_id = 'itm1'`).Scan(&groupID); err != nil {
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

	wantMsg := "manage customization options on the primary till"

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
	if err := db.QueryRow(`SELECT count(*) FROM item_modifier_groups WHERE item_id = 'itm1'`).Scan(&count); err != nil {
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
	if _, err := db.Exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order, is_active) VALUES ('grp-existing','itm1','Extras',0,0,2,0,1)`); err != nil {
		t.Fatalf("seed existing group: %v", err)
	}
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
