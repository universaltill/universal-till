package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func newPromotionsTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerPromotions(mux, d)
	return mux, d
}

// ut-docs#902: GET /promotions must be reachable under UT_AUTH=off with no
// session — same fix and rationale as
// country_settings_page_test.go's TestCountrySettingsPage_ReachableUnderAuthOff
// (ut-docs#901's precedent).
func TestPromotionsPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newPromotionsTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/promotions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /promotions under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Mutating handlers, not just the GET page, must also pick up canPerform's
// UT_AUTH=off bypass — independent review finding on ut-docs#901, applied
// here too.
func TestPromotionsPageCreate_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newPromotionsTestMux(t)

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"AUTHOFF"}, "type": {"amount"}, "value_amount": {"5.00"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("create under UT_AUTH=off: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPromotionsPagePermissions(t *testing.T) {
	mux, _ := newPromotionsTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /promotions = %d, want 403", rec.Code)
	}

	if rec := postForm(mux, "/api/promotions", url.Values{"code": {"NEWCODE"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/promotions/DISC10/edit", url.Values{}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier edit = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/promotions/DISC10/active", url.Values{"active": {"0"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier active toggle = %d, want 403", rec.Code)
	}
}

func TestPromotionsPageCreate_EmptyCodeRejected(t *testing.T) {
	mux, _ := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"   "}, "type": {"amount"}, "value_amount": {"5.00"},
	}, &manager)
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/promotions?err=") {
		t.Fatalf("empty code: loc=%q, want an err redirect", loc)
	}
}

func TestPromotionsPageCreate_InvalidValueRejected(t *testing.T) {
	mux, _ := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"BADVAL"}, "type": {"amount"}, "value_amount": {"0"},
	}, &manager)
	if rec.Header().Get("Location") != "/promotions?err=promotions.error.value_invalid" {
		t.Fatalf("zero value: loc=%q", rec.Header().Get("Location"))
	}

	rec = postForm(mux, "/api/promotions", url.Values{
		"code": {"BADVAL2"}, "type": {"amount"}, "value_amount": {"not-a-number"},
	}, &manager)
	if rec.Header().Get("Location") != "/promotions?err=promotions.error.value_invalid" {
		t.Fatalf("non-numeric value: loc=%q", rec.Header().Get("Location"))
	}
}

func TestPromotionsPageCreate_DatesInvalidRejected(t *testing.T) {
	mux, _ := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"BADDATES"}, "type": {"amount"}, "value_amount": {"5.00"},
		"starts_at": {"2026-12-31"}, "ends_at": {"2026-01-01"},
	}, &manager)
	if rec.Header().Get("Location") != "/promotions?err=promotions.error.dates_invalid" {
		t.Fatalf("ends before starts: loc=%q", rec.Header().Get("Location"))
	}
}

// Review finding (2026-08-13): the end date a shop picks must include that
// whole day. FindActivePromo filters on datetime(ends_at) >=
// CURRENT_TIMESTAMP, and datetime('2026-08-13') is midnight at the START of
// the 13th -- so storing the bare date made a promo ending today already
// expired, and a same-day promo impossible to redeem at all.
func TestPromotionsPageCreate_EndDateIsInclusiveOfThatDay(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	today := time.Now().UTC().Format("2006-01-02")

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"ONEDAY"}, "type": {"amount"}, "value_amount": {"5.00"},
		"starts_at": {today}, "ends_at": {today},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("create: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	if _, _, ok := data.NewPOSRepo(d.Db).FindActivePromo(t.Context(), "", "ONEDAY"); !ok {
		var stored string
		_ = d.Db.QueryRow(`SELECT ends_at FROM promotions WHERE code='ONEDAY'`).Scan(&stored)
		t.Fatalf("a promo starting and ending today must be redeemable today (ends_at=%q)", stored)
	}

	// The list and the edit form still show the plain calendar date the
	// shop typed, not the stored end-of-day bound.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "23:59:59") {
		t.Fatalf("stored end-of-day bound leaked into the page: %s", rec.Body.String())
	}

	// Re-saving an unchanged row must not drift the bound further out.
	rec = postForm(mux, "/api/promotions/ONEDAY/edit", url.Values{
		"type": {"amount"}, "value_amount": {"5.00"},
		"starts_at": {today}, "ends_at": {today},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: code=%d", rec.Code)
	}
	var stored string
	if err := d.Db.QueryRow(`SELECT ends_at FROM promotions WHERE code='ONEDAY'`).Scan(&stored); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if stored != today+" 23:59:59" {
		t.Fatalf("ends_at after re-save = %q, want %q", stored, today+" 23:59:59")
	}
}

// Review finding (2026-08-13): a percent discount over 100% is not a real
// promotion -- the engine clamps the total at zero, so the stored value
// misrepresents what the shop meant. Same range settings_page.go's
// payment-fee percent enforces.
func TestPromotionsPageCreate_PercentOver100Rejected(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"TOOMUCH"}, "type": {"percent"}, "value_percent": {"500"},
	}, &manager)
	if rec.Header().Get("Location") != "/promotions?err=promotions.error.value_invalid" {
		t.Fatalf("500%%: loc=%q", rec.Header().Get("Location"))
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code='TOOMUCH'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected promo must not be stored: n=%d err=%v", n, err)
	}

	// 100% off is still a legitimate promotion and stays allowed.
	rec = postForm(mux, "/api/promotions", url.Values{
		"code": {"FREEBIE"}, "type": {"percent"}, "value_percent": {"100"},
	}, &manager)
	if rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("100%%: loc=%q", rec.Header().Get("Location"))
	}
}

