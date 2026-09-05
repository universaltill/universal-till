package pages

// Kitchen Display, HDMI-local slice (universaltill/ut-docs#544): a
// per-station live order board at /kitchen-display/{station_id}, built on
// the /orders board's own fragment/poll/SSE/one-tap mechanism. Session-
// authed, no manager gate (floor work, like /orders).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// kitchenDisplayFixture wires the display page + the existing order-status
// surface (the one-tap POST the screen reuses) against a migrated DB seeded
// with two categories and three items.
type kitchenDisplayFixture struct {
	mux   *http.ServeMux
	dp    *common.Deps
	dbase *db.DB
	repo  *data.POSRepo
}

func newKitchenDisplayFixture(t *testing.T) *kitchenDisplayFixture {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "kitchen-display.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dbase.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO categories (id, name) VALUES ('cat-food','Food'), ('cat-drinks','Drinks')`)
	mustExec(`INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES
		('itm-steak','STEAK','Steak',1500,'cat-food',1),
		('itm-cola','COLA','Cola',250,'cat-drinks',1),
		('itm-bread','BREAD','Bread',200,'cat-food',1)`)
	dp := &common.Deps{Db: dbase.DB, OrderStatus: pos.NewOrderStatusBroadcaster(), Settings: settings.NewStore(dbase.DB)}
	mux := http.NewServeMux()
	registerOrderStatus(mux, dp)
	registerKitchenDisplay(mux, dp)
	return &kitchenDisplayFixture{mux: mux, dp: dp, dbase: dbase, repo: data.NewPOSRepo(dbase.DB)}
}

func (f *kitchenDisplayFixture) station(t *testing.T, name, dt, addr string) string {
	t.Helper()
	id, err := f.repo.CreateKitchenStation(context.Background(), name, dt, addr)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *kitchenDisplayFixture) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func TestKitchenDisplayPage_RendersForDisplayCapableStations(t *testing.T) {
	f := newKitchenDisplayFixture(t)
	screen := f.station(t, "Pass Screen", data.KitchenDestinationDisplay, "")
	both := f.station(t, "Grill & Screen", data.KitchenDestinationBoth, "g:9100")

	for _, id := range []string{screen, both} {
		rec := f.get("/kitchen-display/" + id)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /kitchen-display/%s = %d, want 200 (body %q)", id, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		// Mirrors orders.html: loads the station fragment once, then the
		// fragment's own root re-arms the poll; SSE nudges via the SAME
		// /api/orders/stream and the SAME orders-push trigger.
		if !strings.Contains(body, `hx-get="/ui/kitchen-display/`+id+`"`) {
			t.Fatalf("page must load its station fragment, got %q", body)
		}
		if !strings.Contains(body, `new EventSource('/api/orders/stream')`) || !strings.Contains(body, `'orders-push'`) {
			t.Fatalf("page must reuse the order-status SSE stream and the orders-push nudge, got %q", body)
		}
	}
	// The station's shop-entered name is in the heading, HTML-escaped.
	body := f.get("/kitchen-display/" + both).Body.String()
	if !strings.Contains(body, "Grill &amp; Screen") {
		t.Fatalf("page must show the station name, got %q", body)
	}
	if !strings.Contains(body, "Kitchen display") {
		t.Fatalf("page must carry the translated title, got %q", body)
	}
}

func TestKitchenDisplayPage_404ForMissingDisabledOrPrinterOnly(t *testing.T) {
	f := newKitchenDisplayFixture(t)
	printer := f.station(t, "Grill", data.KitchenDestinationPrinter, "g:9100")
	disabled := f.station(t, "Old Screen", data.KitchenDestinationDisplay, "")
	if err := f.repo.SetKitchenStationEnabled(context.Background(), disabled, false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"nope", printer, disabled} {
		for _, prefix := range []string{"/kitchen-display/", "/ui/kitchen-display/"} {
			rec := f.get(prefix + id)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET %s%s = %d, want 404", prefix, id, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "display") {
				t.Fatalf("404 body must be the translated explanation, got %q", rec.Body.String())
			}
		}
	}
	// The page-route 404 renders through the layout (a way back on a pinned
	// kiosk, ut-docs#1455), the fragment 404 is a bare localized body.
	if !strings.Contains(f.get("/kitchen-display/nope").Body.String(), "<html") {
		t.Fatal("page 404 must render the full layout, not bare text")
	}
	if strings.Contains(f.get("/ui/kitchen-display/nope").Body.String(), "<html") {
		t.Fatal("fragment 404 must be a bare body that swaps into the page")
	}
}

