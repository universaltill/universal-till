package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// newDesignerCategoriesMux (ut-docs#2174) wires the Designer's own
// category-mutation routes over a real migrated DB with a REAL AuthSvc
// (UT_AUTH=on), same shape as newButtonsMuxRealSession: these routes gate
// on canPerform(d, r, "catalog_management") — the same action /designer
// itself gates on (ut-docs#2357) — so the tests below must actually consult
// the session, not bypass it.
func newDesignerCategoriesMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, d := newButtonsMuxRealSession(t)
	registerDesignerCategoriesAPI(mux, d)
	return mux, d
}

// seedDesignerCategories: two active top-level categories (one with an
// active item in it, one empty) plus one inactive one, so every branch —
// blocked deactivate, allowed deactivate, reactivate — has a fixture.
func seedDesignerCategories(t *testing.T, d *common.Deps) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO categories(id,name,sort_order,is_active) VALUES ('cat-a','Drinks',0,1),('cat-b','Snacks',1,1),('cat-c','Old',2,0)`,
		`INSERT INTO items(id,sku,name,base_price,is_active,category_id) VALUES ('itm-a','A-SKU','Cola',100,1,'cat-a')`,
	} {
		if _, err := d.Db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// TestDesignerCategoriesAPI_CatalogManagementGate: every Designer category
// route is a plain 403 for a cashier (no elevation prompt — the page itself
// is already unreachable without catalog_management, ut-docs#2357) and
// passes for manager/admin/super_admin. Same role sweep as
// TestButtonsAPI_CatalogManagementGate_RealSessionGatesByRole.
func TestDesignerCategoriesAPI_CatalogManagementGate(t *testing.T) {
	cashier := auth.User{ID: "c1", Role: "cashier"}
	routes := []struct {
		name, path string
		form       url.Values
	}{
		{"create", "/api/designer/categories", url.Values{"name": {"New"}}},
		{"update", "/api/designer/categories/cat-a", url.Values{"name": {"Renamed"}}},
		{"active", "/api/designer/categories/cat-b/active", url.Values{"active": {"0"}}},
		{"reorder", "/api/designer/categories/reorder", url.Values{"ids": {"cat-b,cat-a,cat-c"}}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			mux, d := newDesignerCategoriesMux(t)
			seedDesignerCategories(t, d)
			rec := postForm(mux, rt.path, rt.form, &cashier)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("cashier %s = %d, want 403: %s", rt.name, rec.Code, rec.Body.String())
			}
			if isElevationPrompt(rec) {
				t.Fatalf("cashier %s: got an elevation prompt, want a plain 403", rt.name)
			}
			for _, role := range []string{"manager", "admin", "super_admin"} {
				mux, d := newDesignerCategoriesMux(t)
				seedDesignerCategories(t, d)
				mgr := auth.User{ID: "u-" + role, Role: role}
				rec := postForm(mux, rt.path, rt.form, &mgr)
				if rec.Code != http.StatusNoContent {
					t.Fatalf("%s %s = %d, want 204: %s", role, rt.name, rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
					t.Fatalf("%s %s: HX-Trigger = %q, want buttons-changed (the live preview refreshes off it)", role, rt.name, got)
				}
			}
		})
	}
}

func TestDesignerCategoriesAPI_Create(t *testing.T) {
	mux, d := newDesignerCategoriesMux(t)
	seedDesignerCategories(t, d)
	mgr := auth.User{ID: "m1", Role: "manager"}

	rec := postForm(mux, "/api/designer/categories", url.Values{"name": {"  Bakery "}, "color": {"#0f766e"}}, &mgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var name, color string
	var sortOrder int
	if err := d.Db.QueryRow(`SELECT name, COALESCE(color,''), sort_order FROM categories WHERE name='Bakery'`).Scan(&name, &color, &sortOrder); err != nil {
		t.Fatalf("created row missing: %v", err)
	}
	if color != "#0f766e" || sortOrder != 3 {
		t.Fatalf("created row = %q/%d, want colour kept and appended after the existing three", color, sortOrder)
	}

	// Blank name: refused with the localized categories key, nothing written.
	rec = postForm(mux, "/api/designer/categories", url.Values{"name": {"   "}}, &mgr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank create = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if want := httpx.T("en", "categories.error.name_required"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("blank create body = %q, want %q", rec.Body.String(), want)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("error Content-Type = %q, want text/html so app.js's beforeSwap swaps it inline", ct)
	}
	if rec.Header().Get("HX-Trigger") != "" {
		t.Fatalf("a refused create must not trigger a preview refresh")
	}

	// A colour off the fixed palette is refused (same allowlist as /categories).
	rec = postForm(mux, "/api/designer/categories", url.Values{"name": {"Evil"}, "color": {"red;--x:1"}}, &mgr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad colour create = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := d.Db.QueryRow(`SELECT count(*) FROM categories WHERE name='Evil'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("bad colour create must not persist, count=%d err=%v", n, err)
	}
}