// Review finding (2026-08-13): junk in a date field must be rejected, not
// stored. SQLite's datetime() yields NULL for it, and FindActivePromo's
// comparison then silently never matches -- a promo that looks saved but
// can never be redeemed.
func TestPromotionsPageCreate_MalformedDateRejected(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"JUNKDATE"}, "type": {"amount"}, "value_amount": {"5.00"},
		"ends_at": {"not-a-date"},
	}, &manager)
	if rec.Header().Get("Location") != "/promotions?err=promotions.error.dates_invalid" {
		t.Fatalf("junk date: loc=%q", rec.Header().Get("Location"))
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code='JUNKDATE'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected promo must not be stored: n=%d err=%v", n, err)
	}
}

func TestPromotionsPageCreate_AmountAndList(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"SUMMER5"}, "type": {"amount"}, "value_amount": {"5.00"},
		"description": {"Summer £5 off"},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("create: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// Stored as minor units (500), not major (5).
	var value int64
	if err := d.Db.QueryRow(`SELECT value FROM promotions WHERE code = 'SUMMER5'`).Scan(&value); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if value != 500 {
		t.Fatalf("value = %d, want 500 minor units", value)
	}

	// Shows up on the list page.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SUMMER5") {
		t.Fatalf("promotions page missing new code: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// Audited.
	var auditCount int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'promotion' AND entity_id = 'SUMMER5' AND action = 'promotion_create'`).Scan(&auditCount); err != nil {
		t.Fatalf("audit lookup: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit rows for create = %d, want 1", auditCount)
	}
}

// ut-docs#1400: parsePromotionForm's "amount" case hardcoded `* 100`
// regardless of the active currency's decimals -- an operator entering
// "500" for ¥500 on a 0-decimal shop (IRR/IRT/IQD/AFN/JPY) got 50000 minor
// units persisted instead of 500. Mirrors shifts_page_test.go's
// TestShiftsPage_CarryForwardDisplayIsCurrencyAware currency-switch pattern.
func TestPromotionsPageCreate_AmountIsCurrencyDecimalsAware(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") })

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"YEN500"}, "type": {"amount"}, "value_amount": {"500"},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("create: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	var value int64
	if err := d.Db.QueryRow(`SELECT value FROM promotions WHERE code = 'YEN500'`).Scan(&value); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if value != 500 {
		t.Fatalf("value = %d, want 500 minor units under a 0-decimal currency (not 50000)", value)
	}
}

func TestPromotionsPageCreate_Percent(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"TENPCT"}, "type": {"percent"}, "value_percent": {"10"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var value int64
	var typ string
	if err := d.Db.QueryRow(`SELECT type, value FROM promotions WHERE code = 'TENPCT'`).Scan(&typ, &value); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if typ != "percent" || value != 1000 {
		t.Fatalf("type=%q value=%d, want percent/1000", typ, value)
	}
}

func TestPromotionsPageCreate_DuplicateCodeRedirectsWithFriendlyError(t *testing.T) {
	mux, _ := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"DUP1"}, "type": {"amount"}, "value_amount": {"1.00"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("first create: code=%d", rec.Code)
	}

	rec = postForm(mux, "/api/promotions", url.Values{
		"code": {"DUP1"}, "type": {"amount"}, "value_amount": {"2.00"},
	}, &manager)
	if rec.Header().Get("Location") != "/promotions?err=promotions.error.code_exists" {
		t.Fatalf("duplicate create: loc=%q", rec.Header().Get("Location"))
	}
}

func TestPromotionsPageEdit_UpdatesButNotCode(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"EDITME2"}, "type": {"amount"}, "value_amount": {"1.00"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}

	rec = postForm(mux, "/api/promotions/EDITME2/edit", url.Values{
		"type": {"percent"}, "value_percent": {"15"}, "description": {"Updated desc"},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("edit: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	var typ, desc string
	var value int64
	if err := d.Db.QueryRow(`SELECT type, value, description FROM promotions WHERE code = 'EDITME2'`).Scan(&typ, &value, &desc); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if typ != "percent" || value != 1500 || desc != "Updated desc" {
		t.Fatalf("edit did not apply: type=%q value=%d desc=%q", typ, value, desc)
	}

	// Code is immutable: no row under any other code, and EDITME2 still exists.
	var count int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'EDITME2'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("code changed unexpectedly: count=%d err=%v", count, err)
	}

	var auditCount int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'promotion' AND entity_id = 'EDITME2' AND action = 'promotion_edit'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit rows for edit = %d, err=%v", auditCount, err)
	}
}

func TestPromotionsPageDeactivateReactivate(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"TOGGLE2"}, "type": {"amount"}, "value_amount": {"1.00"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}

	rec = postForm(mux, "/api/promotions/TOGGLE2/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("deactivate: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM promotions WHERE code = 'TOGGLE2'`).Scan(&active); err != nil || active != 0 {
		t.Fatalf("not deactivated: active=%d err=%v", active, err)
	}

	// Reflected in the (still-shown) list, both active and inactive rows appear.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "TOGGLE2") {
		t.Fatalf("deactivated promo should still be listed: body=%s", rec.Body.String())
	}

	rec = postForm(mux, "/api/promotions/TOGGLE2/active", url.Values{"active": {"1"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reactivate: code=%d", rec.Code)
	}
	if err := d.Db.QueryRow(`SELECT is_active FROM promotions WHERE code = 'TOGGLE2'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("not reactivated: active=%d err=%v", active, err)
	}

	var auditCount int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'promotion' AND entity_id = 'TOGGLE2'`).Scan(&auditCount); err != nil || auditCount != 3 { // create + deactivate + activate
		t.Fatalf("audit rows for TOGGLE2 = %d, want 3, err=%v", auditCount, err)
	}
}
