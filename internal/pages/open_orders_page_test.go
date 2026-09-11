package pages

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
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
