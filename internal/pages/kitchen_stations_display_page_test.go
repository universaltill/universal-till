package pages

// Kitchen-stations admin page, Kitchen Display slice (universaltill/
// ut-docs#544): the create/edit forms carry a destination-type select
// (printer/display/both); a printer address is required only when the
// station prints; display-capable enabled stations link to their screen.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

func TestKitchenStationsPage_DestinationTypeCreateRules(t *testing.T) {
	mux, d := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// display-only: no address needed.
	rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Pass Screen"}, "destination_type": {"display"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/kitchen-stations" {
		t.Fatalf("create display-only without address: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var dt, addr string
	if err := d.Db.QueryRow(`SELECT destination_type, COALESCE(printer_address,'') FROM kitchen_stations WHERE name = 'Pass Screen'`).Scan(&dt, &addr); err != nil || dt != "display" || addr != "" {
		t.Fatalf("display station row: dt=%q addr=%q err=%v", dt, addr, err)
	}

	// both: address required, exactly like printer.
	rec = postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Grill"}, "destination_type": {"both"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.address_required" {
		t.Fatalf("create 'both' without address: loc=%q", rec.Header().Get("Location"))
	}
	rec = postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Grill"}, "destination_type": {"both"}, "printer_address": {"g:9100"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations" {
		t.Fatalf("create 'both' with address: loc=%q", rec.Header().Get("Location"))
	}
	if err := d.Db.QueryRow(`SELECT destination_type FROM kitchen_stations WHERE name = 'Grill'`).Scan(&dt); err != nil || dt != "both" {
		t.Fatalf("both station row: dt=%q err=%v", dt, err)
	}

	// Omitted destination_type keeps the pre-#544 default (printer), so the
	// existing address-required path is untouched.
	rec = postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Legacy"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.address_required" {
		t.Fatalf("create with no destination_type and no address: loc=%q", rec.Header().Get("Location"))
	}
	rec = postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Legacy"}, "printer_address": {"l:9100"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations" {
		t.Fatalf("create with no destination_type: loc=%q", rec.Header().Get("Location"))
	}
	if err := d.Db.QueryRow(`SELECT destination_type FROM kitchen_stations WHERE name = 'Legacy'`).Scan(&dt); err != nil || dt != "printer" {
		t.Fatalf("default destination: dt=%q err=%v", dt, err)
	}

	// Unknown type → its own error key, nothing written.
	rec = postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Bogus"}, "destination_type": {"hologram"}, "printer_address": {"x:9100"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.destination_invalid" {
		t.Fatalf("create with bogus destination_type: loc=%q", rec.Header().Get("Location"))
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM kitchen_stations WHERE name = 'Bogus'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("bogus station must not be created: n=%d err=%v", n, err)
	}
}

func TestKitchenStationsPage_DestinationTypeUpdateRules(t *testing.T) {
	mux, d := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Grill"}, "destination_type": {"printer"}, "printer_address": {"g:9100"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rec.Code)
	}
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM kitchen_stations WHERE name = 'Grill'`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	// Switch to display-only with a blank address: allowed.
	rec = postForm(mux, "/api/kitchen-stations/"+id, url.Values{"name": {"Grill Screen"}, "destination_type": {"display"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations" {
		t.Fatalf("update to display without address: loc=%q", rec.Header().Get("Location"))
	}
	var dt, name string
	if err := d.Db.QueryRow(`SELECT destination_type, name FROM kitchen_stations WHERE id = ?`, id).Scan(&dt, &name); err != nil || dt != "display" || name != "Grill Screen" {
		t.Fatalf("update: dt=%q name=%q err=%v", dt, name, err)
	}
	// Switch to both with a blank address: rejected, row unchanged.
	rec = postForm(mux, "/api/kitchen-stations/"+id, url.Values{"name": {"Grill"}, "destination_type": {"both"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.address_required" {
		t.Fatalf("update to both without address: loc=%q", rec.Header().Get("Location"))
	}
	if err := d.Db.QueryRow(`SELECT destination_type FROM kitchen_stations WHERE id = ?`, id).Scan(&dt); err != nil || dt != "display" {
		t.Fatalf("rejected update must not change the row: dt=%q err=%v", dt, err)
	}
	// Bogus type → its own error.
	rec = postForm(mux, "/api/kitchen-stations/"+id, url.Values{"name": {"Grill"}, "destination_type": {"laser"}, "printer_address": {"g:9100"}}, &manager)
	if rec.Header().Get("Location") != "/kitchen-stations?err=kitchenstations.error.destination_invalid" {
		t.Fatalf("update with bogus destination_type: loc=%q", rec.Header().Get("Location"))
	}
}

func TestKitchenStationsPage_RendersDestinationSelectAndViewDisplayLink(t *testing.T) {
	mux, d := newKitchenStationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// Empty page: the create form already carries the select.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/kitchen-stations", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d", rec.Code)
	}
	if !strings.Contains(body, `name="destination_type"`) {
		t.Fatal("create form must carry a destination_type select")
	}
	for _, v := range []string{`value="printer"`, `value="display"`, `value="both"`} {
		if !strings.Contains(body, v) {
			t.Fatalf("destination select must offer %s", v)
		}
	}
	if strings.Contains(body, "/kitchen-display/") {
		t.Fatal("no stations yet: no View display link expected")
	}

	// One of each: printer (no link), display (link), both (link), and a
	// disabled display (no link — the screen 404s for a disabled station).
	for _, s := range []struct{ name, dt, addr string }{
		{"Printer Only", "printer", "p:9100"},
		{"Pass Screen", "display", ""},
		{"Grill Both", "both", "g:9100"},
		{"Old Screen", "display", ""},
	} {
		if rec := postForm(mux, "/api/kitchen-stations", url.Values{"name": {s.name}, "destination_type": {s.dt}, "printer_address": {s.addr}}, &manager); rec.Header().Get("Location") != "/kitchen-stations" {
			t.Fatalf("create %s: loc=%q", s.name, rec.Header().Get("Location"))
		}
	}
	ids := map[string]string{}
	for _, name := range []string{"Printer Only", "Pass Screen", "Grill Both", "Old Screen"} {
		var id string
		if err := d.Db.QueryRow(`SELECT id FROM kitchen_stations WHERE name = ?`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	if rec := postForm(mux, "/api/kitchen-stations/"+ids["Old Screen"]+"/active", url.Values{"active": {"0"}}, &manager); rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate: %d", rec.Code)
	}

	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/kitchen-stations", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d", rec.Code)
	}
	if !strings.Contains(body, `href="/kitchen-display/`+ids["Pass Screen"]+`"`) {
		t.Fatal("enabled display station must link to its screen")
	}
	if !strings.Contains(body, `href="/kitchen-display/`+ids["Grill Both"]+`"`) {
		t.Fatal("enabled 'both' station must link to its screen")
	}
	if strings.Contains(body, `href="/kitchen-display/`+ids["Printer Only"]+`"`) {
		t.Fatal("printer-only station must not link to a screen")
	}
	if strings.Contains(body, `href="/kitchen-display/`+ids["Old Screen"]+`"`) {
		t.Fatal("disabled display station must not link to a screen")
	}
	if !strings.Contains(body, "View display") {
		t.Fatal("link text must be the translated View display label")
	}
	// The per-row edit form pre-selects the station's current type.
	if !strings.Contains(body, `value="both" selected`) || !strings.Contains(body, `value="display" selected`) {
		t.Fatal("edit forms must pre-select each station's current destination type")
	}
}
