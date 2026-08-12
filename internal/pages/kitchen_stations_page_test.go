package pages

// Kitchen-stations admin page (universaltill/ut-docs#516): manager gating,
// station CRUD via the form endpoints, category/item routing posts, and a
// real render of web/ui/pages/kitchen_stations.html.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func newKitchenStationsTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "kitchen-stations-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	if _, err := dbase.DB.Exec(`INSERT INTO categories (id, name) VALUES ('cat-food','Food')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbase.DB.Exec(`INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES ('itm-pie','PIE','Pork Pie',450,'cat-food',1)`); err != nil {
		t.Fatal(err)
	}
	d := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}}
	mux := http.NewServeMux()
	registerKitchenStations(mux, d)
	return mux, d
}

func TestKitchenStationsPagePermissions(t *testing.T) {
	mux, _ := newKitchenStationsTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/kitchen-stations", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /kitchen-stations = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Grill"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/kitchen-stations/routes/categories/cat-food", url.Values{"station_id": {"x"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier category routes = %d, want 403", rec.Code)
	}
}

func TestKitchenStationsPage_CreateUpdateDeactivateAndRender(t *testing.T) {
	mux, d := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// Whitespace-only name rejected.
	rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"   "}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.required" {
		t.Fatalf("whitespace-only name: loc=%q", rec.Header().Get("Location"))
	}

	// Create.
	rec = postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Grill"}, "printer_address": {"192.168.1.60:9100"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/kitchen-stations" {
		t.Fatalf("create: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM kitchen_stations WHERE name = 'Grill'`).Scan(&id); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	// The page renders and shows the station + routing matrix.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/kitchen-stations", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Grill") || !strings.Contains(body, "Food") {
		t.Fatalf("page render: code=%d body missing station/category", rec.Code)
	}

	// Update name + address.
	rec = postForm(mux, "/api/kitchen-stations/"+id, url.Values{"name": {"Char Grill"}, "printer_address": {"/dev/usb/lp1"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: code=%d", rec.Code)
	}
	var name, addr string
	if err := d.Db.QueryRow(`SELECT name, printer_address FROM kitchen_stations WHERE id = ?`, id).Scan(&name, &addr); err != nil || name != "Char Grill" || addr != "/dev/usb/lp1" {
		t.Fatalf("update did not take effect: name=%q addr=%q err=%v", name, addr, err)
	}

	// Unknown id → not_found error key (a valid address, so this isolates
	// the not-found path rather than tripping the address-required check).
	rec = postForm(mux, "/api/kitchen-stations/nope", url.Values{"name": {"X"}, "printer_address": {"192.168.1.61:9100"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.not_found" {
		t.Fatalf("not-found update: loc=%q", rec.Header().Get("Location"))
	}

	// Blank address rejected, on both create and update (code review,
	// ut-docs#516: a station with no address would silently swallow every
	// line routed to it).
	rec = postForm(mux, "/api/kitchen-stations", url.Values{"name": {"No Address"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.address_required" {
		t.Fatalf("create with blank address: loc=%q", rec.Header().Get("Location"))
	}
	rec = postForm(mux, "/api/kitchen-stations/"+id, url.Values{"name": {"Char Grill"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.address_required" {
		t.Fatalf("update with blank address: loc=%q", rec.Header().Get("Location"))
	}

	// Deactivate / reactivate (soft toggle, no delete).
	rec = postForm(mux, "/api/kitchen-stations/"+id+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate: code=%d", rec.Code)
	}
	var enabled int
	if err := d.Db.QueryRow(`SELECT enabled FROM kitchen_stations WHERE id = ?`, id).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatalf("not deactivated: enabled=%d err=%v", enabled, err)
	}
	rec = postForm(mux, "/api/kitchen-stations/"+id+"/active", url.Values{"active": {"1"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reactivate: code=%d", rec.Code)
	}
}

func TestKitchenStationsPage_CategoryAndItemRoutes(t *testing.T) {
	mux, d := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Grill"}, "printer_address": {"g:9100"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var station string
	if err := d.Db.QueryRow(`SELECT id FROM kitchen_stations WHERE name = 'Grill'`).Scan(&station); err != nil {
		t.Fatal(err)
	}

	// Category routing post writes the rows.
	rec = postForm(mux, "/api/kitchen-stations/routes/categories/cat-food", url.Values{"station_id": {station}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/kitchen-stations" {
		t.Fatalf("category routes: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM category_station_routes WHERE category_id = 'cat-food' AND station_id = ?`, station).Scan(&n); err != nil || n != 1 {
		t.Fatalf("category route row: n=%d err=%v", n, err)
	}

	// Item override post writes the rows; the overridden item then renders
	// in the overrides section.
	rec = postForm(mux, "/api/kitchen-stations/routes/items/itm-pie", url.Values{"station_id": {station}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("item routes: code=%d", rec.Code)
	}
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM item_station_routes WHERE item_id = 'itm-pie' AND station_id = ?`, station).Scan(&n); err != nil || n != 1 {
		t.Fatalf("item route row: n=%d err=%v", n, err)
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/kitchen-stations", nil), manager)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, req)
	if !strings.Contains(getRec.Body.String(), "Pork Pie") {
		t.Fatal("overridden item must render in the overrides list")
	}

	// Posting with no station_id values removes the override (replace-all).
	rec = postForm(mux, "/api/kitchen-stations/routes/items/itm-pie", url.Values{}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("remove override: code=%d", rec.Code)
	}
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM item_station_routes WHERE item_id = 'itm-pie'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("override not removed: n=%d err=%v", n, err)
	}
}

// The search box (?q=) renders matching, not-yet-overridden items with
// station checkboxes.
func TestKitchenStationsPage_ItemSearch(t *testing.T) {
	mux, _ := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Grill"}, "printer_address": {"g:9100"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/kitchen-stations?q=pork", nil), manager)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, req)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), "Pork Pie") {
		t.Fatalf("search render: code=%d, body missing match", getRec.Code)
	}
}
