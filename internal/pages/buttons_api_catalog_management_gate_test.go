package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// newButtonsMuxRealSession is newButtonsMux's real-auth twin (ut-docs#2312):
// UNLIKE newButtonsMux (which forces UT_AUTH=off so every pre-existing,
// auth-agnostic test in this package keeps passing unchanged), this helper
// wires a real AuthSvc against the real seedForPages/migration-seeded
// role_permissions, and leaves UT_AUTH alone -- so canPerform/checkOrElevate
// actually consult the session in the request context, exactly as a real
// till does. Mirrors internal/pages/tax_codes_page_test.go's own
// t.Setenv("UT_AUTH", "on") + real-session pattern for the same reason
// (TestTaxCodesPage_RealSessionGatesByRole).
func newButtonsMuxRealSession(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	d := &common.Deps{
		Db:       db,
		BtnStore: ui.NewButtonStore(db),
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
	mux := http.NewServeMux()
	registerButtonsAPI(mux, d)
	return mux, d
}

// seedOneButton is the minimal shortcut_buttons/items fixture every test
// below needs to exercise move/remove/add against a real row.
func seedOneButton(t *testing.T, d *common.Deps) {
	t.Helper()
	if _, err := d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES ('itm-btn','BTN-SKU','Button Item',100,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES ('BTN','Button Item','itm-btn',0),('BTN2','Second','itm-btn',1)`); err != nil {
		t.Fatalf("seed buttons: %v", err)
	}
}

// isElevationPrompt reports whether rec's body is checkOrElevate's
// needsElevation response (elevation.go's renderElevationPrompt) -- the
// X-UT-Response header it sets unconditionally on that branch, same check
// app.js's own utPostWithElevation/htmx OOB-swap detection uses.
func isElevationPrompt(rec *httptest.ResponseRecorder) bool {
	return rec.Header().Get("X-UT-Response") == "elevation-prompt"
}

// ut-docs#2312 AC: "A cashier-role session gets 403/elevation prompt on
// every route above; manager passes." Covers the four checkOrElevate-gated
// routes ut-docs#2312's card names explicitly: POST /api/buttons/{add,
// remove,reorder} and POST /api/buttons/move.
func TestButtonsAPI_CatalogManagementGate_RealSessionGatesByRole(t *testing.T) {
	cashier := auth.User{ID: "c1", Role: "cashier"}

	newMux := func(t *testing.T) (*http.ServeMux, *common.Deps) {
		mux, d := newButtonsMuxRealSession(t)
		seedOneButton(t, d)
		return mux, d
	}

	t.Run("reorder", func(t *testing.T) {
		mux, _ := newMux(t)
		rec := postForm(mux, "/api/buttons/reorder", url.Values{"codes": {"BTN2,BTN"}}, &cashier)
		if !isElevationPrompt(rec) {
			t.Fatalf("cashier reorder: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
		}
		for _, role := range []string{"manager", "admin", "super_admin"} {
			mux, _ := newMux(t)
			mgr := auth.User{ID: "u-" + role, Role: role}
			rec := postForm(mux, "/api/buttons/reorder", url.Values{"codes": {"BTN2,BTN"}}, &mgr)
			if isElevationPrompt(rec) {
				t.Fatalf("%s reorder: got elevation prompt, want past the gate: %d %s", role, rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusNoContent {
				t.Fatalf("%s reorder: code=%d, want 204: %s", role, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("add", func(t *testing.T) {
		mux, d := newMux(t)
		rec := postForm(mux, "/api/buttons/add", url.Values{"label": {"New"}, "code": {"NEW1"}, "itemId": {"itm-btn"}}, &cashier)
		if !isElevationPrompt(rec) {
			t.Fatalf("cashier add: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
		}
		var count int
		if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode='NEW1'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cashier add: button must not be created, count=%d err=%v", count, err)
		}
		for _, role := range []string{"manager", "admin", "super_admin"} {
			mux, d := newMux(t)
			mgr := auth.User{ID: "u-" + role, Role: role}
			rec := postForm(mux, "/api/buttons/add", url.Values{"label": {"New"}, "code": {"NEW-" + role}, "itemId": {"itm-btn"}}, &mgr)
			if isElevationPrompt(rec) {
				t.Fatalf("%s add: got elevation prompt, want past the gate: %d %s", role, rec.Code, rec.Body.String())
			}
			var count int
			if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode=?`, "NEW-"+role).Scan(&count); err != nil || count != 1 {
				t.Fatalf("%s add: button must be created, count=%d err=%v", role, count, err)
			}
		}
	})

	t.Run("remove", func(t *testing.T) {
		mux, d := newMux(t)
		rec := postForm(mux, "/api/buttons/remove", url.Values{"code": {"BTN"}}, &cashier)
		if !isElevationPrompt(rec) {
			t.Fatalf("cashier remove: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
		}
		var count int
		if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode='BTN'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("cashier remove: button must not be removed, count=%d err=%v", count, err)
		}
		for _, role := range []string{"manager", "admin", "super_admin"} {
			mux, d := newMux(t)
			mgr := auth.User{ID: "u-" + role, Role: role}
			rec := postForm(mux, "/api/buttons/remove", url.Values{"code": {"BTN"}}, &mgr)
			if isElevationPrompt(rec) {
				t.Fatalf("%s remove: got elevation prompt, want past the gate: %d %s", role, rec.Code, rec.Body.String())
			}
			var count int
			if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE barcode='BTN'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("%s remove: button must be removed, count=%d err=%v", role, count, err)
			}
		}
	})

	t.Run("move", func(t *testing.T) {
		mux, d := newMux(t)
		rec := postForm(mux, "/api/buttons/move", url.Values{"code": {"BTN"}, "dir": {"1"}}, &cashier)
		if !isElevationPrompt(rec) {
			t.Fatalf("cashier move: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
		}
		var order int
		if err := d.Db.QueryRow(`SELECT sort_order FROM shortcut_buttons WHERE barcode='BTN'`).Scan(&order); err != nil || order != 0 {
			t.Fatalf("cashier move: sort_order must not change, got %d err=%v", order, err)
		}
		for _, role := range []string{"manager", "admin", "super_admin"} {
			mux, d := newMux(t)
			mgr := auth.User{ID: "u-" + role, Role: role}
			rec := postForm(mux, "/api/buttons/move", url.Values{"code": {"BTN"}, "dir": {"1"}}, &mgr)
			if isElevationPrompt(rec) {
				t.Fatalf("%s move: got elevation prompt, want past the gate: %d %s", role, rec.Code, rec.Body.String())
			}
			var order int
			if err := d.Db.QueryRow(`SELECT sort_order FROM shortcut_buttons WHERE barcode='BTN'`).Scan(&order); err != nil || order != 1 {
				t.Fatalf("%s move: sort_order must change, got %d err=%v", role, order, err)
			}
		}
	})
}

