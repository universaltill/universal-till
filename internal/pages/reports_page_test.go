package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newReportsPageTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerReportsPage(mux, dp)
	return mux, dp
}

func getReportsPage(t *testing.T, mux *http.ServeMux, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/reports"+query, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestReportsPage_DaysParamValidAndInvalidValues(t *testing.T) {
	mux, _ := newReportsPageTestDeps(t)

	// No days param -> the 14-day default is selected.
	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `value="14" selected`) {
		t.Fatalf("expected the 14-day default selected, got: %s", rec.Body.String())
	}

	// A valid days value is honored.
	rec = getReportsPage(t, mux, "?days=30")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `value="30" selected`) {
		t.Fatalf("expected 30 days selected, got: %s", rec.Body.String())
	}

	// Out-of-range/garbage values fall back to the 14-day default rather
	// than erroring or hammering the DB with an unbounded window.
	for _, bad := range []string{"?days=0", "?days=-5", "?days=9999", "?days=notanumber"} {
		rec = getReportsPage(t, mux, bad)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", bad, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `value="14" selected`) {
			t.Fatalf("%s: expected fallback to the 14-day default, got: %s", bad, rec.Body.String())
		}
	}
}

func TestReportsPage_GrandTotalsSumDailySales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()
	// Two completed sales within the default 14-day window, on different
	// (relative) days so SalesByDay groups them into separate rows that
	// the handler must sum back together into GrandTotal/GrandTax/GrandCount.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,20,120,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s2','R002','completed','sale','GBP',200,0,40,240,datetime('now','-1 day'))`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// GrandTotal = 120 + 240 = 360 minor units = £3.60.
	if !strings.Contains(body, "£3.60") {
		t.Fatalf("expected the summed grand total £3.60, got: %s", body)
	}
}

func TestReportsPage_RefundsAndNetKPIs(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()
	// A £3.60 sale and a £1.00 completed return within the default 14-day
	// window: Refunds must show the return total, Net = Revenue - Refunds.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',300,0,60,360,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('r1','R002','completed','return','GBP',0,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "£3.60") {
		t.Fatalf("expected revenue £3.60 (gross of returns), got: %s", body)
	}
	if !strings.Contains(body, "£1.00") {
		t.Fatalf("expected the £1.00 refund total, got: %s", body)
	}
	// Net = 360 - 100 = 260 minor units = £2.60.
	if !strings.Contains(body, "£2.60") {
		t.Fatalf("expected net £2.60 (360 - 100), got: %s", body)
	}
}

func TestReportsPage_NetGoesNegativeWhenRefundsExceedSales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()
	// A refund with no matching sale in the window (e.g. the sale happened
	// before the window started): Net must go negative rather than the
	// handler crashing or the template garbling it, and the KPI must carry
	// the same "stock-low" negative-value treatment as the YoY tile.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('r1','R001','completed','return','GBP',0,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "£-1.00") {
		t.Fatalf("expected the negative net £-1.00, got: %s", body)
	}
	if !strings.Contains(body, `kpi-value stock-low">£-1.00`) {
		t.Fatalf("expected the negative net KPI to carry the stock-low treatment, got: %s", body)
	}
}

func TestReportsPage_TillsSectionHiddenUnlessMultipleRegisters(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	// A single till (the implicit primary, till_id='') -- the per-till
	// breakdown is meaningless for a one-register shop and must be hidden.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,till_id,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,'',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Sales by till") {
		t.Fatalf("expected the per-till section hidden for a single till, got: %s", rec.Body.String())
	}

	// A second till's sale makes the breakdown meaningful -- it must appear.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO tills(id,name,bearer_hash) VALUES('t2','Register 2','h2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,till_id,created_at) VALUES('s2','R002','completed','sale','GBP',900,0,0,900,'t2',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec = getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sales by till") {
		t.Fatalf("expected the per-till section shown once a second till has sales, got: %s", body)
	}
	if !strings.Contains(body, "Register 2") {
		t.Fatalf("expected Register 2 listed in the per-till breakdown, got: %s", body)
	}
}

func TestReportsPage_EODRowsOnlyIncludeEODKind(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO report_archive(id,kind,period,content_json) VALUES('r1','eod','2026-01-01','{"day":"2026-01-01","sales_count":3,"net":500}')`); err != nil {
		t.Fatal(err)
	}
	// A non-EOD archived report must never leak into the EOD table, even
	// though ListArchivedReports returns both kinds.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO report_archive(id,kind,period,content_json) VALUES('r2','some_other_kind','2026-01-02','{"day":"2026-01-02","sales_count":999,"net":999999}')`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "2026-01-01") {
		t.Fatalf("expected the eod row's period, got: %s", body)
	}
	if !strings.Contains(body, "£5.00") {
		t.Fatalf("expected the eod row's net rendered, got: %s", body)
	}
	if strings.Contains(body, "999999") || strings.Contains(body, "2026-01-02") {
		t.Fatalf("expected the non-eod archived report excluded, got: %s", body)
	}
}

func TestReportsPage_ManagerOnlySectionsGatedByRole(t *testing.T) {
	// Set explicitly (not just left ambient/unset) so this test can't
	// silently pass or fail depending on the developer's shell environment.
	t.Setenv("UT_AUTH", "on")
	mux, _ := newReportsPageTestDeps(t)
	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `name="time"`) {
		t.Fatalf("expected the manager-only EOD settings form hidden for a non-manager, got: %s", rec.Body.String())
	}
}

