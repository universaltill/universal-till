package pages

// Kitchen-stations admin page (universaltill/ut-docs#516): manager gating,
// station CRUD via the form endpoints, category/item routing posts, and a
// real render of web/ui/pages/kitchen_stations.html.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// stubBrowsePrinters lets tests swap in a canned BrowsePrinters result
// instead of waiting out a real (bounded but multi-second) LAN scan on
// every test run — same pattern as discovery_api_test.go's stubBrowse.
func stubBrowsePrinters(t *testing.T, candidates []discovery.PrinterCandidate, err error) {
	t.Helper()
	orig := discoveryBrowsePrinters
	discoveryBrowsePrinters = func(ctx context.Context, timeout time.Duration) ([]discovery.PrinterCandidate, error) {
		return candidates, err
	}
	t.Cleanup(func() { discoveryBrowsePrinters = orig })
}

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
	d := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(dbase.DB)}
	mux := http.NewServeMux()
	registerKitchenStations(mux, d)
	return mux, d
}

// ut-docs#902: GET /kitchen-stations must be reachable under UT_AUTH=off
// with no session — same fix and rationale as
// country_settings_page_test.go's TestCountrySettingsPage_ReachableUnderAuthOff
// (ut-docs#901's precedent).
func TestKitchenStationsPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newKitchenStationsTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/kitchen-stations", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /kitchen-stations under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Mutating handlers, not just the GET page, must also pick up canPerform's
// UT_AUTH=off bypass — independent review finding on ut-docs#901, applied
// here too.
func TestKitchenStationsPageCreate_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newKitchenStationsTestMux(t)

	rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Auth-Off Grill"}, "printer_address": {"tcp://127.0.0.1:9100"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/kitchen-stations" {
		t.Fatalf("create under UT_AUTH=off: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
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
	// ut-docs#1458: GET /kitchen-stations must render the full layout on
	// 403 too, not a bare rail-less body (same fix class as #1455).
	if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
		t.Fatalf("cashier's 403 on GET /kitchen-stations has no nav rail:\n%s", body)
	}
	if rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Grill"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/kitchen-stations/routes/categories/cat-food", url.Values{"station_id": {"x"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier category routes = %d, want 403", rec.Code)
	}
}

