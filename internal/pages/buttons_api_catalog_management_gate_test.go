package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
// every route above; manager passes." Covers the checkOrElevate-gated
// routes ut-docs#2312's card names explicitly: POST /api/buttons/{add,
// remove,reorder}. POST /api/buttons/move (the ut-docs#2285 long-press
// sheet's own route) no longer exists -- ut-docs#2339's jiggle-mode edit
// mode replaced that whole UI with drag/keyboard reorder over the shared
// /api/buttons/reorder route, already covered by the "reorder" sub-test.
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

}
