package pages

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2137. The sale screen's On hold strip is clipped off-screen on the
// pilot tablet (1280x800, ut-docs#2128) and /open-orders was read-only by
// design (ut-docs#1918), telling the cashier to "tap it on the On hold strip"
// — a control the device does not show. Net effect, reported by the product
// owner: three real parked orders, one open 221 minutes, with no way to pick
// any of them up.
//
// The fix is the product owner's own: a button next to Card on the sale
// screen that opens a popup listing the parked orders, each one tappable to
// resume. It depends on no strip and no viewport budget.

func parkedOrdersFragment(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/parked-orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/parked-orders = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestParkedOrders_EveryOrderIsAResumeControl(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	if _, err := d.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','Window 2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('h1','Table 4',1250,3,'{}','tbl-1',datetime('now')),
 ('h2','Sarah',500,1,'{}',NULL,datetime('now'))`); err != nil {
		t.Fatalf("seed held sales: %v", err)
	}
	body := parkedOrdersFragment(t, mux)
	// hx-vals is attribute-escaped by html/template (the browser unescapes it
	// before htmx ever sees it), so compare against the decoded form — same
	// shape the sale screen's own jsonVals attributes render in.
	decoded := html.UnescapeString(body)

	// One real, submitting control per parked order — not a div with a click
	// handler: the till is driven by finger, and must stay reachable by
	// keyboard and assistive tech.
	if n := strings.Count(body, `hx-post="/api/pos/resume"`); n != 2 {
		t.Fatalf("found %d resume controls, want one per parked order: %s", n, body)
	}
	if n := strings.Count(body, "<button"); n < 2 {
		t.Fatalf("resume controls must be real buttons, got: %s", body)
	}
	for _, want := range []string{
		`"id":"h1"`, `"id":"h2"`,
		`data-held-id="h1"`, `data-held-id="h2"`,
		"Table 4", "Sarah", "Window 2",
		httpx.FormatMoney(1250, "en"), httpx.FormatMoney(500, "en"),
		// Resuming replaces the live basket, same contract as the strip chip.
		`hx-target="#basket"`,
	} {
		if !strings.Contains(decoded, want) {
			t.Fatalf("expected %q in the parked-orders popup, got: %s", want, body)
		}
	}
	if strings.Contains(body, ">1250<") || strings.Contains(body, "tbl-1") {
		t.Fatalf("totals must be money-formatted and tables shown by label, got: %s", body)
	}
	// Oldest first, matching the strip and the page.
	if strings.Index(decoded, `"id":"h1"`) > strings.Index(decoded, `"id":"h2"`) {
		t.Fatalf("expected the older order first, got: %s", body)
	}
}

func TestParkedOrders_EmptyStateOffersNothingToResume(t *testing.T) {
	mux, _ := newOpenOrdersTestMux(t)
	body := parkedOrdersFragment(t, mux)
	if !strings.Contains(html.UnescapeString(body), httpx.T("en", "open_orders.empty")) {
		t.Fatalf("expected the empty-state copy, got: %s", body)
	}
	if strings.Contains(body, `hx-post="/api/pos/resume"`) {
		t.Fatalf("nothing is parked, so there must be no resume control: %s", body)
	}
}

// The popup is reached from the sale screen, so it must not need a fresh page
// load to be correct: it is fetched when opened, and a resume from it must
// re-render the basket the cashier is looking at.
func TestParkedOrders_ResumeFromThePopupLoadsTheBasket(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	holdMux := http.NewServeMux()
	registerHoldAPI(holdMux, d)

	if _, err := d.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdTestPost(holdMux, "/api/pos/hold", "label=Table+4")
	row := holdTestOnlyRow(t, d)
	if d.Engine.HasItems() {
		t.Fatal("precondition: parking should have emptied the live basket")
	}
	if body := parkedOrdersFragment(t, mux); !strings.Contains(body, `data-held-id="`+row.ID+`"`) {
		t.Fatalf("the parked order is not offered in the popup: %s", body)
	}

	// Exactly what tapping that entry does.
	holdTestPost(holdMux, "/api/pos/resume", "id="+row.ID)

	if !d.Engine.HasItems() {
		t.Fatal("tapping a popup entry did not restore the order into the basket")
	}
	if body := parkedOrdersFragment(t, mux); strings.Contains(body, `data-held-id="`+row.ID+`"`) {
		t.Fatal("the resumed order is still offered as parked")
	}
}

// The page's own hint sent the cashier to a control the tablet does not show.
func TestOpenOrdersPage_NoLongerPointsAtTheOffScreenStrip(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('h1','Table 4',1250,3,'{}',NULL,datetime('now'))`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	if body := openOrdersGet(t, mux); strings.Contains(body, httpx.T("en", "open_orders.hint")) {
		t.Fatal("the 'tap it on the On hold strip' hint must not survive: the " +
			"strip is clipped off-screen at 1280x800 (ut-docs#2128)")
	}
}
