package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2614: POST /api/buttons/unhide-all -- the Designer's "Show all N
// on the sell screen" -- copies /api/buttons/unhide's gating exactly
// (requirePrimary + checkOrElevate("catalog_management")).

// seedTwoHidden adds two active hidden items plus one inactive hidden item
// (which unhide-all must leave alone).
func seedTwoHidden(t *testing.T, d *common.Deps) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO items(id,sku,name,base_price,is_active,sell_screen_hidden) VALUES ('itm-h1','H1-SKU','Hidden One',100,1,1)`,
		`INSERT INTO items(id,sku,name,base_price,is_active,sell_screen_hidden) VALUES ('itm-h2','H2-SKU','Hidden Two',100,1,1)`,
		`INSERT INTO items(id,sku,name,base_price,is_active,sell_screen_hidden) VALUES ('itm-h3','H3-SKU','Hidden Inactive',100,0,1)`,
	} {
		if _, err := d.Db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
}

func countHidden(t *testing.T, d *common.Deps) int {
	t.Helper()
	var n int
	if err := d.Db.QueryRow(`SELECT count(*) FROM items WHERE sell_screen_hidden = 1 AND is_active = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestButtonsAPI_UnhideAll_CatalogManagementGate(t *testing.T) {
	cashier := auth.User{ID: "c1", Role: "cashier"}
	mux, d := newButtonsMuxRealSession(t)
	seedTwoHidden(t, d)
	rec := postForm(mux, "/api/buttons/unhide-all", url.Values{}, &cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("cashier unhide-all: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Show every hidden item on the sell screen again.") {
		t.Fatalf("cashier unhide-all: expected the buttons_unhide_all elevation summary, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/api/buttons/unhide-all") {
		t.Fatalf("cashier unhide-all: elevation retry must target /api/buttons/unhide-all, got: %s", rec.Body.String())
	}
	if n := countHidden(t, d); n != 2 {
		t.Fatalf("cashier unhide-all: items must stay hidden, got %d hidden", n)
	}

	for _, role := range []string{"manager", "admin", "super_admin"} {
		mux, d := newButtonsMuxRealSession(t)
		seedTwoHidden(t, d)
		mgr := auth.User{ID: "u-" + role, Role: role}
		rec := postForm(mux, "/api/buttons/unhide-all", url.Values{}, &mgr)
		if isElevationPrompt(rec) {
			t.Fatalf("%s unhide-all: got elevation prompt: %d %s", role, rec.Code, rec.Body.String())
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s unhide-all: code=%d, want 200: %s", role, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
			t.Fatalf("%s unhide-all: HX-Trigger = %q, want buttons-changed", role, got)
		}
		if n := countHidden(t, d); n != 0 {
			t.Fatalf("%s unhide-all: expected every active item unhidden, %d still hidden", role, n)
		}
		var inactive int
		if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm-h3'`).Scan(&inactive); err != nil || inactive != 1 {
			t.Fatalf("%s unhide-all: inactive item must stay hidden, got %d err=%v", role, inactive, err)
		}
		var rows int
		if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE item_id IN ('itm-h1','itm-h2')`).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("%s unhide-all: must not create shortcut_buttons rows, got %d err=%v", role, rows, err)
		}
	}
}

// An empty hidden set is a successful no-op, not an error.
func TestButtonsAPI_UnhideAll_NoneHiddenIs200(t *testing.T) {
	mux, _ := newButtonsMuxRealSession(t)
	mgr := auth.User{ID: "u-manager", Role: "manager"}
	rec := postForm(mux, "/api/buttons/unhide-all", url.Values{}, &mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("unhide-all with nothing hidden: code=%d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestButtonsAPI_UnhideAll_ElevatedPINWritesAuditRow(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedTwoHidden(t, d)
	mgrID, blockedID := newElevationTestPrincipals(t, d, "mgr-btn-unhide-all", "blocked-cashier-btn-unhide-all", "445566")

	rec := postForm(mux, "/api/buttons/unhide-all", url.Values{"override_pin": {"445566"}}, &auth.User{ID: blockedID, Role: "cashier"})
	if isElevationPrompt(rec) || rec.Code != http.StatusOK {
		t.Fatalf("unhide-all with approver PIN: code=%d prompt=%v: %s", rec.Code, isElevationPrompt(rec), rec.Body.String())
	}
	if n := countHidden(t, d); n != 0 {
		t.Fatalf("expected every active item unhidden, %d still hidden", n)
	}
	var actorID, blockedActorID, entityID, dataJSON string
	if err := d.Db.QueryRow(`SELECT actor_id, blocked_actor_id, entity_id, data_json FROM audit_log WHERE action='buttons_unhide_all'`).
		Scan(&actorID, &blockedActorID, &entityID, &dataJSON); err != nil {
		t.Fatalf("expected a buttons_unhide_all audit row: %v", err)
	}
	if actorID != mgrID || blockedActorID != blockedID {
		t.Fatalf("audit actor=%q blocked=%q, want %q / %q", actorID, blockedActorID, mgrID, blockedID)
	}
	if entityID != "all" {
		t.Fatalf("audit entity_id = %q, want all", entityID)
	}
	if !strings.Contains(dataJSON, `"count":2`) {
		t.Fatalf("audit data_json = %s, want count 2", dataJSON)
	}
}

// Same replica refusal as TestButtonsAPI_HideUnhideDeleteItemRefusedOnReplica.
func TestButtonsAPI_UnhideAllRefusedOnReplica(t *testing.T) {
	mux, d := newButtonsMux(t)
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}
	if _, err := d.Db.Exec(`UPDATE items SET sell_screen_hidden = 1 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/buttons/unhide-all", url.Values{}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unhide-all on replica: want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "manage quick-sale buttons on the primary till") {
		t.Fatalf("unhide-all on replica: missing replica_use_primary message, got %q", rec.Body.String())
	}
	var hidden int
	if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm1'`).Scan(&hidden); err != nil || hidden != 1 {
		t.Fatalf("itm1 must stay hidden on a replica: hidden=%d err=%v", hidden, err)
	}
}

// unhide-all takes no input, so a GET (link prefetch, an <img src>) must
// never mutate -- only POST is accepted.
func TestButtonsAPI_UnhideAll_RejectsGET(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`UPDATE items SET sell_screen_hidden = 1 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/buttons/unhide-all", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET unhide-all: code=%d, want 405", rec.Code)
	}
	var hidden int
	if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm1'`).Scan(&hidden); err != nil || hidden != 1 {
		t.Fatalf("GET must not unhide: hidden=%d err=%v", hidden, err)
	}
}