func TestDesignerCategoriesAPI_Update(t *testing.T) {
	mux, d := newDesignerCategoriesMux(t)
	seedDesignerCategories(t, d)
	mgr := auth.User{ID: "m1", Role: "manager"}

	rec := postForm(mux, "/api/designer/categories/cat-a", url.Values{"name": {"Cold drinks"}, "color": {"#4338ca"}}, &mgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	var name, color string
	if err := d.Db.QueryRow(`SELECT name, COALESCE(color,'') FROM categories WHERE id='cat-a'`).Scan(&name, &color); err != nil {
		t.Fatalf("row: %v", err)
	}
	if name != "Cold drinks" || color != "#4338ca" {
		t.Fatalf("row = %q/%q after update", name, color)
	}

	// Recolour to "no colour" clears it.
	rec = postForm(mux, "/api/designer/categories/cat-a", url.Values{"name": {"Cold drinks"}, "color": {""}}, &mgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear colour = %d: %s", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT COALESCE(color,'') FROM categories WHERE id='cat-a'`).Scan(&color); err != nil || color != "" {
		t.Fatalf("colour after clear = %q err=%v, want empty", color, err)
	}

	rec = postForm(mux, "/api/designer/categories/nope", url.Values{"name": {"X"}}, &mgr)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if want := httpx.T("en", "categories.error.not_found"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("unknown id body = %q, want %q", rec.Body.String(), want)
	}
}

// TestDesignerCategoriesAPI_Active: deactivating a category that still has
// active items is refused with the blocking count surfaced inline (the
// card's "don't silently block" requirement); an empty category deactivates
// and reactivates; both leave the preview refreshed only on success.
func TestDesignerCategoriesAPI_Active(t *testing.T) {
	mux, d := newDesignerCategoriesMux(t)
	seedDesignerCategories(t, d)
	mgr := auth.User{ID: "m1", Role: "manager"}

	rec := postForm(mux, "/api/designer/categories/cat-a/active", url.Values{"active": {"0"}}, &mgr)
	if rec.Code != http.StatusConflict {
		t.Fatalf("blocked deactivate = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if want := fmt.Sprintf(httpx.T("en", "categories.error.deactivate_blocked"), 1); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("blocked body = %q, want %q", rec.Body.String(), want)
	}
	if rec.Header().Get("HX-Trigger") != "" {
		t.Fatalf("a blocked deactivate must not trigger a preview refresh")
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM categories WHERE id='cat-a'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("cat-a must stay active, is_active=%d err=%v", active, err)
	}

	rec = postForm(mux, "/api/designer/categories/cat-b/active", url.Values{"active": {"0"}}, &mgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("deactivate empty = %d: %s", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT is_active FROM categories WHERE id='cat-b'`).Scan(&active); err != nil || active != 0 {
		t.Fatalf("cat-b is_active=%d err=%v, want 0", active, err)
	}

	rec = postForm(mux, "/api/designer/categories/cat-c/active", url.Values{"active": {"1"}}, &mgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reactivate = %d: %s", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT is_active FROM categories WHERE id='cat-c'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("cat-c is_active=%d err=%v, want 1", active, err)
	}
}

func TestDesignerCategoriesAPI_Reorder(t *testing.T) {
	mux, d := newDesignerCategoriesMux(t)
	seedDesignerCategories(t, d)
	mgr := auth.User{ID: "m1", Role: "manager"}

	// Comma-joined single field (what the Designer's own script posts).
	rec := postForm(mux, "/api/designer/categories/reorder", url.Values{"ids": {"cat-c,cat-b,cat-a"}}, &mgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reorder = %d: %s", rec.Code, rec.Body.String())
	}
	rows, err := d.Db.Query(`SELECT id FROM categories ORDER BY sort_order, name`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	if strings.Join(got, ",") != "cat-c,cat-b,cat-a" {
		t.Fatalf("order after reorder = %v", got)
	}

	rec = postForm(mux, "/api/designer/categories/reorder", url.Values{}, &mgr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reorder = %d, want 400", rec.Code)
	}
}

// TestDesignerCategoriesAPI_ReplicaRefused: categories sync shop-wide from
// the primary (adminTables), so a satellite must refuse every mutation up
// front — same requirePrimary contract as /api/categories/* and
// /api/buttons/*.
func TestDesignerCategoriesAPI_ReplicaRefused(t *testing.T) {
	mux, d := newDesignerCategoriesMux(t)
	seedDesignerCategories(t, d)
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.local"); err != nil {
		t.Fatalf("set primary url: %v", err)
	}
	mgr := auth.User{ID: "m1", Role: "manager"}
	rec := postForm(mux, "/api/designer/categories", url.Values{"name": {"Nope"}}, &mgr)
	if rec.Code != http.StatusConflict {
		t.Fatalf("replica create = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if want := httpx.T("en", "categories.error.replica_use_primary"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("replica body = %q, want %q", rec.Body.String(), want)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT count(*) FROM categories WHERE name='Nope'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("replica create must not persist, count=%d err=%v", n, err)
	}
}
