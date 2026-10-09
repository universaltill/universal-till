package pages

import (
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2702 review: the compact tender panel dropped the held-sales strip,
// and with it the only UI that reached POST /api/pos/held/table -- moving a
// parked order to another table (ut-docs#820/#1381/#1704). The Open orders
// popup now carries that control on each row.

func seedMoveTableFixture(t *testing.T) (*http.ServeMux, *http.ServeMux, func(q string) string) {
	t.Helper()
	mux, d := newOpenOrdersTestMux(t)
	holdMux := http.NewServeMux()
	registerHoldAPI(holdMux, d)
	if _, err := d.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES
 ('tbl-1','Window 2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-2','Patio 5','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('tbl-3','Bar 1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed tables: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('h1','Table 4',1250,3,'{}','tbl-1',datetime('now','-2 minutes')),
 ('h2','Sarah',500,1,'{"order_type":"takeaway"}',NULL,datetime('now','-1 minutes')),
 ('h3','Group',700,2,'{}','tbl-3',datetime('now'))`); err != nil {
		t.Fatalf("seed held sales: %v", err)
	}
	tableOf := func(id string) string {
		var tid string
		if err := d.Db.QueryRow(`SELECT COALESCE(table_id,'') FROM held_sales WHERE id=?`, id).Scan(&tid); err != nil {
			t.Fatalf("read table_id of %s: %v", id, err)
		}
		return tid
	}
	return mux, holdMux, tableOf
}

// rowHTML cuts one popup <li> out of the fragment by its held id.
func rowHTML(t *testing.T, body, id string) string {
	t.Helper()
	i := strings.Index(body, `data-held-id="`+id+`"`)
	if i < 0 {
		t.Fatalf("row %s not in popup: %s", id, body)
	}
	start := strings.LastIndex(body[:i], "<li")
	end := strings.Index(body[i:], "</li>")
	return body[start : i+end]
}

func TestParkedOrders_RowOffersMoveTableToFreeTablesOnly(t *testing.T) {
	mux, _, _ := seedMoveTableFixture(t)
	body := html.UnescapeString(parkedOrdersFragment(t, mux))

	h1 := rowHTML(t, body, "h1")
	if !strings.Contains(h1, `hx-post="/api/pos/held/table"`) {
		t.Fatalf("row h1 has no Move table control: %s", h1)
	}
	if !strings.Contains(h1, httpx.T("en", "basket.table.move")) {
		t.Fatalf("Move table control is unlabelled: %s", h1)
	}
	// Free table offered; own table and a table another parked order holds are not.
	if !strings.Contains(h1, `"table_id":"tbl-2"`) || !strings.Contains(h1, "Patio 5") {
		t.Fatalf("free table Patio 5 not offered: %s", h1)
	}
	for _, bad := range []string{`"table_id":"tbl-1"`, `"table_id":"tbl-3"`} {
		if strings.Contains(h1, bad) {
			t.Fatalf("row h1 must not offer %s (own/occupied): %s", bad, h1)
		}
	}
	// The move re-renders the popup body in place, not the removed strip.
	for _, want := range []string{`"view":"parked-orders"`, `hx-target="#parked-orders-body"`} {
		if !strings.Contains(h1, want) {
			t.Fatalf("move option missing %s: %s", want, h1)
		}
	}
	// Accessible name names the order, so several rows' controls are distinguishable.
	if !strings.Contains(h1, "Table 4") {
		t.Fatalf("move control should name its order: %s", h1)
	}

	// A takeaway order gets no table control (ut-docs#1381).
	if h2 := rowHTML(t, body, "h2"); strings.Contains(h2, "/api/pos/held/table") {
		t.Fatalf("takeaway order must not offer Move table: %s", h2)
	}
}

func TestParkedOrders_NoMoveControlWithoutTables(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload) VALUES ('h1','Sarah',500,1,'{}')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if body := parkedOrdersFragment(t, mux); strings.Contains(body, "/api/pos/held/table") {
		t.Fatalf("a shop with no tables must not get a Move table control: %s", body)
	}
}

// ut-docs#3325 regression (e2e/tests/held-table-move-2702.spec.ts's third
// case): html/template strips a leading "data-" before sniffing an
// attribute's escaping context (html/template/context.go attrType), so the
// ORIGINAL attribute name here, "data-on-before-request", was treated as a
// JS-event attribute (it starts with "on" once "data-" is gone) and got
// JS-string escaping, not plain attribute escaping -- a templated id inside
// its value came out JSON-quoted. remember-focus:parked-orders-body,parked-
// move-{{ $id }} rendered as ...,parked-move-&#34;<id>&#34;, so
// restore-focus's later byId() lookup never matched the real
// id="parked-move-<id>" summary (no quotes), and focus silently stayed on
// <body>. Fixed by renaming the whole data-on-* family to data-* (dropping
// the "on-" infix, e.g. data-before-request) so stripping "data-" no longer
// leaves anything starting with "on" -- see web/public/inline-actions.js's
// header. This test pins the fix generally: the never-JS-context id=
// attribute on the <summary> itself is ground truth for the real id, and
// data-before-request's copy of it must match byte for byte, whatever it
// gets renamed to next.
func TestParkedOrders_MoveOptionRememberFocusIDNotJSEscaped(t *testing.T) {
	mux, _, _ := seedMoveTableFixture(t)
	body := parkedOrdersFragment(t, mux)
	h1 := rowHTML(t, body, "h1")

	start := strings.Index(h1, `id="parked-move-`)
	if start < 0 {
		t.Fatalf("no Move table toggle in row h1: %s", h1)
	}
	end := strings.Index(h1[start:], `"`+`>`)
	realID := h1[start+len(`id="`) : start+end]
	if strings.ContainsAny(realID, `"&`) {
		t.Fatalf("sanity check failed, got a suspicious id %q", realID)
	}

	wantFragment := "remember-focus:parked-orders-body," + realID
	if !strings.Contains(h1, wantFragment) {
		t.Fatalf("the before-request remember-focus hook must reference the SAME id (%q) the real <summary> carries, "+
			"byte for byte -- row h1: %s", wantFragment, h1)
	}
}

func TestHeldTableMove_FromPopupReRendersPopup(t *testing.T) {
	_, holdMux, tableOf := seedMoveTableFixture(t)
	rec := holdTestPost(holdMux, "/api/pos/held/table", "id=h1&table_id=tbl-2&view=parked-orders")
	if rec.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", rec.Code, rec.Body.String())
	}
	if got := tableOf("h1"); got != "tbl-2" {
		t.Fatalf("h1 table after move = %q, want tbl-2", got)
	}
	if rec.Header().Get("HX-Trigger") != "held-changed" {
		t.Fatalf("expected HX-Trigger held-changed (badge refresh), got %q", rec.Header().Get("HX-Trigger"))
	}
	body := html.UnescapeString(rec.Body.String())
	if strings.Contains(body, `id="held-sales"`) {
		t.Fatalf("popup move must re-render the popup, not the removed strip: %s", body)
	}
	if !strings.Contains(rowHTML(t, body, "h1"), "Patio 5") {
		t.Fatalf("row h1 should now show Patio 5: %s", body)
	}
}

func TestHeldTableMove_FromPopupOccupiedShowsToast(t *testing.T) {
	_, holdMux, tableOf := seedMoveTableFixture(t)
	// tbl-3 is held by h3 -- e.g. a stale popup offered it before another till took it.
	rec := holdTestPost(holdMux, "/api/pos/held/table", "id=h1&table_id=tbl-3&view=parked-orders")
	if rec.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", rec.Code, rec.Body.String())
	}
	if got := tableOf("h1"); got != "tbl-1" {
		t.Fatalf("rejected move changed h1 to %q", got)
	}
	if rec.Header().Get("HX-Trigger") != "" {
		t.Fatalf("a refused move must not fire held-changed, got %q", rec.Header().Get("HX-Trigger"))
	}
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, httpx.T("en", "basket.table.occupied")) || !strings.Contains(body, `role="alert"`) {
		t.Fatalf("expected the occupied toast in the popup: %s", body)
	}
	if !strings.Contains(body, `data-held-id="h1"`) {
		t.Fatalf("popup list should still render under the toast: %s", body)
	}
}

