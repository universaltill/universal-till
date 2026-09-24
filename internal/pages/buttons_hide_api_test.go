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

// ut-docs#2541: /api/buttons/{hide,unhide,delete-item} copy the exact
// gating/elevation pattern the existing reorder/add/remove routes already
// have (TestButtonsAPI_CatalogManagementGate_RealSessionGatesByRole) —
// requirePrimary + checkOrElevate("catalog_management").
func TestButtonsAPI_HideUnhideDeleteItem_CatalogManagementGate(t *testing.T) {
	cashier := auth.User{ID: "c1", Role: "cashier"}

	newMux := func(t *testing.T) (*http.ServeMux, *common.Deps) {
		mux, d := newButtonsMuxRealSession(t)
		seedOneButton(t, d)
		return mux, d
	}

	t.Run("hide", func(t *testing.T) {
		mux, d := newMux(t)
		rec := postForm(mux, "/api/buttons/hide", url.Values{"itemId": {"itm-btn"}}, &cashier)
		if !isElevationPrompt(rec) {
			t.Fatalf("cashier hide: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
		}
		var hidden int
		if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm-btn'`).Scan(&hidden); err != nil || hidden != 0 {
			t.Fatalf("cashier hide: item must not be hidden, hidden=%d err=%v", hidden, err)
		}

		for _, role := range []string{"manager", "admin", "super_admin"} {
			mux, d := newMux(t)
			mgr := auth.User{ID: "u-" + role, Role: role}
			rec := postForm(mux, "/api/buttons/hide", url.Values{"itemId": {"itm-btn"}}, &mgr)
			if isElevationPrompt(rec) {
				t.Fatalf("%s hide: got elevation prompt, want past the gate: %d %s", role, rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s hide: code=%d, want 200: %s", role, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
				t.Fatalf("%s hide: HX-Trigger = %q, want buttons-changed", role, got)
			}
			var hidden int
			if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm-btn'`).Scan(&hidden); err != nil || hidden != 1 {
				t.Fatalf("%s hide: item must be hidden, hidden=%d err=%v", role, hidden, err)
			}
			var buttons int
			if err := d.Db.QueryRow(`SELECT count(*) FROM shortcut_buttons WHERE item_id='itm-btn'`).Scan(&buttons); err != nil || buttons != 0 {
				t.Fatalf("%s hide: explicit shortcut_buttons rows must be deleted, got %d", role, buttons)
			}
		}
	})

	t.Run("unhide", func(t *testing.T) {
		mux, d := newMux(t)
		if _, err := d.Db.Exec(`UPDATE items SET sell_screen_hidden=1 WHERE id='itm-btn'`); err != nil {
			t.Fatal(err)
		}
		rec := postForm(mux, "/api/buttons/unhide", url.Values{"itemId": {"itm-btn"}}, &cashier)
		if !isElevationPrompt(rec) {
			t.Fatalf("cashier unhide: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
		}
		var hidden int
		if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm-btn'`).Scan(&hidden); err != nil || hidden != 1 {
			t.Fatalf("cashier unhide: item must stay hidden, hidden=%d err=%v", hidden, err)
		}

		for _, role := range []string{"manager", "admin", "super_admin"} {
			mux, d := newMux(t)
			if _, err := d.Db.Exec(`UPDATE items SET sell_screen_hidden=1 WHERE id='itm-btn'`); err != nil {
				t.Fatal(err)
			}
			mgr := auth.User{ID: "u-" + role, Role: role}
			rec := postForm(mux, "/api/buttons/unhide", url.Values{"itemId": {"itm-btn"}}, &mgr)
			if isElevationPrompt(rec) {
				t.Fatalf("%s unhide: got elevation prompt, want past the gate: %d %s", role, rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s unhide: code=%d, want 200: %s", role, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
				t.Fatalf("%s unhide: HX-Trigger = %q, want buttons-changed", role, got)
			}
			var hidden int
			if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm-btn'`).Scan(&hidden); err != nil || hidden != 0 {
				t.Fatalf("%s unhide: item must be unhidden, hidden=%d err=%v", role, hidden, err)
			}
		}
	})

	t.Run("delete-item", func(t *testing.T) {
		mux, d := newMux(t)
		rec := postForm(mux, "/api/buttons/delete-item", url.Values{"itemId": {"itm-btn"}}, &cashier)
		if !isElevationPrompt(rec) {
			t.Fatalf("cashier delete-item: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
		}
		var active int
		if err := d.Db.QueryRow(`SELECT is_active FROM items WHERE id='itm-btn'`).Scan(&active); err != nil || active != 1 {
			t.Fatalf("cashier delete-item: item must stay active, active=%d err=%v", active, err)
		}

		mux2, d2 := newMux(t)
		mgr := auth.User{ID: "u-manager", Role: "manager"}
		rec2 := postForm(mux2, "/api/buttons/delete-item", url.Values{"itemId": {"itm-btn"}}, &mgr)
		if isElevationPrompt(rec2) {
			t.Fatalf("manager delete-item: got elevation prompt, want past the gate: %d %s", rec2.Code, rec2.Body.String())
		}
		if rec2.Code != http.StatusOK {
			t.Fatalf("manager delete-item: code=%d, want 200: %s", rec2.Code, rec2.Body.String())
		}
		if got := rec2.Header().Get("HX-Trigger"); got != "buttons-changed" {
			t.Fatalf("manager delete-item: HX-Trigger = %q, want buttons-changed", got)
		}
		var active2 int
		if err := d2.Db.QueryRow(`SELECT is_active FROM items WHERE id='itm-btn'`).Scan(&active2); err != nil || active2 != 0 {
			t.Fatalf("manager delete-item: item must be deactivated, active=%d err=%v", active2, err)
		}
	})
}

// TestButtonsAPI_HideUnhideDeleteItem_ElevationSummaryUsesItemName
// (ut-docs#2541 review finding 3): hide/unhide/delete-item's elevation
// prompts used to reuse /api/buttons/{add,remove}'s own summary keys
// (elevation.summary.buttons_add/buttons_remove), formatted with the raw
// itemId -- a manager approving the PIN prompt saw a bare UUID, not what
// they're actually approving, and delete-item's prompt (borrowing "remove"
// wording) never said the ITEM was being deleted from the catalog, only
// that a "quick button" was being removed from the sell screen -- much less
// alarming than what the action actually does. Each route now has its OWN
// key (elevation.summary.buttons_{hide,unhide,delete_item}), formatted with
// the item's real NAME (looked up from the catalog), and delete-item's
// wording explicitly says the item is deleted from the catalog.
func TestButtonsAPI_HideUnhideDeleteItem_ElevationSummaryUsesItemName(t *testing.T) {
	cashier := auth.User{ID: "c1", Role: "cashier"}

	cases := []struct {
		path       string
		wantSubstr string
	}{
		{"/api/buttons/hide", "Hide “Button Item” from the sell screen."},
		{"/api/buttons/unhide", "Show “Button Item” on the sell screen again."},
		{"/api/buttons/delete-item", "Delete “Button Item” from the catalog. This removes it everywhere, not just the sell screen."},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			mux, d := newButtonsMuxRealSession(t)
			seedOneButton(t, d)

			rec := postForm(mux, tc.path, url.Values{"itemId": {"itm-btn"}}, &cashier)
			if !isElevationPrompt(rec) {
				t.Fatalf("%s: want elevation prompt, got %d: %s", tc.path, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.wantSubstr) {
				t.Fatalf("%s: expected elevation summary %q, got: %s", tc.path, tc.wantSubstr, body)
			}
			// The raw id still legitimately appears once, as the retry
			// form's hidden itemId field (elevationHiddenField) -- only the
			// VISIBLE summary text must show the name, not the id.
			summary := body[strings.Index(body, `class="elevation-summary"`):]
			summary = summary[:strings.Index(summary, "</p>")]
			if strings.Contains(summary, "itm-btn") {
				t.Fatalf("%s: elevation summary text must show the item's NAME, not its raw id, got: %s", tc.path, summary)
			}
		})
	}
}

