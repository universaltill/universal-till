package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
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
		AuthSvc:  auth.NewService(db),
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

func getReportsTab(t *testing.T, mux *http.ServeMux, name, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/ui/reports/tab/"+name+query, nil)
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

func TestReportsPage_CashAdjustmentsByReasonSectionHiddenUntilThereAreAny(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	// No cash_adjustment audit entries yet -- the section must not render
	// at all (mirrors the Tills section's hidden-when-not-applicable rule).
	rec := getReportsTab(t, mux, "payments", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Cash adjustments by reason") {
		t.Fatalf("expected the cash-adjustments section hidden with no adjustments, got: %s", rec.Body.String())
	}

	// Two Pfandrückgabe payouts (ut-docs#267's motivating case) plus a
	// float top-up under a different reason, written the same way
	// RecordCashAdjustment (internal/pos/shifts.go) does.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at) VALUES
('a1','user1','shift','shift1','cash_adjustment','{"amount":-500,"reason":"Pfandrückgabe"}',datetime('now')),
('a2','user1','shift','shift1','cash_adjustment','{"amount":-300,"reason":"Pfandrückgabe"}',datetime('now')),
('a3','user1','shift','shift1','cash_adjustment','{"amount":2000,"reason":"float top-up"}',datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	rec = getReportsTab(t, mux, "payments", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Cash adjustments by reason") {
		t.Fatalf("expected the cash-adjustments section shown once there are adjustments, got: %s", body)
	}
	if !strings.Contains(body, "Pfandrückgabe") || !strings.Contains(body, "float top-up") {
		t.Fatalf("expected both reasons listed, got: %s", body)
	}
}

// The only other surface for this data, /audit, is manager/admin-only
// ("this reads system-wide history" — audit_page.go) because a reason is
// staff free text. The reporting shortcut must not widen that: gated on
// the same "audit" action, not just "reports" (which a cashier also
// holds), per the independent review of this card.
func TestReportsPage_CashAdjustmentsByReasonGatedOnAuditPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at) VALUES
('a1','user1','shift','shift1','cash_adjustment','{"amount":-500,"reason":"Pfandrückgabe"}',datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	roleReq := func(role string) *httptest.ResponseRecorder {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/payments", nil), auth.User{ID: "u1", Role: role})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// A cashier holds "reports" (sees the tab and its other cards) but not
	// "audit" -- the cash-adjustments card must stay hidden for them.
	rec := roleReq("cashier")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Cash adjustments by reason") {
		t.Fatalf("expected the cash-adjustments card hidden for a cashier (no audit permission), got: %s", rec.Body.String())
	}

	// A manager holds "audit" and must see it.
	rec = roleReq("manager")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Cash adjustments by reason") {
		t.Fatalf("expected the cash-adjustments card shown for a manager, got: %s", rec.Body.String())
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
	rec := getReportsTab(t, mux, "payments", "")
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
	rec = getReportsTab(t, mux, "payments", "")
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

	rec := getReportsTab(t, mux, "eod", "")
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
	// The page itself must not offer the EOD tab to a non-manager…
	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "/ui/reports/tab/eod") {
		t.Fatalf("expected the manager-only EOD tab hidden for a non-manager, got: %s", rec.Body.String())
	}
	// …and the fragment itself stays gated even when requested directly.
	rec = getReportsTab(t, mux, "eod", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `name="time"`) {
		t.Fatalf("expected the manager-only EOD settings form hidden for a non-manager, got: %s", rec.Body.String())
	}
}

// Complements TestReportsPage_ManagerOnlySectionsGatedByRole's no-session
// negative case with the positive one: a REAL manager session (ut-docs#709 —
// canPerform()/Auth.Can(), not just the no-session short-circuit) must see
// the EOD tab and its settings form. Also covers super_admin (#554/#555's
// noted broadening vs. the old isManagerOrAuthOff gate, which only
// recognized manager/admin) — accepted and inert since nothing today
// creates a super_admin-role user.
func TestReportsPage_ManagerAndSuperAdminSessionsSeeEODTab(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	for _, role := range []string{"manager", "admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			mux, _ := newReportsPageTestDeps(t)
			req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/reports", nil), auth.User{ID: "u1", Role: role})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "/ui/reports/tab/eod") {
				t.Fatalf("expected the EOD tab offered to role=%s, got: %s", role, rec.Body.String())
			}

			req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/eod", nil), auth.User{ID: "u1", Role: role})
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `name="time"`) {
				t.Fatalf("expected the EOD settings form shown to role=%s, got: %s", role, rec.Body.String())
			}
		})
	}
}

