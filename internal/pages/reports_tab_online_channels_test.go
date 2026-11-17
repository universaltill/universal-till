package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5427 (ADR-0169 §9): Reports → Online channels, read from the
// channel_orders table. These are web shop / marketplace orders, never till
// sales.

func seedChannelOrderRow(t *testing.T, d *sql.DB, id, plugin, cur, placedAt, status, pay string, total, refund int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO channel_orders(id, connection_id, plugin_key, external_order_id, revision, placed_at,
  currency, status, payment_state, total_minor, refund_total_minor, created_at, updated_at)
VALUES(?, 'conn-'||?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, id, plugin, "ext-"+id, placedAt, cur, status, pay, total, refund, placedAt, placedAt); err != nil {
		t.Fatalf("seed channel order %s: %v", id, err)
	}
}

func seedOctoberChannelOrders(t *testing.T, d *sql.DB) {
	t.Helper()
	seedChannelOrderRow(t, d, "a", "com.x.shop", "GBP", "2026-10-05T10:00:00Z", "new", "paid", 5000, 0)
	seedChannelOrderRow(t, d, "b", "com.x.shop", "GBP", "2026-10-06T10:00:00Z", "new", "partially_refunded", 3000, 1000)
	seedChannelOrderRow(t, d, "c", "com.x.market", "GBP", "2026-10-07T10:00:00Z", "new", "paid", 2000, 0)
	seedChannelOrderRow(t, d, "e", "com.x.shop", "EUR", "2026-10-08T10:00:00Z", "new", "paid", 4000, 0)
}

const onlineChannelsQuery = "?period=month&anchor=2026-10-15"

func TestReportsOnlineChannelsTab_RendersPerChannelAndCurrency(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	seedOctoberChannelOrders(t, dp.Db)

	rec := getReportsTab(t, mux, "online-channels", onlineChannelsQuery)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if n := strings.Count(body, `data-testid="online-channels-row"`); n != 3 {
		t.Fatalf("rows = %d, want 3 (2 GBP + 1 EUR): %s", n, body)
	}
	if n := strings.Count(body, `data-testid="online-channels-total"`); n != 1 {
		t.Fatalf("total rows = %d, want exactly 1 (GBP only)", n)
	}
	money := httpx.FuncsFor("en")["moneyIn"].(func(any, string) string)
	for _, w := range []string{money(int64(9000), "GBP"), money(int64(4000), "EUR"), money(int64(1000), "GBP")} {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q in %s", w, body)
		}
	}
	if !strings.Contains(body, httpx.T("en", "reports.online_channels.notice")) {
		t.Fatalf("notice missing: %s", body)
	}
	if !strings.Contains(body, `href="/channel-orders"`) {
		t.Fatalf("link to /channel-orders missing")
	}
	// UX A1: all six cells stay on a phone card; A5: plain-word headers.
	if !strings.Contains(body, `data-cards-collapse="off"`) {
		t.Fatalf("table must opt out of the More collapse (UX A1)")
	}
	for _, w := range []string{"Order value", "After refunds"} {
		if !strings.Contains(body, w) {
			t.Fatalf("missing header %q", w)
		}
	}
}

func TestReportsOnlineChannelsTab_ForbiddenWithoutReports(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	seedOctoberChannelOrders(t, dp.Db)

	do := func(path, role string) *httptest.ResponseRecorder {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), auth.User{ID: "c1", Role: role})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("/ui/reports/tab/online-channels"+onlineChannelsQuery, "cashier"); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier fragment = %d, want 403", rec.Code)
	}
	rec := do("/reports"+onlineChannelsQuery, "cashier")
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "report-tab-online-channels") {
		t.Fatalf("cashier /reports = %d", rec.Code)
	}
	if rec := do("/ui/reports/tab/online-channels"+onlineChannelsQuery, "manager"); rec.Code != http.StatusOK {
		t.Fatalf("manager fragment = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReportsPage_OnlineChannelsTabHiddenWithoutOrders(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)

	body := getReportsPage(t, mux, "?days=30").Body.String()
	if strings.Contains(body, "report-tab-online-channels") {
		t.Fatalf("tab must be hidden when the till has no channel orders")
	}

	seedChannelOrderRow(t, dp.Db, "z", "com.x.shop", "GBP", "2020-01-01T10:00:00Z", "new", "paid", 100, 0)
	body = getReportsPage(t, mux, "?days=30").Body.String()
	want := `hx-get="/ui/reports/tab/online-channels?days=30"`
	if !strings.Contains(body, want) {
		t.Fatalf("missing %s", want)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "report-tab-online-channels") && strings.Contains(line, "hx-trigger") {
			t.Fatalf("tab button must not auto-fire: %s", line)
		}
	}
	// UX A4: last in the strip, after Payments & channels and shrinkage.
	if strings.Index(body, "report-tab-online-channels") < strings.Index(body, `id="report-tab-payments"`) {
		t.Fatalf("online channels tab must come after payments")
	}
	if i := strings.Index(body, `id="report-tab-shrinkage"`); i >= 0 && strings.Index(body, "report-tab-online-channels") < i {
		t.Fatalf("online channels tab must be last (after shrinkage)")
	}
}