// ut-docs#2312: GET /ui/pos/tile-sheet renders Move/Remove/Edit LOCKED
// (a lock icon + muted styling, never the real `disabled` attribute --
// #2285's "show, don't hide") for a cashier, and fully live for a manager.
// A cashier's tap must still reach the server (proven above: it lands on
// the real elevation prompt), so Move/Remove are never `disabled` here --
// only Edit's IsReplica branch uses that attribute, and that's unrelated to
// this gate.
func TestTileSheet_LockedForCashierGrantedForManager(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedOneButton(t, d)

	get := func(u auth.User) *httptest.ResponseRecorder {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/pos/tile-sheet?code=BTN", nil), u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	cashierBody := get(auth.User{ID: "c1", Role: "cashier"}).Body.String()
	if !strings.Contains(cashierBody, "tile-sheet-lock") {
		t.Fatalf("cashier tile sheet: expected a lock icon, got: %s", cashierBody)
	}
	// Move-LATER must stay CLICKABLE (no real `disabled`) for a locked
	// cashier: BTN is at position 0 (of 2, same uncategorized bucket), so
	// it HAS a later same-category neighbour -- only IsReplica or a real
	// edge may ever add `disabled` here, and neither applies. A tap must
	// still reach the server and land on the real elevation prompt
	// (proven by TestButtonsAPI_CatalogManagementGate_RealSessionGatesByRole
	// above), which a real `disabled` attribute would silently prevent.
	moveLaterStart := strings.Index(cashierBody, `data-testid="tile-sheet-move-later"`)
	if moveLaterStart < 0 {
		t.Fatalf("cashier tile sheet: missing move-later button: %s", cashierBody)
	}
	moveLaterTag := cashierBody[moveLaterStart:]
	if end := strings.Index(moveLaterTag, ">"); end >= 0 {
		moveLaterTag = moveLaterTag[:end]
	}
	if strings.Contains(moveLaterTag, "disabled") {
		t.Fatalf("cashier tile sheet: move-later must stay clickable (locked, not disabled) so a tap reaches the elevation prompt, got tag: %s", moveLaterTag)
	}

	managerBody := get(auth.User{ID: "m1", Role: "manager"}).Body.String()
	if strings.Contains(managerBody, "tile-sheet-lock") {
		t.Fatalf("manager tile sheet: expected no lock icon, got: %s", managerBody)
	}
}