// ut-docs#794 review finding (blocker): before this card, the EOD tab's
// entire body — including the Run/Print/Settings buttons checkOrElevate now
// gates — was hidden behind eod_report itself, the SAME action. A shop that
// grants `reports` to a role without also granting `eod_report` (exactly
// what the permission matrix, ut-docs#557's own sibling feature, exists to
// let a shop do) would see the elevation dialog become unreachable dead
// code: no button, nothing to click, nothing to approve. This pins the fix
// — the view gate (`reports`) and the run gate (`eod_report`) are
// independent, and a viewer without eod_report still gets the buttons
// (checkOrElevate is what actually stops them), but not the archived
// report rows (real money figures stay behind eod_report specifically).
func TestReportsPage_EODTabButtonsVisibleWithoutEODReportPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()
	authRepo := data.NewAuthRepo(dp.Db)

	// A role that can view Reports but is explicitly NOT trusted to run/
	// approve EOD on its own — grant `reports`, leave `eod_report` denied
	// (cashier's default).
	if err := authRepo.SetRolePermission(ctx, nil, "cashier", "reports", true); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, keyEODEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, keyEODTime, "22:30"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO report_archive(id,kind,period,content_json) VALUES('r1','eod','2026-01-01','{"day":"2026-01-01","sales_count":3,"net":500}')`); err != nil {
		t.Fatal(err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/eod", nil), auth.User{ID: "u1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/reports/eod/run"`) {
		t.Fatalf("expected the Run button visible to a reports-only viewer (checkOrElevate is the real gate, not this render), got: %s", body)
	}
	if !strings.Contains(body, `name="time"`) {
		t.Fatalf("expected the schedule form visible, got: %s", body)
	}
	if !strings.Contains(body, "22:30") {
		t.Fatalf("expected the REAL current schedule time shown (operational, not sensitive), got: %s", body)
	}
	if strings.Contains(body, "500") || strings.Contains(body, "£5.00") {
		t.Fatalf("expected the archived report's money figures NOT shown to a role without eod_report, got: %s", body)
	}
	// ut-docs#794 review finding (residual): the Reprint button is the
	// ONLY trigger for checkOrElevate on print/{period} — without this,
	// that site's elevation dialog is exactly as unreachable as the whole
	// tab was before the main fix.
	if !strings.Contains(body, `hx-post="/api/reports/eod/print/2026-01-01"`) {
		t.Fatalf("expected the Reprint button visible (without money columns) so print/{period}'s elevation dialog stays reachable, got: %s", body)
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

	rec := getReportsTab(t, mux, "forecast", "")
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

	rec := getReportsTab(t, mux, "forecast", "")
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

	rec := getReportsTab(t, mux, "sales-trend", "")
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

// The whole point of ut-docs#401: heavy report queries must NOT run on page
// load. A distinctively-named top seller proves it — its name can only reach
// the response body via TopItems, so it must be absent from GET /reports and
// present only in the items tab fragment.
func TestReportsPage_TopItemsDeferredToItemsTab(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('zz1','SKU-zz','Zzyzx Distinctive Top Item',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('s1-l1','s1',1,'zz1','Zzyzx Distinctive Top Item',3,100,0,0,0,100,100)`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsPage(t, mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Zzyzx Distinctive Top Item") {
		t.Fatalf("expected the top-items query deferred (item name absent from the initial page), got: %s", rec.Body.String())
	}

	rec = getReportsTab(t, mux, "items", "?days=14")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Zzyzx Distinctive Top Item") {
		t.Fatalf("expected the top seller in the items tab fragment, got: %s", rec.Body.String())
	}
}

func TestReportsTabs_AllNamedTabsReturn200(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReportsPageTestDeps(t)
	for _, name := range []string{"sales-trend", "items", "tax", "forecast", "payments", "eod", "tips"} {
		rec := getReportsTab(t, mux, name, "?days=14")
		if rec.Code != http.StatusOK {
			t.Fatalf("tab %q: expected 200, got %d: %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestReportsTabs_DaysParamHonored(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	// A sale 5 days ago: inside a 14-day window, outside a 2-day one.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('zz1','SKU-zz','Zzyzx Windowed Item',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now','-5 days'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('s1-l1','s1',1,'zz1','Zzyzx Windowed Item',3,100,0,0,0,100,100)`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsTab(t, mux, "items", "?days=14")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Zzyzx Windowed Item") {
		t.Fatalf("expected the 5-day-old sale inside the 14-day window, got: %s", rec.Body.String())
	}

	rec = getReportsTab(t, mux, "items", "?days=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Zzyzx Windowed Item") {
		t.Fatalf("expected the 5-day-old sale outside the 2-day window, got: %s", rec.Body.String())
	}
}

// ut-docs#519: ?period=month&anchor=... resolves a real calendar month
// end-to-end, independent of the rolling ?days= window — a sale from three
// months ago (outside any sane ?days= value) must still show up when its
// own month is anchored, and a sale just outside that month must not.
func TestReportsTabs_PeriodMonthAnchorSelectsCalendarMonth(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('jul1','SKU-jul','July Only Item',100,1)`); err != nil {
		t.Fatal(err)
	}
	// Inside July 2026: the 15th at noon.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s-jul','R-JUL','completed','sale','GBP',100,0,0,100,'2026-07-15 12:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('s-jul-l1','s-jul',1,'jul1','July Only Item',1,100,0,0,0,100,100)`); err != nil {
		t.Fatal(err)
	}
	// Just outside July: August 1st, 00:00:00 (the exclusive boundary itself).
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s-aug','R-AUG','completed','sale','GBP',100,0,0,100,'2026-08-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('s-aug-l1','s-aug',1,'jul1','July Only Item',1,100,0,0,0,100,100)`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsTab(t, mux, "items", "?period=month&anchor=2026-07-15")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "July Only Item") {
		t.Fatalf("expected July's own sale inside the anchored month, got: %s", body)
	}
	// Only ONE of the two sales (qty 1) should count, not both — the August
	// row (sitting exactly on the exclusive month boundary) must not leak
	// in and double the quantity to ×2.
	if strings.Contains(body, "×2") {
		t.Fatalf("expected qty 1 (July only, August excluded at the boundary), got a doubled ×2: %s", body)
	}
}

// ut-docs#519: the reports.business_day_start setting shifts a "day"
// period's boundary — a sale just after midnight but before the configured
// boundary counts toward the PREVIOUS business day, not "today".
func TestReportsPage_BusinessDayStart_ShiftsDayPeriodBoundary(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if err := dp.Settings.Set(ctx, keyReportsBusinessDayStart, "06:00"); err != nil {
		t.Fatal(err)
	}

	// A sale at 02:00 local "today" — with a 06:00 boundary this belongs to
	// YESTERDAY's business day, so anchor=yesterday (period=day) must show
	// it and anchor=today (i.e. no anchor, "now") must not.
	twoAM := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 2, 0, 0, 0, time.Local)
	if twoAM.After(time.Now()) {
		// Guard: only meaningful if "now" is actually past 02:00 local.
		t.Skip("test needs to run after 02:00 local time")
	}
	yesterday := twoAM.AddDate(0, 0, -1).Format("2006-01-02")
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('lt1','SKU-lt','Late Night Item',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s-lt','R-LT','completed','sale','GBP',100,0,0,100,?)`,
		twoAM.UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,line_discount,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('s-lt-l1','s-lt',1,'lt1','Late Night Item',1,100,0,0,0,100,100)`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsTab(t, mux, "items", "?period=day&anchor="+yesterday)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Late Night Item") {
		t.Fatalf("02:00 sale with a 06:00 business-day boundary must count toward the PREVIOUS business day, got: %s", rec.Body.String())
	}
}

func TestReportsTabs_UnknownNameReturns404(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReportsPageTestDeps(t)
	rec := getReportsTab(t, mux, "nonsense", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown tab, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReportsPage_TabNavWiredToFragmentRoutes(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReportsPageTestDeps(t)
	rec := getReportsPage(t, mux, "?days=30")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, name := range []string{"sales-trend", "items", "tax", "forecast", "payments", "eod", "tips"} {
		want := `hx-get="/ui/reports/tab/` + name + `?days=30"`
		if !strings.Contains(body, want) {
			t.Fatalf("expected the tab nav to contain %s, got: %s", want, body)
		}
	}
	if !strings.Contains(body, `id="report-tab-panel"`) {
		t.Fatalf("expected the empty tab panel container, got: %s", body)
	}
	// No tab may auto-fire on page load — a load trigger would defeat the
	// entire deferral. (The base layout has its own unrelated load-triggered
	// elements, so only inspect elements pointing at the tab fragments.)
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "/ui/reports/tab/") && strings.Contains(line, "hx-trigger") {
			t.Fatalf("expected no explicit hx-trigger on tab buttons (click is the default; load would defeat deferral), got: %s", line)
		}
	}
}

// ut-docs#519 review finding (blocking): the picker's date input and the
// tab buttons' ?anchor= query string used to be computed by two independent
// functions (reportAnchorParam vs. parseReportWindow's own internal
// anchorDate) that could disagree once a business-day boundary was
// configured — the picker showing one date while every tab silently queried
// a different (often not-yet-started, empty) window. Both now read the same
// resolved reportWindow.Anchor, so this asserts they can never drift apart
// again, independent of what the actual date happens to be.
func TestReportsPage_PickerAnchorMatchesTabQueryStringAnchor(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReportsPageTestDeps(t)

	rec := getReportsPage(t, mux, "?period=day")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	m := regexp.MustCompile(`name="anchor" value="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("expected the picker's anchor date input, got: %s", body)
	}
	pickerAnchor := m[1]

	tabM := regexp.MustCompile(`/ui/reports/tab/items\?period=day&amp;anchor=([0-9-]+)`).FindStringSubmatch(body)
	if tabM == nil {
		t.Fatalf("expected the items tab's hx-get to carry period=day&anchor=..., got: %s", body)
	}
	tabAnchor := tabM[1]

	if pickerAnchor != tabAnchor {
		t.Fatalf("picker anchor %q and tab query-string anchor %q must agree — a mismatch means clicking a tab queries a different window than the KPIs above it", pickerAnchor, tabAnchor)
	}
}

