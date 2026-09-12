package pages

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// newOpenOrdersTestMux wires registerOpenOrders over the same hand-rolled
// held_sales/tables schema newHoldTestDeps uses (the page reads exactly
// those two tables), plus the Menu/Settings the base layout renders.
func newOpenOrdersTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	_, hold := newHoldTestDeps(t)
	d := &common.Deps{
		Db:       hold.Db,
		Engine:   hold.Engine,
		State:    common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Settings: settings.NewStore(hold.Db),
		Menu:     []common.MenuItem{{Href: "/", Label: "nav.till"}},
	}
	mux := http.NewServeMux()
	registerOpenOrders(mux, d)
	return mux, d
}

func openOrdersGet(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open-orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /open-orders = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestOpenOrdersPage_EmptyState (ut-docs#1918): with nothing held the page
// renders the friendly empty-state copy through the base layout (nav rail
// present, so a kiosk always has a way back), and no table at all.
func TestOpenOrdersPage_EmptyState(t *testing.T) {
	mux, _ := newOpenOrdersTestMux(t)
	body := openOrdersGet(t, mux)
	if !strings.Contains(body, httpx.T("en", "open_orders.title")) {
		t.Fatalf("expected the page title, got: %s", body)
	}
	// HTMLEscapeString: the copy carries an apostrophe, which html/template
	// renders as &#39;.
	if !strings.Contains(body, template.HTMLEscapeString(httpx.T("en", "open_orders.empty"))) {
		t.Fatalf("expected the empty-state copy, got: %s", body)
	}
	if strings.Contains(body, `class="table"`) || strings.Contains(body, "data-held-id") {
		t.Fatalf("expected no rows table in the empty state, got: %s", body)
	}
	if !strings.Contains(body, `class="nav"`) {
		t.Fatalf("expected the base layout's nav rail, got: %s", body)
	}
}

// TestOpenOrdersPage_ListsHeldSalesWithTableTotalAndAge (ut-docs#1918): one
// row per held sale -- label, the table's CURRENT label (resolved from the
// tables table, not whatever the strip snapshotted), item count, a
// locale-formatted money total (never a raw minor-unit integer), and
// whole minutes since the order was first parked.
func TestOpenOrdersPage_ListsHeldSalesWithTableTotalAndAge(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	if _, err := d.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-1','Window 2','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	parkedAt := time.Now().UTC().Add(-90 * time.Minute).Format("2006-01-02 15:04:05")
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('h1','Table 4',1250,3,'{}','tbl-1',?),
 ('h2','Sarah',500,1,'{}',NULL,datetime('now'))`, parkedAt); err != nil {
		t.Fatalf("seed held sales: %v", err)
	}
	body := openOrdersGet(t, mux)
	for _, want := range []string{
		`data-held-id="h1"`, `data-held-id="h2"`,
		"Table 4", "Sarah", "Window 2",
		httpx.FormatMoney(1250, "en"), httpx.FormatMoney(500, "en"),
		httpx.T("en", "open_orders.col.table"), httpx.T("en", "open_orders.col.age"),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in the open-orders page, got: %s", want, body)
		}
	}
	if strings.Contains(body, ">1250<") || strings.Contains(body, "tbl-1") {
		t.Fatalf("totals must be money-formatted and tables shown by label, got: %s", body)
	}
	// 90 minutes ago, allowing the test's own clock to tick over one minute.
	if !strings.Contains(body, strings.Replace(httpx.T("en", "open_orders.age_minutes"), "%d", "90", 1)) &&
		!strings.Contains(body, strings.Replace(httpx.T("en", "open_orders.age_minutes"), "%d", "91", 1)) {
		t.Fatalf("expected the h1 row to show ~90 min open, got: %s", body)
	}
	if strings.Contains(body, template.HTMLEscapeString(httpx.T("en", "open_orders.empty"))) {
		t.Fatalf("empty-state copy must not render alongside rows, got: %s", body)
	}
	// Oldest first (repo.List orders by created_at), matching the strip.
	if strings.Index(body, `data-held-id="h1"`) > strings.Index(body, `data-held-id="h2"`) {
		t.Fatalf("expected the older order first, got: %s", body)
	}
}

// TestOpenOrdersPage_RowsAreRealResumeButtons (ut-docs#2138): each row must
// be a real, focusable control -- a <form>+<button> -- not a <tr> with a JS
// click handler and no keyboard/assistive-tech equivalent (ut-docs#826's
// accessibility gate).
func TestOpenOrdersPage_RowsAreRealResumeButtons(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('h1','Table 4',1250,3,'{}',NULL,datetime('now'))`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	body := openOrdersGet(t, mux)
	for _, want := range []string{
		`<form method="post" action="/open-orders/resume">`,
		`<button type="submit" class="open-order-row-btn"`,
		`<input type="hidden" name="id" value="h1">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in the open-orders page, got: %s", want, body)
		}
	}
	// ut-docs#2138 review: the original assertion here looked for the exact
	// literal `<tr data-held-id="h1" onclick`, a string this template has
	// never produced in any version -- it passed against the broken page and
	// the fixed one alike, so it pinned nothing. What actually has to hold is
	// that resuming depends on NO script at all: no inline handler and no
	// htmx attribute anywhere in the rows, so the row still resumes with
	// JavaScript off (that, not the tag name, is what ut-docs#826's gate is
	// about). Checked over the <tbody> only, so the base layout's own
	// scripts/htmx wiring can't satisfy or break it.
	tbody := body[strings.Index(body, "<tbody>"):strings.Index(body, "</tbody>")]
	for _, banned := range []string{"onclick", "onmousedown", "ontouchstart", "hx-post", "hx-get", "data-record-open"} {
		if strings.Contains(tbody, banned) {
			t.Fatalf("a row must resume without script, found %q in the rows: %s", banned, tbody)
		}
	}
	// And the control must be the row itself, not a widget tucked into one
	// cell: one <td colspan> spanning every column, holding the button.
	if !strings.Contains(tbody, `<td colspan="5">`) {
		t.Fatalf("expected the whole row to be the control (one full-width cell), got: %s", tbody)
	}
}

// TestOpenOrdersResume_SuccessRedirectsToSaleScreenWithBasketLoaded
// (ut-docs#2138): tapping a row on THIS page (not the sale-screen popup)
// resumes the order and sends the cashier to the sale screen, sharing the
// same resumeHeldSale logic (hold_api.go) the popup's POST /api/pos/resume
// uses -- so the ut-docs#820 table re-resolution and ut-docs#1390 claim
// handling exist in exactly one place.
func TestOpenOrdersResume_SuccessRedirectsToSaleScreenWithBasketLoaded(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	holdMux := http.NewServeMux()
	registerHoldAPI(holdMux, d)
	if _, err := d.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdTestPost(holdMux, "/api/pos/hold", "label=Table+4")
	row := holdTestOnlyRow(t, d)

	rec := holdTestPost(mux, "/open-orders/resume", "id="+row.ID)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /open-orders/resume = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("expected redirect to the sale screen (\"/\"), got %q", got)
	}
	if !d.Engine.HasItems() {
		t.Fatal("expected the resumed order's basket to be loaded onto the engine")
	}
	rows, err := data.NewHeldSalesRepo(d.Db).List(context.Background())
	if err != nil {
		t.Fatalf("list held_sales: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected the resumed order to leave held_sales, got %+v", rows)
	}
}

// TestOpenOrdersResume_BusyRefusalKeepsOrderParkedAndListed (ut-docs#2138's
// own acceptance criterion): with the live basket already busy, tapping a
// row is refused with the existing hold.error.busy message, and the order
// stays parked and listed -- exactly the pre-#2138 rule, just reachable from
// a new place.
func TestOpenOrdersResume_BusyRefusalKeepsOrderParkedAndListed(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('h1','Table 4',1250,3,'{}',NULL,datetime('now'))`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	if _, err := d.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed a live basket: %v", err)
	}
	rec := holdTestPost(mux, "/open-orders/resume", "id=h1")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /open-orders/resume = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/open-orders?err=hold.error.busy" {
		t.Fatalf("expected the busy refusal to redirect back with the error, got %q", got)
	}
	body := openOrdersGet(t, mux)
	if !strings.Contains(body, `data-held-id="h1"`) {
		t.Fatalf("expected the refused order to stay parked and listed, got: %s", body)
	}
}

// TestOpenOrdersPage_ShowsErrBanner (ut-docs#2138): the ?err= query string
// the resume route's busy redirect carries must actually render, same
// "login-error" banner convention country_settings_page.go's renderPage
// already uses for this shape.
func TestOpenOrdersPage_ShowsErrBanner(t *testing.T) {
	mux, _ := newOpenOrdersTestMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open-orders?err=hold.error.busy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /open-orders?err=... = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), httpx.T("en", "hold.error.busy")) {
		t.Fatalf("expected the busy error banner, got: %s", rec.Body.String())
	}
}