// ut-docs#1585: kitchen_stations and category_station_routes/
// item_station_routes sync shop-wide as admin tables (adminTables,
// ut-docs#1546) via a one-way primary-wins pull — a write accepted on a
// satellite would silently vanish on the very next admin pull, with
// nothing explaining why. Every mutating action (station create/update/
// active, category routes, item routes) must refuse on a replica with a
// clear, localized signal, same pattern as registers_page.go's
// requirePrimary (ut-docs#1590).
func TestKitchenStationsPage_MutationsRefusedOnReplica(t *testing.T) {
	mux, d := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// A station that existed before this till became a replica (synced
	// down from the primary) — used below to check update/active/routes,
	// since create itself is refused and can't produce one.
	const stationID = "station-existing"
	if _, err := d.Db.Exec(`INSERT INTO kitchen_stations (id, name, destination_type, printer_address, enabled, created_at, updated_at) VALUES (?, 'Existing', 'printer', 'g:9100', 1, datetime('now'), datetime('now'))`, stationID); err != nil {
		t.Fatalf("seed station: %v", err)
	}

	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Satellite Grill"}, "printer_address": {"s:9100"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.replica_use_primary" {
		t.Fatalf("create on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var count int
	if err := d.Db.QueryRow(`SELECT count(*) FROM kitchen_stations WHERE name = 'Satellite Grill'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("station must not be created on a replica, found %d rows", count)
	}

	rec = postForm(mux, "/api/kitchen-stations/"+stationID, url.Values{"name": {"Renamed"}, "printer_address": {"g:9100"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.replica_use_primary" {
		t.Fatalf("update (name change) on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var name string
	if err := d.Db.QueryRow(`SELECT name FROM kitchen_stations WHERE id = ?`, stationID).Scan(&name); err != nil || name != "Existing" {
		t.Fatalf("name must not change on a replica: name=%q err=%v", name, err)
	}

	// ut-docs#1585: printer_address is till-local (never synced) -- an
	// address-only edit (name/destination_type unchanged) is the one
	// mutation a replica is still allowed to make on this page.
	rec = postForm(mux, "/api/kitchen-stations/"+stationID, url.Values{"name": {"Existing"}, "printer_address": {"192.168.1.99:9100"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/kitchen-stations" {
		t.Fatalf("address-only update on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var addr string
	if err := d.Db.QueryRow(`SELECT printer_address FROM kitchen_stations WHERE id = ?`, stationID).Scan(&addr); err != nil || addr != "192.168.1.99:9100" {
		t.Fatalf("address-only update must persist on a replica: addr=%q err=%v", addr, err)
	}

	rec = postForm(mux, "/api/kitchen-stations/"+stationID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.replica_use_primary" {
		t.Fatalf("deactivate on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var enabled int
	if err := d.Db.QueryRow(`SELECT enabled FROM kitchen_stations WHERE id = ?`, stationID).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("station must not be deactivated on a replica: enabled=%d err=%v", enabled, err)
	}

	rec = postForm(mux, "/api/kitchen-stations/routes/categories/cat-food", url.Values{"station_id": {stationID}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.replica_use_primary" {
		t.Fatalf("category routes on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM category_station_routes WHERE category_id = 'cat-food'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("category route must not be written on a replica: n=%d err=%v", n, err)
	}

	rec = postForm(mux, "/api/kitchen-stations/routes/items/itm-pie", url.Values{"station_id": {stationID}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.replica_use_primary" {
		t.Fatalf("item routes on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM item_station_routes WHERE item_id = 'itm-pie'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("item route must not be written on a replica: n=%d err=%v", n, err)
	}

	// GET /kitchen-stations itself is unaffected — a replica still
	// reports, it just never decides (ADR-0036's framing for the same
	// primary/replica split).
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/kitchen-stations", nil), manager)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, req)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /kitchen-stations on replica: code=%d", getRec.Code)
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

// Printer discovery (ut-docs#140): mirrors discovery_api_test.go's coverage
// of the analogous till-discovery endpoint — manager gating, the JSON
// response shape, empty-vs-null, and the error path.

func TestDiscoverPrintersAPI_RejectsNonManager(t *testing.T) {
	mux, _ := newKitchenStationsTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/kitchen-stations/discover-printers", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier discover-printers = %d, want 403", rec.Code)
	}
}

func TestDiscoverPrintersAPI_ReturnsCandidatesFound(t *testing.T) {
	mux, _ := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	stubBrowsePrinters(t, []discovery.PrinterCandidate{
		{Name: "Bar Printer", Address: "192.168.1.70:9100"},
	}, nil)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/kitchen-stations/discover-printers", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Printers []struct {
				Name    string `json:"name"`
				Address string `json:"address"`
			} `json:"printers"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Error != nil {
		t.Fatalf("expected error: null, got %v", out.Error)
	}
	if len(out.Data.Printers) != 1 || out.Data.Printers[0].Name != "Bar Printer" || out.Data.Printers[0].Address != "192.168.1.70:9100" {
		t.Fatalf("unexpected printers payload: %+v", out.Data.Printers)
	}
}

func TestDiscoverPrintersAPI_ReturnsEmptyArrayNotNullWhenNoneFound(t *testing.T) {
	mux, _ := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	stubBrowsePrinters(t, nil, nil)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/kitchen-stations/discover-printers", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"printers":[]`) {
		t.Fatalf("expected an empty array, not null, got body: %s", rec.Body.String())
	}
}

func TestDiscoverPrintersAPI_ScanFailureReturns500WithoutLeakingRawError(t *testing.T) {
	mux, _ := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	stubBrowsePrinters(t, nil, errors.New("write udp6 [::]:1234->[ff02::fb]:5353: sendto: no route to host"))

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/kitchen-stations/discover-printers", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sendto") {
		t.Fatalf("raw network error leaked into the response body: %s", rec.Body.String())
	}
}