// ut-docs#3630: a movable row carries the invisible placeholder immediately
// before its <details>, so column 2 keeps the toggle's width when the open
// panel spans the row; a row without moves still gets one (column alignment).
func TestParkedOrders_MovableRowRendersPlaceholderBeforeDetails(t *testing.T) {
	mux, _, _ := seedMoveTableFixture(t)
	body := html.UnescapeString(parkedOrdersFragment(t, mux))

	const ph = `class="btn secondary parked-move-placeholder" aria-hidden="true"`
	h1 := rowHTML(t, body, "h1")
	if n := strings.Count(h1, "parked-move-placeholder"); n != 1 {
		t.Fatalf("movable row h1 should carry exactly one placeholder, got %d: %s", n, h1)
	}
	pi, di := strings.Index(h1, ph), strings.Index(h1, `<details class="parked-move"`)
	if pi < 0 || di < 0 || pi > di {
		t.Fatalf("placeholder must precede the <details> (ph=%d details=%d): %s", pi, di, h1)
	}
	if between := h1[pi:di]; strings.Count(between, "<") != 1 { // only the placeholder's own </span>
		t.Fatalf("placeholder must sit immediately before <details>: %q", between)
	}
	if h2 := rowHTML(t, body, "h2"); !strings.Contains(h2, ph) || strings.Contains(h2, "<details") {
		t.Fatalf("row without moves keeps a lone placeholder: %s", h2)
	}
}