// TestOpenOrdersPage_AgeCountsFromFirstPark (ut-docs#1918): through the real
// hold/resume handlers -- park, resume, re-park -- the row's "open for"
// must keep counting from the FIRST park, not reset on the re-park.
func TestOpenOrdersPage_AgeCountsFromFirstPark(t *testing.T) {
	mux, d := newOpenOrdersTestMux(t)
	holdMux := http.NewServeMux()
	registerHoldAPI(holdMux, d)
	if _, err := d.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	holdTestPost(holdMux, "/api/pos/hold", "label=Table+4")
	first := holdTestOnlyRow(t, d)
	backdated := time.Now().UTC().Add(-45 * time.Minute).Format("2006-01-02 15:04:05")
	if _, err := d.Db.Exec(`UPDATE held_sales SET created_at = ? WHERE id = ?`, backdated, first.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	holdTestPost(holdMux, "/api/pos/resume", "id="+first.ID)
	holdTestPost(holdMux, "/api/pos/hold", "")

	body := openOrdersGet(t, mux)
	if !strings.Contains(body, `data-held-id="`+first.ID+`"`) {
		t.Fatalf("expected the re-parked order under its original id, got: %s", body)
	}
	want45 := strings.Replace(httpx.T("en", "open_orders.age_minutes"), "%d", strconv.Itoa(45), 1)
	want46 := strings.Replace(httpx.T("en", "open_orders.age_minutes"), "%d", strconv.Itoa(46), 1)
	if !strings.Contains(body, want45) && !strings.Contains(body, want46) {
		t.Fatalf("expected the age to keep counting from the first park (~45 min), got: %s", body)
	}
}