// ut-docs#519 review finding (blocking): the eod tab's business_day_start
// setting was validated and saved on write, but never read back on render —
// the input always showed blank, so saving the Day-end settings panel for
// any reason (even just toggling the auto-run checkbox) silently wiped the
// boundary back to midnight the next time the form posted its blank field.
func TestReportsTabs_EOD_BusinessDayStartRoundTrips(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if err := dp.Settings.Set(ctx, keyReportsBusinessDayStart, "06:30"); err != nil {
		t.Fatal(err)
	}

	rec := getReportsTab(t, mux, "eod", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="business_day_start" value="06:30"`) {
		t.Fatalf("expected the saved business_day_start to round-trip into the rendered field, got: %s", body)
	}
}

// ut-docs#964: the "tips" tab's received-vs-allocated summary — "received"
// (payments.tip_amount on a completed sale, tip_recipient='employee') and
// "allocated" (worker_allocations, this ADR-0063 ledger) are two
// independent figures, so this seeds both sides distinctly and checks each
// renders, not just that the tab returns 200.
func TestReportsPage_TipsTabShowsReceivedVsAllocated(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker1','worker1','Worker One','cashier',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,cashier_id,created_at) VALUES('s1','R001','completed','sale','GBP',1000,0,0,1000,'worker1',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id,sale_id,method_id,amount,currency,change_given,tip_amount,tip_recipient,paid_at) VALUES('p1','s1','cash',1000,'GBP',0,500,'employee',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO worker_allocations(id,source_type,source_id,cashier_id,amount_minor,allocated_at,note) VALUES('wa1','tip','','worker1',300,datetime('now'),'shift payout')`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsTab(t, mux, "tips", "?days=14")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "£5.00") {
		t.Fatalf("expected tip received (£5.00) rendered, got: %s", body)
	}
	if !strings.Contains(body, "£3.00") {
		t.Fatalf("expected tip allocated (£3.00) rendered, got: %s", body)
	}
}