func TestKitchenDisplayFragment_ListsOnlyThisStationsOrders(t *testing.T) {
	f := newKitchenDisplayFixture(t)
	ctx := context.Background()
	grill := f.station(t, "Grill", data.KitchenDestinationDisplay, "")
	bar := f.station(t, "Bar", data.KitchenDestinationBoth, "b:9100")
	if err := f.repo.SetCategoryStationRoutes(ctx, "cat-food", []string{grill}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatal(err)
	}
	seedKitchenSale(t, f.dbase, "R-steak", "itm-steak")
	seedKitchenSale(t, f.dbase, "R-cola", "itm-cola")
	seedKitchenSale(t, f.dbase, "R-both", "itm-bread", "itm-cola")

	rec := f.get("/ui/kitchen-display/" + grill)
	if rec.Code != http.StatusOK {
		t.Fatalf("fragment = %d (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"R-steak", "R-both"} {
		if !strings.Contains(body, want) {
			t.Fatalf("grill fragment must list %s, got %q", want, body)
		}
	}
	if strings.Contains(body, "R-cola") {
		t.Fatalf("grill fragment must not list the bar-only order, got %q", body)
	}
	// Same partial, same row shape: the one-tap buttons post to the
	// EXISTING status endpoint, and the root re-arms the poll + SSE nudge
	// against THIS station's fragment URL.
	if !strings.Contains(body, `hx-post="/api/orders/R-steak/status"`) {
		t.Fatalf("rows must carry the shared one-tap buttons, got %q", body)
	}
	if !strings.Contains(body, `hx-get="/ui/kitchen-display/`+grill+`"`) || !strings.Contains(body, `hx-trigger="every 15s, orders-push from:body"`) {
		t.Fatalf("fragment root must re-arm the poll and SSE nudge on its own URL, got %q", body)
	}
	if !strings.Contains(body, `<a href="/journal/R-steak">`) {
		t.Fatalf("local orders are always journal-linkable, got %q", body)
	}

	bar1 := f.get("/ui/kitchen-display/" + bar).Body.String()
	if !strings.Contains(bar1, "R-cola") || !strings.Contains(bar1, "R-both") || strings.Contains(bar1, "R-steak") {
		t.Fatalf("bar fragment must list exactly the cola orders, got %q", bar1)
	}
}

func TestKitchenDisplayFragment_EmptyStateAndTerminalTapClearsBoth(t *testing.T) {
	f := newKitchenDisplayFixture(t)
	ctx := context.Background()
	grill := f.station(t, "Grill", data.KitchenDestinationDisplay, "")
	bar := f.station(t, "Bar", data.KitchenDestinationDisplay, "")
	if err := f.repo.SetCategoryStationRoutes(ctx, "cat-food", []string{grill}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatal(err)
	}

	body := f.get("/ui/kitchen-display/" + grill).Body.String()
	if !strings.Contains(body, "No orders for this station") {
		t.Fatalf("empty station must show its own empty state, got %q", body)
	}

	// A split order shows on both screens; advancing it to a terminal
	// status on either clears it from both (status is per ORDER — the v1
	// limitation the page documents).
	seedKitchenSale(t, f.dbase, "R-split", "itm-steak", "itm-cola")
	for _, id := range []string{grill, bar} {
		if b := f.get("/ui/kitchen-display/" + id).Body.String(); !strings.Contains(b, "R-split") {
			t.Fatalf("split order must show on station %s, got %q", id, b)
		}
	}
	if rec := postOrderStatus(f.mux, "R-split", "collected"); rec.Code != http.StatusOK {
		t.Fatalf("one-tap collected = %d", rec.Code)
	}
	for _, id := range []string{grill, bar} {
		if b := f.get("/ui/kitchen-display/" + id).Body.String(); strings.Contains(b, "R-split") {
			t.Fatalf("collected order must leave station %s, got %q", id, b)
		}
	}
}