// TestButtonsAPI_HideUnhideDeleteItem_ElevationSummaryFallsBackToIDWhenLookupFails
// (ut-docs#2541 review finding 3): an itemId that doesn't resolve to a real
// item (stale/tampered client state) must still render a usable elevation
// summary -- falling back to the raw id rather than blanking the message or
// erroring the whole prompt out.
func TestButtonsAPI_HideUnhideDeleteItem_ElevationSummaryFallsBackToIDWhenLookupFails(t *testing.T) {
	cashier := auth.User{ID: "c1", Role: "cashier"}
	for _, path := range []string{"/api/buttons/hide", "/api/buttons/unhide", "/api/buttons/delete-item"} {
		t.Run(path, func(t *testing.T) {
			mux, _ := newButtonsMuxRealSession(t)
			rec := postForm(mux, path, url.Values{"itemId": {"does-not-exist"}}, &cashier)
			if !isElevationPrompt(rec) {
				t.Fatalf("%s: want elevation prompt, got %d: %s", path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "does-not-exist") {
				t.Fatalf("%s: expected the raw id as a fallback when the item lookup fails, got: %s", path, rec.Body.String())
			}
		})
	}
}

// TestButtonsAPI_HideUnhideDeleteItemRefusedOnReplica (ut-docs#2541 review
// finding 6): hide/unhide/delete-item all write to shop-wide admin state
// (items.sell_screen_hidden/is_active, shortcut_buttons) just like
// add/remove/reorder, so they need the exact same replica refusal
// TestButtonsAPI_MutationsRefusedOnReplica already pins for those routes --
// a write accepted on a satellite would silently vanish on the next admin
// pull (ut-docs#1697 and its own cited defect class).
func TestButtonsAPI_HideUnhideDeleteItemRefusedOnReplica(t *testing.T) {
	mux, d := newButtonsMux(t)
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	const wantMsg = "manage quick-sale buttons on the primary till"
	assertRefused := func(t *testing.T, label string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusConflict {
			t.Errorf("%s on replica: want 409, got %d: %s", label, rec.Code, rec.Body.String())
			return
		}
		if !strings.Contains(rec.Body.String(), wantMsg) {
			t.Errorf("%s on replica: body missing the localized replica_use_primary message, got %q", label, rec.Body.String())
		}
	}

	assertRefused(t, "hide", postForm(mux, "/api/buttons/hide", url.Values{"itemId": {"itm1"}}, nil))
	var hidden int
	if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm1'`).Scan(&hidden); err != nil || hidden != 0 {
		t.Errorf("itm1 must not be hidden on a replica: hidden=%d err=%v", hidden, err)
	}

	if _, err := d.Db.Exec(`UPDATE items SET sell_screen_hidden = 1 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	assertRefused(t, "unhide", postForm(mux, "/api/buttons/unhide", url.Values{"itemId": {"itm1"}}, nil))
	if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id='itm1'`).Scan(&hidden); err != nil || hidden != 1 {
		t.Errorf("itm1 must stay hidden on a replica: hidden=%d err=%v", hidden, err)
	}

	assertRefused(t, "delete-item", postForm(mux, "/api/buttons/delete-item", url.Values{"itemId": {"itm1"}}, nil))
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM items WHERE id='itm1'`).Scan(&active); err != nil || active != 1 {
		t.Errorf("itm1 must not be deactivated on a replica: active=%d err=%v", active, err)
	}
}

// TestButtonsAPI_HideUnhideDeleteItem_EmptyItemIDIs400 (design's "Validate
// input (empty/unknown itemId → 400 localized fragment like Remove does)"):
// an empty itemId is rejected by all three new routes with the same
// localized-fragment 400 shape /api/buttons/{add,remove} already use.
func TestButtonsAPI_HideUnhideDeleteItem_EmptyItemIDIs400(t *testing.T) {
	for _, path := range []string{"/api/buttons/hide", "/api/buttons/unhide", "/api/buttons/delete-item"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("UT_AUTH", "off")
			chdirRoot(t)
			initPagesI18n(t)
			db := openPagesTestDB(t)
			defer db.Close()
			seedForPages(t, db)
			d := &common.Deps{Db: db, BtnStore: ui.NewButtonStore(db), Settings: settings.NewStore(db)}
			mux := http.NewServeMux()
			registerButtonsAPI(mux, d)

			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("itemId="))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s with empty itemId: code=%d, want 400: %s", path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `class="error"`) {
				t.Fatalf("%s: expected a localized error fragment, got: %s", path, rec.Body.String())
			}
		})
	}
}

// TestButtonsAPI_HideUnhideDeleteItem_UnknownOrInactiveItemIs400
// (ut-docs#2541 review finding 4): hide/unhide/delete-item used to accept
// an unknown or already-inactive item id and answer 200 + write an audit
// row for an action that never actually changed anything (the underlying
// UPDATE touched zero rows and returned no error). The repo layer now
// returns data.ErrItemNotFound, which these handlers must map to the same
// localized 400 fragment as an empty itemId, and no elevated case may
// audit a no-op.
func TestButtonsAPI_HideUnhideDeleteItem_UnknownOrInactiveItemIs400(t *testing.T) {
	manager := auth.User{ID: "u-manager", Role: "manager"}

	for _, path := range []string{"/api/buttons/hide", "/api/buttons/unhide", "/api/buttons/delete-item"} {
		t.Run(path+"/unknown", func(t *testing.T) {
			mux, _ := newButtonsMuxRealSession(t)
			rec := postForm(mux, path, url.Values{"itemId": {"does-not-exist"}}, &manager)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s with unknown itemId: code=%d, want 400: %s", path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `class="error"`) {
				t.Fatalf("%s: expected a localized error fragment, got: %s", path, rec.Body.String())
			}
		})
		t.Run(path+"/inactive", func(t *testing.T) {
			mux, d := newButtonsMuxRealSession(t)
			if _, err := d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES ('itm-inactive','INA-SKU','Inactive Item',100,0)`); err != nil {
				t.Fatalf("seed inactive item: %v", err)
			}
			rec := postForm(mux, path, url.Values{"itemId": {"itm-inactive"}}, &manager)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s with inactive itemId: code=%d, want 400: %s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestButtonsAPI_Remove_UnknownCodeIs400 (design's Remove-parity validation
// bar, for the one path -- Remove -- that still resolves from a code):
// Remove without an itemId AND with a code that has no shortcut_buttons row
// must 400, not silently succeed.
func TestButtonsAPI_Remove_UnknownCodeIs400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	d := &common.Deps{Db: db, BtnStore: ui.NewButtonStore(db), Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerButtonsAPI(mux, d)

	req := httptest.NewRequest(http.MethodPost, "/api/buttons/remove", strings.NewReader("code=never-existed"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("remove with unknown code: code=%d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestButtonsAPI_Reorder_MaterializesImplicitTile (ut-docs#2541): dragging
// a tile that has no shortcut_buttons row of its own (every active,
// non-hidden item is an implicit quick button by default) through the real
// HTTP route persists it, not just at the ButtonStore level
// (TestButtonStoreUpdateOrder_MaterializesImplicitTile in internal/ui).
func TestButtonsAPI_Reorder_MaterializesImplicitTile(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	// itm1 (from seedForPages) already exists and is active -- give it a
	// real SKU-resolvable code with no shortcut_buttons row so the reorder
	// below has an implicit tile to materialize.
	if _, err := db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES ('itm-implicit','IMP-SKU','Implicit Item',100,1)`); err != nil {
		t.Fatalf("seed implicit item: %v", err)
	}
	d := &common.Deps{Db: db, BtnStore: ui.NewButtonStore(db), Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerButtonsAPI(mux, d)

	form := url.Values{"codes": {"IMP-SKU,ABC"}} // ABC is itm1's own seeded barcode
	req := httptest.NewRequest(http.MethodPost, "/api/buttons/reorder", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reorder: code=%d, want 204: %s", rec.Code, rec.Body.String())
	}

	var barcode string
	var sortOrder int
	if err := db.QueryRow(`SELECT barcode, sort_order FROM shortcut_buttons WHERE item_id='itm-implicit'`).Scan(&barcode, &sortOrder); err != nil {
		t.Fatalf("expected a materialized shortcut_buttons row for the implicit tile: %v", err)
	}
	if barcode != "IMP-SKU" || sortOrder != 0 {
		t.Fatalf("materialized row = barcode=%q sort_order=%d, want IMP-SKU at position 0", barcode, sortOrder)
	}
}