// ut-docs#1274: reports_tab_tips.html's #tips-amount field hardcoded a fixed
// 2-decimal pattern/placeholder regardless of the shop's real configured
// currency, same defect class as shifts.html (see
// TestShiftsPage_LabelsAndPatternsAreCurrencyAware) -- on a 0-decimal
// currency the old pattern rejected a valid integer amount like "500".
func TestReportsPage_TipsTabRecordFieldPatternIsCurrencyAware(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReportsPageTestDeps(t)

	httpx.InitCurrency("GBP")
	body := getReportsTab(t, mux, "tips", "?days=14").Body.String()
	if !strings.Contains(body, "(£)") {
		t.Fatalf("expected the GBP symbol in the tips-amount label, got:\n%s", body)
	}
	if !strings.Contains(body, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
		t.Fatalf("expected the 2-decimal pattern for GBP, got:\n%s", body)
	}
	if !strings.Contains(body, `placeholder="0.00"`) {
		t.Fatalf("expected the 2-decimal placeholder for GBP, got:\n%s", body)
	}

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	body = getReportsTab(t, mux, "tips", "?days=14").Body.String()
	if !strings.Contains(body, "(تومان)") {
		t.Fatalf("expected the IRT symbol in the tips-amount label, got:\n%s", body)
	}
	if strings.Contains(body, "(£)") {
		t.Fatalf("expected NO leftover GBP symbol on the tips-amount label once currency is 0-decimal, got:\n%s", body)
	}
	if !strings.Contains(body, `pattern="[0-9]+"`) {
		t.Fatalf("expected the 0-decimal (integer-only) pattern for IRT, got:\n%s", body)
	}
	if strings.Contains(body, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
		t.Fatalf("expected NO 2-decimal pattern left over once currency is 0-decimal, got:\n%s", body)
	}
	if !strings.Contains(body, `placeholder="0"`) {
		t.Fatalf("expected the 0-decimal placeholder for IRT, got:\n%s", body)
	}
	if strings.Contains(body, `placeholder="0.00"`) {
		t.Fatalf("expected NO 2-decimal placeholder left over once currency is 0-decimal, got:\n%s", body)
	}
}