func TestReportsOnlineChannelsTab_EmptyPeriodShowsNoticeAndLink(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	seedChannelOrderRow(t, dp.Db, "s", "com.x.shop", "GBP", "2026-09-10T10:00:00Z", "new", "paid", 100, 0)

	body := getReportsTab(t, mux, "online-channels", onlineChannelsQuery).Body.String()
	if !strings.Contains(body, `data-testid="online-channels-empty"`) || !strings.Contains(body, "No online orders to count in this period.") {
		t.Fatalf("empty state missing: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "reports.online_channels.notice")) || !strings.Contains(body, `href="/channel-orders"`) {
		t.Fatalf("notice/link missing")
	}
	if strings.Contains(body, "<table") {
		t.Fatalf("no table expected")
	}
}

func TestReportsOnlineChannelsTab_CancelledUnpaidLine(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	seedChannelOrderRow(t, dp.Db, "v", "com.x.shop", "GBP", "2026-10-05T10:00:00Z", "cancelled", "voided", 7777, 0)
	seedChannelOrderRow(t, dp.Db, "p", "com.x.shop", "GBP", "2026-10-06T10:00:00Z", "new", "paid", 5000, 0)

	body := getReportsTab(t, mux, "online-channels", onlineChannelsQuery).Body.String()
	if !strings.Contains(body, `data-testid="online-channels-cancelled"`) || !strings.Contains(body, "1 order cancelled before payment is not counted.") {
		t.Fatalf("cancelled line missing: %s", body)
	}
	money := httpx.FuncsFor("en")["moneyIn"].(func(any, string) string)
	if !strings.Contains(body, money(int64(5000), "GBP")) || strings.Contains(body, money(int64(7777), "GBP")) || strings.Contains(body, money(int64(12777), "GBP")) {
		t.Fatalf("gross must be the paid order only: %s", body)
	}
}

func TestReportsOnlineChannelsTab_AllCancelledUnpaidShowsBothLines(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	seedChannelOrderRow(t, dp.Db, "v", "com.x.shop", "GBP", "2026-10-05T10:00:00Z", "cancelled", "pending", 7777, 0)
	seedChannelOrderRow(t, dp.Db, "w", "com.x.shop", "GBP", "2026-10-06T10:00:00Z", "cancelled", "", 100, 0)

	body := getReportsTab(t, mux, "online-channels", onlineChannelsQuery).Body.String()
	for _, w := range []string{"online-channels-empty", "2 orders cancelled before payment are not counted."} {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q: %s", w, body)
		}
	}
}

func TestReportsOnlineChannelsTab_BusinessDayStartBoundary(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	if err := dp.Settings.Set(t.Context(), keyReportsBusinessDayStart, "04:00"); err != nil {
		t.Fatal(err)
	}
	d := time.Date(2026, 3, 10, 0, 0, 0, 0, time.Local)
	in := time.Date(2026, 3, 11, 3, 59, 0, 0, time.Local).UTC().Format(time.RFC3339)
	out := time.Date(2026, 3, 11, 4, 0, 0, 0, time.Local).UTC().Format(time.RFC3339)
	seedChannelOrderRow(t, dp.Db, "in", "com.x.shop", "GBP", in, "new", "paid", 1100, 0)
	seedChannelOrderRow(t, dp.Db, "out", "com.x.shop", "GBP", out, "new", "paid", 2200, 0)
	money := httpx.FuncsFor("en")["moneyIn"].(func(any, string) string)

	body := getReportsTab(t, mux, "online-channels", "?period=day&anchor="+d.Format("2006-01-02")).Body.String()
	if !strings.Contains(body, money(int64(1100), "GBP")) || strings.Contains(body, money(int64(2200), "GBP")) {
		t.Fatalf("day D must hold only the 03:59 order: %s", body)
	}
	body = getReportsTab(t, mux, "online-channels", "?period=day&anchor="+d.AddDate(0, 0, 1).Format("2006-01-02")).Body.String()
	if strings.Contains(body, money(int64(1100), "GBP")) || !strings.Contains(body, money(int64(2200), "GBP")) {
		t.Fatalf("day D+1 must hold only the 04:00 order: %s", body)
	}
}

// BA R6 / AC6: channel orders are not till sales. Adding them must not change
// a single byte of the header/KPI row or of any other tab's fragment.
func TestReportsOnlineChannels_KeptOutOfSalesFigures(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,20,120,'2026-10-10 12:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('r1','R002','completed','return','GBP',0,0,0,50,'2026-10-11 12:00:00')`); err != nil {
		t.Fatal(err)
	}
	tabs := []string{"sales-trend", "items", "tax", "payments", "eod", "shrinkage", "tips"}
	capture := func() map[string]string {
		out := map[string]string{}
		rec := getReportsPage(t, mux, onlineChannelsQuery)
		if rec.Code != http.StatusOK {
			t.Fatalf("/reports = %d", rec.Code)
		}
		page := rec.Body.String()
		i := strings.Index(page, `<div class="report-tabs"`)
		if i < 0 {
			t.Fatalf("report-tabs not found")
		}
		// The tab strip legitimately gains a button, so only the header and
		// KPI row above it are compared.
		out["header"] = page[:i]
		for _, name := range tabs {
			r := getReportsTab(t, mux, name, onlineChannelsQuery)
			if r.Code != http.StatusOK {
				t.Fatalf("tab %s = %d", name, r.Code)
			}
			out[name] = r.Body.String()
		}
		return out
	}
	before := capture()
	seedOctoberChannelOrders(t, dp.Db)
	seedChannelOrderRow(t, dp.Db, "u", "com.x.shop", "USD", "2026-10-09T10:00:00Z", "new", "refunded", 9900, 9900)
	after := capture()
	for k, b := range before {
		if after[k] != b {
			t.Fatalf("%s changed after channel orders were added", k)
		}
	}
}
