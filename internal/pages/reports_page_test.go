package pages

import (
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