// A role holding `reports` but not `worker_allocation` (the cashier default)
// must still see the tab's totals — same visibility split EOD's own
// IsManager/CanRunEOD gating uses — but not the row-level detail table, the
// record-a-payout form, or the export link, since those expose or let
// someone write individual workers' payout records.
func TestReportsPage_TipsTabRecordFormGatedOnWorkerAllocationPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()
	authRepo := data.NewAuthRepo(dp.Db)

	// A role that can view Reports but is not trusted to record/export
	// worker payouts -- grant `reports`, leave `worker_allocation` denied
	// (cashier's default).
	if err := authRepo.SetRolePermission(ctx, nil, "cashier", "reports", true); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO worker_allocations(id,source_type,source_id,cashier_id,amount_minor,allocated_at,note) VALUES('wa1','tip','','u1',300,datetime('now'),'')`); err != nil {
		t.Fatal(err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/tips", nil), auth.User{ID: "u1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "£3.00") {
		t.Fatalf("expected the allocated total visible under `reports` alone, got: %s", body)
	}
	if strings.Contains(body, `hx-post="/api/reports/worker-allocations"`) {
		t.Fatalf("expected the record-payout form hidden without worker_allocation, got: %s", body)
	}
	if strings.Contains(body, "/api/reports/worker-allocations/export") {
		t.Fatalf("expected the export link hidden without worker_allocation, got: %s", body)
	}

	// A manager holds worker_allocation (migration 066) and must see both.
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/tips", nil), auth.User{ID: "m1", Role: "manager"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/reports/worker-allocations"`) {
		t.Fatalf("expected the record-payout form visible for a manager, got: %s", body)
	}
	if !strings.Contains(body, "/api/reports/worker-allocations/export") {
		t.Fatalf("expected the export link visible for a manager, got: %s", body)
	}
}