func TestReportsPage_SeasonalCardRendersLunarBadgeAndCategoryRollup(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	// A categorized solar-window seller and an uncategorized lunar-window
	// one (-330d: inside lunar k=1 [-354,-326), outside solar [-365,-337)).
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO categories(id,name) VALUES('c1','Bakery')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active,category_id) VALUES('bread','SKU-b','Sourdough',100,1,'c1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('dates','SKU-d','Fresh Dates',100,1)`); err != nil {
		t.Fatal(err)
	}
	seedSeasonalSale := func(saleID, itemID string, qty float64, daysAgo int) {
		t.Helper()
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES(?,?,'completed','sale','GBP',100,0,0,100,datetime('now',?))`,
			saleID, "R-"+saleID, fmt.Sprintf("-%d days", daysAgo)); err != nil {
			t.Fatal(err)
		}
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES(?,?,1,?,?,?,100,0,0,0,100,100)`,
			saleID+"-l1", saleID, itemID, "Line "+itemID, qty); err != nil {
			t.Fatal(err)
		}
	}
	seedSeasonalSale("sy1", "bread", 10, 360)
	seedSeasonalSale("sy2", "dates", 9, 330)

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The lunar-window item renders with the lunar badge; the solar one doesn't.
	if !strings.Contains(body, "🌙") || !strings.Contains(body, "lunar") {
		t.Fatalf("expected the lunar badge on the lunar-window item, got: %s", body)
	}
	// Category rollup: heading, the real category, and the uncategorized bucket.
	if !strings.Contains(body, "By category") || !strings.Contains(body, "Bakery") || !strings.Contains(body, "Uncategorized") {
		t.Fatalf("expected the category rollup with Bakery + Uncategorized, got: %s", body)
	}
}

// The reports header's low-stock chip links straight to /inventory, so it
// must never disagree with what that page itself warns about
// (universaltill/ut-docs#85 — this duplicated flat-7-day check was found
// still un-updated by an independent review after inventory_page.go and
// alerts.go had already been made lead-time-aware). Same fixture shape as
// inventory_prediction_test.go's TestInventoryLeadTimeAwareWarnAndReorder:
// rate 2/day, 16 on hand → DaysLeft=8, a 10-day lead time — the flat
// "<=7" check misses this (8 > 7); the shared EffectiveWarnDays doesn't
// (8 <= 10).
func TestReportsPage_LowStockChipMatchesInventoryPageLeadTime(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active,lead_time_days) VALUES('it-slow','SKU-ship','Slow Ship',100,1,10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO stock_locations(id,name) VALUES('loc-1','Shop floor')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO inventory(id,item_id,location_id,quantity,updated_at) VALUES('inv-1','it-slow','loc-1',16,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 14; i++ {
		saleID := fmt.Sprintf("s-%d", i)
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES(?,?,'completed','sale','GBP',400,0,0,400,datetime('now',?))`,
			saleID, "R-"+saleID, fmt.Sprintf("-%d days", i%9)); err != nil {
			t.Fatal(err)
		}
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES(?,?,1,'it-slow','Slow Ship',4,100,0,0,0,400,400)`,
			saleID+"-l1", saleID); err != nil {
			t.Fatal(err)
		}
	}

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `⚠ 1 `) || !strings.Contains(body, "chip-warn") {
		t.Fatalf("expected the reports header's low-stock chip to warn on exactly 1 item (DaysLeft=8 within its own 10-day lead time), got: %s", body)
	}
}

func TestReportsPage_SeasonalCategoryRollupHiddenWhenOnlyUncategorized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('loose','SKU-l','Loose Leaf',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('sy1','R-sy1','completed','sale','GBP',100,0,0,100,datetime('now','-360 days'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('sy1-l1','sy1',1,'loose','Loose Leaf',5,100,0,0,0,100,100)`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The seasonal card itself shows…
	if !strings.Contains(body, "Loose Leaf") {
		t.Fatalf("expected the seasonal card with the item, got: %s", body)
	}
	// …but a rollup that would only restate "Uncategorized" is suppressed.
	if strings.Contains(body, "By category") {
		t.Fatalf("expected the category rollup hidden when every item is uncategorized, got: %s", body)
	}
}

func TestReportsPage_WeekdayBarsNormalizeToBusiestAsFullWidth(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()
	// Weekday buckets derive from strftime('%w', created_at, 'localtime')
	// -- deterministic and timezone-stable relative to "now" (unlike the
	// hour buckets, which the review flagged as too UTC/localtime-fragile
	// to seed deterministically). Two sales today vs. one sale yesterday
	// (a DIFFERENT weekday, still within the 14-day window) makes "today"
	// busiest at 100% and "yesterday" at 50%.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s2','R002','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s3','R003','completed','sale','GBP',100,0,0,100,datetime('now','-1 day'))`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "inline-size:100%") {
		t.Fatalf("expected the busiest weekday bar normalized to 100%%, got: %s", body)
	}
	if !strings.Contains(body, "inline-size:50%") {
		t.Fatalf("expected the less-busy weekday bar normalized to 50%%, got: %s", body)
	}
}