// ut-docs#1273: the record-payout form targets #report-tab-panel (a full
// tab re-render on success), so htmx's default "never swap a non-2xx
// response" behavior would otherwise drop every validation/save error
// silently instead of surfacing it — the button would appear to do
// nothing. Pins the rendered wiring that fixes this: a dedicated result
// element plus a `htmx:responseError` listener scoped to the form itself.
func TestReportsPage_TipsTabRecordFormWiresResponseErrorHandler(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _ := newReportsPageTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/tips", nil), auth.User{ID: "m1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="tips-record-form"`) {
		t.Fatalf("expected the record-payout form to carry a stable id for the error handler to bind to, got: %s", body)
	}
	if !strings.Contains(body, `id="tips-result"`) {
		t.Fatalf("expected a dedicated result element for surfacing a save/validation error, got: %s", body)
	}
	if !strings.Contains(body, "htmx:responseError") {
		t.Fatalf("expected a htmx:responseError handler wired for the record-payout form, got: %s", body)
	}
}

// Independent review, ut-docs#964 blocker 2: a role holding `reports` but
// not `worker_allocation` must NOT be able to read one named worker's
// totals via ?cashier=, even though the tab renders a worker picker
// populated with every user for a worker_allocation-holding session — a
// reports-only session gets the shop-wide total regardless of what it puts
// on the query string. Confirms the fix pins the exact leak the review
// reproduced ("read worker2's £42.42 payout total via ?cashier=worker2" as
// a `reports`-only session, two clicks in the normal UI).
func TestReportsPage_TipsTabCashierFilterIgnoredWithoutWorkerAllocationPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()
	authRepo := data.NewAuthRepo(dp.Db)

	if err := authRepo.SetRolePermission(ctx, nil, "cashier", "reports", true); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO worker_allocations(id,source_type,source_id,cashier_id,amount_minor,allocated_at,note) VALUES('wa1','tip','','worker1',300,datetime('now'),'')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO worker_allocations(id,source_type,source_id,cashier_id,amount_minor,allocated_at,note) VALUES('wa2','tip','','worker2',4242,datetime('now'),'')`); err != nil {
		t.Fatal(err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/tips?cashier=worker2", nil), auth.User{ID: "u1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Shop-wide total (£3.00 + £42.42 = £45.42), NOT worker2's isolated
	// £42.42 -- proves ?cashier= was ignored, not honored.
	if !strings.Contains(body, "£45.42") {
		t.Fatalf("expected the shop-wide total (?cashier= ignored for a reports-only session), got: %s", body)
	}
	if strings.Contains(body, "£42.42") {
		t.Fatalf("LEAK: expected worker2's isolated total NOT shown to a reports-only session via ?cashier=, got: %s", body)
	}
	// The picker itself must also be absent (it's gated under CanRecord in
	// the template now, not just its effect on the query).
	if strings.Contains(body, `name="cashier"`) {
		t.Fatalf("expected the worker filter picker hidden without worker_allocation, got: %s", body)
	}
}

func workerAllocationCount(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT count(*) FROM worker_allocations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A valid POST persists a row and the re-rendered tab reflects the updated
// allocated total immediately (the htmx swap target).
func TestReportsPage_RecordWorkerAllocation_ValidPayoutPersistsAndRefreshesTab(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker1','worker1','Worker One','cashier',1)`); err != nil {
		t.Fatal(err)
	}

	today := time.Now().Format("2006-01-02")
	form := url.Values{
		"date":        {today},
		"cashier_id":  {"worker1"},
		"source_type": {"tip"},
		"amount":      {"1234"},
		"note":        {"shift-end payout"},
	}
	rec := postForm(mux, "/api/reports/worker-allocations", form, &auth.User{ID: "mgr1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "£12.34") {
		t.Fatalf("expected the refreshed tab to show the new allocated total (£12.34), got: %s", rec.Body.String())
	}

	if n := workerAllocationCount(t, dp); n != 1 {
		t.Fatalf("expected 1 worker_allocations row, got %d", n)
	}
	var sourceType, cashierID, note string
	var amount int64
	if err := dp.Db.QueryRowContext(ctx, `SELECT source_type, cashier_id, amount_minor, note FROM worker_allocations`).
		Scan(&sourceType, &cashierID, &amount, &note); err != nil {
		t.Fatal(err)
	}
	if sourceType != "tip" || cashierID != "worker1" || amount != 1234 || note != "shift-end payout" {
		t.Fatalf("unexpected persisted row: type=%s cashier=%s amount=%d note=%s", sourceType, cashierID, amount, note)
	}

	// The write must also be audited (ADR-0063/#964's own record-keeping
	// point — a payout with no audit trail is not a usable statutory record).
	var auditCount int
	if err := dp.Db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action = 'worker_allocation_recorded'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expected 1 worker_allocation_recorded audit entry, got %d", auditCount)
	}
}

// Each of these is rejected with 400 and writes nothing — a future date (a
// payout record documents money already paid, never a promise), an unknown
// cashier_id, a source_type outside the UK-scoped tip/service_charge subset,
// and a non-positive amount.
func TestReportsPage_RecordWorkerAllocation_InvalidInputsRejectedAndWriteNothing(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker1','worker1','Worker One','cashier',1)`); err != nil {
		t.Fatal(err)
	}

	today := time.Now().Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	manager := &auth.User{ID: "mgr1", Role: "manager"}

	cases := []struct {
		name string
		form url.Values
	}{
		{"future date", url.Values{"date": {tomorrow}, "cashier_id": {"worker1"}, "source_type": {"tip"}, "amount": {"100"}}},
		{"unknown cashier", url.Values{"date": {today}, "cashier_id": {"does-not-exist"}, "source_type": {"tip"}, "amount": {"100"}}},
		{"bad source_type", url.Values{"date": {today}, "cashier_id": {"worker1"}, "source_type": {"yuzde_usulu_pool"}, "amount": {"100"}}},
		{"zero amount", url.Values{"date": {today}, "cashier_id": {"worker1"}, "source_type": {"tip"}, "amount": {"0"}}},
		{"negative amount", url.Values{"date": {today}, "cashier_id": {"worker1"}, "source_type": {"tip"}, "amount": {"-50"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postForm(mux, "/api/reports/worker-allocations", c.form, manager)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if n := workerAllocationCount(t, dp); n != 0 {
		t.Fatalf("expected no rows written by any rejected request, got %d", n)
	}
}

// The CSV export returns the right headers and contains a recorded row's
// data — mirrors eod_api.go's own archive/export CSV precedent.
func TestReportsPage_WorkerAllocationExport_ReturnsCSVWithRecordedRow(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker1','worker1','Worker One','cashier',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO worker_allocations(id,source_type,source_id,cashier_id,amount_minor,allocated_at,note) VALUES('wa1','tip','','worker1',1234,'2026-08-25T18:00:00Z','shift payout')`); err != nil {
		t.Fatal(err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/reports/worker-allocations/export?from=2026-08-25&to=2026-08-25", nil), auth.User{ID: "mgr1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("expected Content-Type text/csv, got %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("expected Content-Disposition attachment, got %q", cd)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Worker One") {
		t.Fatalf("expected the worker's display name in the export, got: %s", body)
	}
	if !strings.Contains(body, "1234") {
		t.Fatalf("expected the raw minor-unit amount in the export, got: %s", body)
	}
	if !strings.Contains(body, "shift payout") {
		t.Fatalf("expected the note in the export, got: %s", body)
	}

	var auditCount int
	if err := dp.Db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action = 'worker_allocation_exported'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expected 1 worker_allocation_exported audit entry, got %d", auditCount)
	}
}

// A session lacking `worker_allocation` gets 403 from both the record POST
// and the export GET — the same permission gates both, per #964's brief.
func TestReportsPage_WorkerAllocation_ForbiddenWithoutPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker1','worker1','Worker One','cashier',1)`); err != nil {
		t.Fatal(err)
	}

	cashier := &auth.User{ID: "u1", Role: "cashier"}
	today := time.Now().Format("2006-01-02")
	form := url.Values{"date": {today}, "cashier_id": {"worker1"}, "source_type": {"tip"}, "amount": {"100"}}
	rec := postForm(mux, "/api/reports/worker-allocations", form, cashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 from POST, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := workerAllocationCount(t, dp); n != 0 {
		t.Fatalf("expected no row written by a forbidden POST, got %d", n)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/reports/worker-allocations/export?from=2026-08-25&to=2026-08-25", nil), *cashier)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 from export GET, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A regression guard on the window round-trip: parseReportWindow/
// renderTipsTab read r.URL.Query() (the GET-tab route's own convention),
// which would be empty on this POST's body-encoded fields if the handler
// didn't explicitly copy them onto r.URL.RawQuery before re-rendering —
// silently resetting the just-posted-from window/cashier filter back to
// the 14-day/all-workers default instead of keeping what the operator had
// open. Seeds an allocation outside the default 14-day window but inside a
// wider one, and a second worker's allocation, to prove the SAME period and
// cashier filter the record form re-submitted are honored on the refresh.
func TestReportsPage_RecordWorkerAllocation_RefreshedTabKeepsWindowAndCashierFilter(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker1','worker1','Worker One','cashier',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker2','worker2','Worker Two','cashier',1)`); err != nil {
		t.Fatal(err)
	}
	// 30 days ago: outside the 14-day default, inside a 90-day window.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO worker_allocations(id,source_type,source_id,cashier_id,amount_minor,allocated_at,note) VALUES('wa-old','tip','','worker1',700,datetime('now','-30 days'),'')`); err != nil {
		t.Fatal(err)
	}
	// Today, but a DIFFERENT worker -- must be excluded by the cashier filter.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO worker_allocations(id,source_type,source_id,cashier_id,amount_minor,allocated_at,note) VALUES('wa-other','tip','','worker2',999,datetime('now'),'')`); err != nil {
		t.Fatal(err)
	}

	today := time.Now().Format("2006-01-02")
	form := url.Values{
		"date":        {today},
		"cashier_id":  {"worker1"},
		"source_type": {"tip"},
		"amount":      {"100"},
		"days":        {"90"},
		"cashier":     {"worker1"},
	}
	rec := postForm(mux, "/api/reports/worker-allocations", form, &auth.User{ID: "mgr1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// £8.00 = 700 (30-days-ago) + 100 (just posted) for worker1 only, over a
	// 90-day window -- proves BOTH the wider window and the cashier filter
	// survived the refresh, not just one of the two.
	if !strings.Contains(body, "£8.00") {
		t.Fatalf("expected the refreshed tab to keep the 90-day window and worker1 filter (£8.00 = £7.00 + £1.00), got: %s", body)
	}
	if strings.Contains(body, "£9.99") {
		t.Fatalf("expected worker2's allocation excluded by the re-submitted cashier filter, got: %s", body)
	}
}

// Independent review, ut-docs#964 blocker 1: pins workerAllocationRequestedAt
// against a fixed, non-UTC nowLocal so this is deterministic regardless of
// the host's real TZ or wall clock at test time (the original inline
// version mixed a UTC "today" comparison with a local instant construction,
// so a manager's own local "today" could be rejected as future, or a real
// local tomorrow silently accepted, depending on which side of UTC midnight
// the shop's offset put them on).
func TestWorkerAllocationRequestedAt_LocalTodayNotRejectedAsFuture(t *testing.T) {
	// UTC+3 (Turkey/ADR-0063's own named market) at 01:30 local -- UTC is
	// still 22:30 the PREVIOUS day, so a UTC-based "today" check would see
	// this local date as tomorrow and reject it.
	loc, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Skipf("tzdata for Europe/Istanbul unavailable: %v", err)
	}
	nowLocal := time.Date(2026, 8, 25, 1, 30, 0, 0, loc)

	allocatedAt, isFuture, err := workerAllocationRequestedAt("2026-08-25", nowLocal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isFuture {
		t.Fatal("the shop's own local today must not be rejected as future")
	}

	// The stored instant, read back through the SAME local location (the
	// repo's date(allocated_at,'localtime') equivalent for this offset),
	// must resolve to exactly the picked date -- not a neighbouring day.
	parsed, err := time.Parse(time.RFC3339, allocatedAt)
	if err != nil {
		t.Fatalf("stored allocatedAt %q did not parse as RFC3339: %v", allocatedAt, err)
	}
	if got := parsed.In(loc).Format("2006-01-02"); got != "2026-08-25" {
		t.Fatalf("allocatedAt %s resolves to local date %s, want 2026-08-25", allocatedAt, got)
	}
}

func TestWorkerAllocationRequestedAt_LocalTomorrowRejectedAsFuture(t *testing.T) {
	// UTC-7 (Pacific, an Americas market on the other side of the same
	// bug): at 20:00 local, UTC is already 03:00 the NEXT day, so a
	// UTC-based "today" check would let a real local tomorrow through.
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tzdata for America/Los_Angeles unavailable: %v", err)
	}
	nowLocal := time.Date(2026, 8, 24, 20, 0, 0, 0, loc)

	_, isFuture, err := workerAllocationRequestedAt("2026-08-25", nowLocal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isFuture {
		t.Fatal("the shop's own local tomorrow must be rejected as future")
	}
}

func TestWorkerAllocationRequestedAt_MalformedDateRejected(t *testing.T) {
	if _, _, err := workerAllocationRequestedAt("not-a-date", time.Now()); err == nil {
		t.Fatal("expected an error for a malformed date")
	}
}

// TestWorkerAllocationDateRange_MapsThroughBusinessDayBoundary is the
// regression test for ut-docs#1020 item 6: workerAllocationDateRange used
// to format window.To.Add(-time.Second) directly, which is only correct
// when the business day starts at midnight. With any other start (e.g.
// 06:00), a single ?period=day report's window resolves to
// [day 06:00, day+1 06:00) and formatting the decremented upper bound
// directly yields "day+1", not "day" — so a one-day report's own date
// range spanned TWO calendar days, and a ?period=month report spilled one
// day into the following month. Every payout on that spillover day was
// then double-counted: present in both the report it belongs to and the
// next one. Fixed by mapping both ends through businessDateFor — the SAME
// boundary parseReportWindow itself built the window from.
func TestWorkerAllocationDateRange_MapsThroughBusinessDayBoundary(t *testing.T) {
	cases := []struct {
		name             string
		query            string
		bizStart         string
		wantFrom, wantTo string
	}{
		{"day at midnight", "?period=day&anchor=2026-08-25", "", "2026-08-25", "2026-08-25"},
		{"day at 06:00", "?period=day&anchor=2026-08-25", "06:00", "2026-08-25", "2026-08-25"},
		{"month at midnight", "?period=month&anchor=2026-08-25", "", "2026-08-01", "2026-08-31"},
		{"month at 06:00", "?period=month&anchor=2026-08-25", "06:00", "2026-08-01", "2026-08-31"},
		{"week at 06:00", "?period=week&anchor=2026-08-25", "06:00", "2026-08-24", "2026-08-30"},
		{"year at 06:00", "?period=year&anchor=2026-08-25", "06:00", "2026-01-01", "2026-12-31"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ui/reports/tab/tips"+c.query, nil)
			window := parseReportWindow(req, c.bizStart)
			gotFrom, gotTo := workerAllocationDateRange(window)
			if gotFrom != c.wantFrom || gotTo != c.wantTo {
				t.Fatalf("window [%s, %s) -> dates %s..%s, want %s..%s",
					window.From.Format(time.RFC3339), window.To.Format(time.RFC3339), gotFrom, gotTo, c.wantFrom, c.wantTo)
			}
		})
	}
}

// TestReportsPage_WorkerAllocationExport_EscapesFormulaInjection is the
// regression test for ut-docs#1020 item 2: encoding/csv quotes correctly
// (not a parsing bug), but a manager-typed note or a worker's own display
// name starting with =, +, -, or @ becomes a LIVE FORMULA when the export
// is opened in Excel/Sheets — the help text explicitly frames this export
// as for "a worker, an accountant, or anyone else" to open. csvSafe
// prefixes such a field with a leading apostrophe, defusing it while
// keeping the value readable.
func TestReportsPage_WorkerAllocationExport_EscapesFormulaInjection(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker1','worker1','=cmd|''/c calc''!A1','cashier',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO worker_allocations(id,source_type,source_id,cashier_id,amount_minor,allocated_at,note) VALUES('wa1','tip','','worker1',500,'2026-08-25T18:00:00Z','+SUM(A1:A9)')`); err != nil {
		t.Fatal(err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/reports/worker-allocations/export?from=2026-08-25&to=2026-08-25", nil), auth.User{ID: "mgr1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `'=cmd`) {
		t.Fatalf("expected the worker display name defused with a leading apostrophe, got: %s", body)
	}
	if !strings.Contains(body, `'+SUM(A1:A9)`) {
		t.Fatalf("expected the note defused with a leading apostrophe, got: %s", body)
	}
}
