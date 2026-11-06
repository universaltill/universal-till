package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	// audit_log.actor_id has a real FK to users(id); tests inject the
	// manager session via auth.WithUser without a real login.
	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES ('m1', 'm1', 'Manager', 'manager')`); err != nil {
		t.Fatalf("seed test manager: %v", err)
	}

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
	// ut-docs#1458: GET /promotions must render the full layout on 403 too,
	// not a bare rail-less body (same fix class as tables_page.go/#1455).
	if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
		t.Fatalf("cashier's 403 on GET /promotions has no nav rail:\n%s", body)
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
		"starts_at": {"2026-12-31"}, "ends_at": {"2026-01-01"}, // fixed-date:ok start>end validation only, no clock
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

// ut-docs#1290: newPromotionView's ValueAmountMajor prefill (the amount-type
// edit input's value attribute) hardcoded a 2-decimal `%.2f` against `/100`,
// same defect class as ut-docs#1274's CarryForwardDisplay -- silently wrong
// on a 0-decimal currency (IRR/IRT/IQD/AFN/JPY), where minor units ARE major
// units (500 minor rendered "5.00" instead of "500"). Inserts the row
// directly at a known minor value so this test isolates the display/prefill
// fix from the (separate, out-of-scope) value_amount write-path parsing.
func TestPromotionsPageList_ValueAmountMajorIsCurrencyAware(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	if _, err := d.Db.Exec(`INSERT INTO promotions(code, type, value, is_active) VALUES('IRTPROMO', 'amount', 500, 1)`); err != nil {
		t.Fatal(err)
	}

	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), manager)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /promotions = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	httpx.InitCurrency("GBP")
	body := get()
	if !strings.Contains(body, `data-field-value_amount="5.00"`) {
		t.Fatalf("expected the 2-decimal prefill for GBP, got:\n%s", body)
	}

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	body = get()
	if !strings.Contains(body, `data-field-value_amount="500"`) {
		t.Fatalf("expected the 0-decimal prefill \"500\" (no /100) for IRT, got:\n%s", body)
	}
	if strings.Contains(body, `data-field-value_amount="5.00"`) {
		t.Fatalf("expected NO 2-decimal prefill left over once currency is 0-decimal, got:\n%s", body)
	}
}

// ut-docs#4097 (#4086 precedent): a percent promotion's edit prefill and list
// column used a bare %.2f, so a comma-decimal till showed "7.50". Both are now
// localized; the save path already accepts either separator (#2954).
func TestPromotionsPageList_ValuePercentIsLocaleAware(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	if _, err := d.Db.Exec(`INSERT INTO promotions(code, type, value, is_active) VALUES('PCTLOC', 'percent', 750, 1)`); err != nil {
		t.Fatal(err)
	}

	get := func(path string) string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), manager)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	de := get("/promotions?lang=de")
	if !strings.Contains(de, `data-field-value_percent="7,50"`) || !strings.Contains(de, "7,50%") {
		t.Fatalf("lang=de: expected value=\"7,50\" and 7,50%%, got:\n%s", de)
	}
	if strings.Contains(de, `data-field-value_percent="7.50"`) {
		t.Fatalf("lang=de: unexpected dot-decimal prefill")
	}
	for _, path := range []string{"/promotions?lang=en", "/promotions"} {
		en := get(path)
		if !strings.Contains(en, `data-field-value_percent="7.50"`) || !strings.Contains(en, "7.50%") {
			t.Fatalf("%s: expected value=\"7.50\" and 7.50%%, got:\n%s", path, en)
		}
	}
}

// Round-trip: the comma prefill shown to a de user posts back to 750.
func TestPromotionsPageEdit_PercentCommaPrefillRoundTrips(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	if _, err := d.Db.Exec(`INSERT INTO promotions(code, type, value, is_active) VALUES('PCTRT', 'percent', 750, 1)`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/promotions/PCTRT/edit", url.Values{
		"type": {"percent"}, "value_percent": {"7,50"}, "description": {"rt"},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("edit: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var value int64
	if err := d.Db.QueryRow(`SELECT value FROM promotions WHERE code = 'PCTRT'`).Scan(&value); err != nil || value != 750 {
		t.Fatalf("stored value=%d err=%v, want 750", value, err)
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

// ut-docs#3661: the inline row-edit form is gone; every control now lives in
// the record dialog (#promotion-form), each inside a <label> that names it
// (the #3662 accessible-name contract moved here), and the row carries the
// data-record-* hooks record-dialog.js reads.
func TestPromotionsPageList_DialogSlotContract(t *testing.T) {
	mux, _ := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"NAMED"}, "type": {"percent"}, "value_percent": {"10"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, bad := range []string{"users-inline", `<form method="post" action="/api/promotions/NAMED/edit"`, "x-data"} {
		if strings.Contains(body, bad) {
			t.Errorf("page still carries the old inline-form markup %q", bad)
		}
	}
	for _, want := range []string{
		`data-record-open="promotion-dialog"`,
		`data-record-action="/api/promotions/NAMED/edit"`,
		`data-record-destructive-action="/api/promotions/NAMED/active"`,
		`id="promotion-dialog-msg"`, `id="promotions-new"`, `id="promotions-no-results"`,
		`data-field-code="NAMED"`, `data-field-type="percent"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The pencil is icon-only: aria-label + title.
	if !regexp.MustCompile(`(?s)data-record-edit[^>]*aria-label="Edit"[^>]*title="Edit"`).MatchString(body) {
		t.Errorf("row pencil lacks aria-label+title")
	}

	i := strings.Index(body, `<form id="promotion-form"`)
	if i < 0 {
		t.Fatalf("#promotion-form missing")
	}
	end := strings.Index(body[i:], "</form>")
	if end < 0 {
		t.Fatalf("#promotion-form not closed")
	}
	form := body[i : i+end]
	// Every named control sits inside a <label> whose text names it.
	want := map[string]string{
		"code": "Code", "type": "Type", "value_amount": "Value", "value_percent": "Value",
		"description": "Description", "starts_at": "Starts", "ends_at": "Ends",
	}
	for name, label := range want {
		j := strings.Index(form, `name="`+name+`"`)
		if j < 0 {
			t.Fatalf("control %s missing from the dialog form", name)
		}
		open := strings.LastIndex(form[:j], "<label")
		closed := strings.LastIndex(form[:j], "</label>")
		if open < 0 || open < closed {
			t.Errorf("control %s is not inside a <label>", name)
			continue
		}
		if !strings.Contains(form[open:j], label) {
			t.Errorf("control %s: label text does not contain %q: %s", name, label, form[open:j])
		}
	}
	if !strings.Contains(form, `type="date"`) || strings.Count(form, `type="date"`) != 2 {
		t.Errorf("want exactly two date inputs in the dialog form")
	}
	if !strings.Contains(form, `type="hidden" name="customer_id"`) {
		t.Errorf("hidden customer_id missing")
	}
	if !strings.Contains(form, `id="promo-cust-q"`) || !strings.Contains(form, `data-promo-customer`) {
		t.Errorf("customer picker missing / not wrapped in data-promo-customer")
	}
	if strings.Contains(form, "width:5rem") {
		t.Errorf("inline width style must be gone")
	}
}

// ut-docs#3661 (UX A1): Starts/Ends merge into one "Valid" column, so the
// table is 8 columns wide and an empty side renders an en dash.
func TestPromotionsPageList_ValidColumnMergesDates(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	if _, err := d.Db.Exec(`INSERT INTO promotions(code, type, value, starts_at, is_active) VALUES('VALID1', 'percent', 500, '2031-03-04', 1)`); err != nil { // fixed-date:ok no real-clock check reads it
		t.Fatal(err)
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "<th>Valid</th>") {
		t.Errorf("want a single Valid header")
	}
	if strings.Contains(body, "<th>Starts</th>") || strings.Contains(body, "<th>Ends</th>") {
		t.Errorf("separate Starts/Ends headers must be gone")
	}
	// Scoped to VALID1's own Valid cell: a page-wide en dash check passed
	// with the dash rendering removed (ut-docs#3661 review).
	cell := regexp.MustCompile(`(?s)data-id="VALID1".*?<td class="promo-dates">(.*?)</td>`).FindStringSubmatch(body)
	if cell == nil {
		t.Fatalf("VALID1 row has no promo-dates cell:\n%s", body)
	}
	if want := `<span class="promo-date">2031-03-04</span><span class="promo-date">–</span>`; cell[1] != want { // fixed-date:ok no real-clock check reads it
		t.Errorf("Valid cell = %q, want %q (start, then an en dash for the open end)", cell[1], want)
	}
	if !strings.Contains(body, "data-card-keep") {
		t.Errorf("status cell should carry data-card-keep")
	}
	if !strings.Contains(body, `<bdi dir="ltr">VALID1</bdi>`) {
		t.Errorf("code should be wrapped in a ltr bdi")
	}
}

// ut-docs#3661: refusals from the dialog's hx-boosted forms render in the
// dialog's message region (400 text/html fragment), never a redirect.
func TestPromotionsPage_RefusalRendersInDialogMessageForHtmxRequest(t *testing.T) {
	mux, _ := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	check := func(name string, rec *httptest.ResponseRecorder, wantText string) {
		t.Helper()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d body=%s", name, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Location") != "" {
			t.Errorf("%s: htmx refusal must not redirect", name)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q, want text/html", name, ct)
		}
		body := rec.Body.String()
		if !strings.Contains(body, wantText) {
			t.Errorf("%s: body %q lacks %q", name, body, wantText)
		}
		if strings.Contains(body, "id=") || strings.Contains(body, "<form") || strings.Contains(body, "<dialog") {
			t.Errorf("%s: response must be only the message text: %s", name, body)
		}
	}

	check("bad value", postFormHtmx(mux, "/api/promotions", url.Values{
		"code": {"HX1"}, "type": {"amount"}, "value_amount": {"nope"},
	}, &manager), "Enter a valid value")
	check("empty code", postFormHtmx(mux, "/api/promotions", url.Values{
		"code": {"  "}, "type": {"amount"}, "value_amount": {"5"},
	}, &manager), "Code is required")

	if rec := postFormHtmx(mux, "/api/promotions", url.Values{
		"code": {"HXDUP"}, "type": {"amount"}, "value_amount": {"5"},
	}, &manager); rec.Code != http.StatusOK {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	check("duplicate", postFormHtmx(mux, "/api/promotions", url.Values{
		"code": {"HXDUP"}, "type": {"amount"}, "value_amount": {"5"},
	}, &manager), "That code already exists")

	check("edit bad date", postFormHtmx(mux, "/api/promotions/HXDUP/edit", url.Values{
		"type": {"amount"}, "value_amount": {"5"}, "starts_at": {"2031-02-02"}, "ends_at": {"2031-01-01"}, // fixed-date:ok
	}, &manager), "End date can")
	check("edit bad value", postFormHtmx(mux, "/api/promotions/HXDUP/edit", url.Values{
		"type": {"percent"}, "value_percent": {"500"},
	}, &manager), "Enter a valid value")
}

func TestPromotionsPage_HtmxSuccessSendsHXRedirect(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	for _, c := range []struct {
		name, path string
		form       url.Values
		action     string
	}{
		{"create", "/api/promotions", url.Values{"code": {"HXOK"}, "type": {"amount"}, "value_amount": {"5"}}, "promotion_create"},
		{"edit", "/api/promotions/HXOK/edit", url.Values{"type": {"amount"}, "value_amount": {"6"}}, "promotion_edit"},
		{"deactivate", "/api/promotions/HXOK/active", url.Values{"active": {"0"}}, "promotion_deactivate"},
		{"activate", "/api/promotions/HXOK/active", url.Values{"active": {"1"}}, "promotion_activate"},
	} {
		rec := postFormHtmx(mux, c.path, c.form, &manager)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d body=%s", c.name, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("HX-Redirect"); got != "/promotions" {
			t.Fatalf("%s: HX-Redirect = %q, want /promotions", c.name, got)
		}
		if rec.Header().Get("Location") != "" {
			t.Errorf("%s: a bare Location must not accompany HX-Redirect", c.name)
		}
		var n int
		if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id='HXOK' AND action=?`, c.action).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: audit rows = %d err=%v", c.name, n, err)
		}
	}
}

// ut-docs#3661 review: the code is free text and is the row's URL path
// segment. The row's data-record-action is not URL context to html/template
// (the old inline form's action was), so it must arrive path-escaped: a raw
// "10%OFF" is an invalid escape the server answers 400, and "#", "?" or "/"
// would cut the path short. The escaped path must reach the right row.
func TestPromotionsPageList_RowActionsPathEscapeTheCode(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	for _, c := range []struct{ code, seg string }{
		{"10%OFF", "10%25OFF"},
		{"A#B", "A%23B"},
		{"A?B", "A%3FB"},
		{"A/B", "A%2FB"},
		{"SUMMER 10", "SUMMER%2010"},
	} {
		if rec := postForm(mux, "/api/promotions", url.Values{
			"code": {c.code}, "type": {"amount"}, "value_amount": {"5"},
		}, &manager); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: create %d", c.code, rec.Code)
		}
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), manager)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		body := rec.Body.String()
		for _, want := range []string{
			`data-record-action="/api/promotions/` + c.seg + `/edit"`,
			`data-record-destructive-action="/api/promotions/` + c.seg + `/active"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: page lacks %s", c.code, want)
			}
		}
		if rec := postFormHtmx(mux, "/api/promotions/"+c.seg+"/edit", url.Values{
			"type": {"amount"}, "value_amount": {"7"},
		}, &manager); rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/promotions" {
			t.Errorf("%s: edit via escaped path: %d %q %s", c.code, rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
		}
		if rec := postFormHtmx(mux, "/api/promotions/"+c.seg+"/active", url.Values{"active": {"0"}}, &manager); rec.Code != http.StatusOK {
			t.Errorf("%s: deactivate via escaped path: %d %s", c.code, rec.Code, rec.Body.String())
		}
		var value, active int
		if err := d.Db.QueryRow(`SELECT value, is_active FROM promotions WHERE code = ?`, c.code).Scan(&value, &active); err != nil {
			t.Fatalf("%s: %v", c.code, err)
		}
		if value != 700 || active != 0 {
			t.Errorf("%s: value=%d active=%d, want 700/0", c.code, value, active)
		}
	}
}

// ---- ut-docs#5574: customer name in the list; change/clear on edit ----

func seedPromoCust(t *testing.T, d *common.Deps) {
	t.Helper()
	if _, err := d.Db.Exec(`INSERT INTO customers (id, name) VALUES
		('cust-live-uuid', 'A <b>&amp; Co'), ('cust-other-uuid', 'Other Person'), ('cust-shell-uuid', '')`); err != nil {
		t.Fatalf("seed customers: %v", err)
	}
}

func promoCustomerID(t *testing.T, d *common.Deps, code string) (string, bool) {
	t.Helper()
	var id *string
	if err := d.Db.QueryRow(`SELECT customer_id FROM promotions WHERE code = ?`, code).Scan(&id); err != nil {
		t.Fatalf("lookup %s: %v", code, err)
	}
	if id == nil {
		return "", false
	}
	return *id, true
}

func promoEditAudits(t *testing.T, d *common.Deps, code string) (n int, actor string) {
	t.Helper()
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='promotion' AND entity_id=? AND action='promotion_edit'`, code).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if n > 0 {
		_ = d.Db.QueryRow(`SELECT actor_id FROM audit_log WHERE entity_type='promotion' AND entity_id=? AND action='promotion_edit'`, code).Scan(&actor)
	}
	return n, actor
}

func getPromotionsBody(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /promotions = %d", rec.Code)
	}
	return rec.Body.String()
}

var promoTDRe = regexp.MustCompile(`(?s)<td[^>]*>.*?</td>`)

func TestPromotionsPageList_ShowsCustomerNameNotID(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	if rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"NAMED"}, "type": {"amount"}, "value_amount": {"5.00"}, "customer_id": {"cust-live-uuid"},
	}, &manager); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rec.Code)
	}
	body := getPromotionsBody(t, mux)
	if !strings.Contains(body, `<bdi dir="auto">A &lt;b&gt;&amp;amp; Co</bdi>`) {
		t.Fatalf("escaped name in bdi dir=auto missing:\n%s", body)
	}
	if strings.Contains(body, "<b>&amp; Co") {
		t.Fatalf("customer name rendered unescaped")
	}
	for _, td := range promoTDRe.FindAllString(body, -1) {
		if strings.Contains(td, "cust-live-uuid") {
			t.Fatalf("UUID visible in a cell: %s", td)
		}
	}
	if !strings.Contains(body, `data-field-customer_id="cust-live-uuid"`) {
		t.Fatalf("row lost data-field-customer_id")
	}
	if !strings.Contains(body, `data-promo-customer-label="A &lt;b&gt;&amp;amp; Co"`) {
		t.Fatalf("row lacks the escaped customer label attribute")
	}
}

func TestPromotionsPageList_DeletedCustomerLabel(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	// Shell (name '') and a legacy dangling id (FKs off on one conn only).
	if _, err := d.Db.Exec(`INSERT INTO promotions (code,type,value,customer_id,is_active) VALUES ('SHELLP','amount',100,'cust-shell-uuid',1)`); err != nil {
		t.Fatal(err)
	}
	conn, err := d.Db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.ExecContext(t.Context(), `PRAGMA foreign_keys = OFF`)
	_, err = conn.ExecContext(t.Context(), `INSERT INTO promotions (code,type,value,customer_id,is_active) VALUES ('GONEP','amount',100,'cust-gone-uuid',1)`)
	_, _ = conn.ExecContext(t.Context(), `PRAGMA foreign_keys = ON`)
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	body := getPromotionsBody(t, mux)
	if n := strings.Count(body, `<span class="muted">Deleted customer</span>`); n != 2 {
		t.Fatalf("Deleted customer label count = %d, want 2", n)
	}
	for _, td := range promoTDRe.FindAllString(body, -1) {
		if strings.Contains(td, "cust-shell-uuid") || strings.Contains(td, "cust-gone-uuid") {
			t.Fatalf("UUID visible in a cell: %s", td)
		}
	}
}

func TestPromotionsPageEdit_ChangesCustomer_AuditedOnce(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	postForm(mux, "/api/promotions", url.Values{"code": {"CHG"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-live-uuid"}}, &manager)
	rec := postForm(mux, "/api/promotions/CHG/edit", url.Values{
		"type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-other-uuid"},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("edit: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if id, _ := promoCustomerID(t, d, "CHG"); id != "cust-other-uuid" {
		t.Fatalf("customer_id = %q", id)
	}
	if n, actor := promoEditAudits(t, d, "CHG"); n != 1 || actor != "m1" {
		t.Fatalf("audit rows=%d actor=%q", n, actor)
	}
}

func TestPromotionsPageEdit_ClearsCustomer_AuditedOnce(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	postForm(mux, "/api/promotions", url.Values{"code": {"CLR"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-live-uuid"}}, &manager)
	rec := postForm(mux, "/api/promotions/CLR/edit", url.Values{
		"type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {""},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	if _, bound := promoCustomerID(t, d, "CLR"); bound {
		t.Fatal("customer_id should be NULL after clear")
	}
	if n, actor := promoEditAudits(t, d, "CLR"); n != 1 || actor != "m1" {
		t.Fatalf("audit rows=%d actor=%q", n, actor)
	}
	if !strings.Contains(getPromotionsBody(t, mux), `<span class="muted">Any customer</span>`) {
		t.Fatal("list should show Any customer")
	}
}

func TestPromotionsPageEdit_OmittedCustomerIDKeepsBinding(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	postForm(mux, "/api/promotions", url.Values{"code": {"KEEP"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-live-uuid"}}, &manager)
	rec := postForm(mux, "/api/promotions/KEEP/edit", url.Values{
		"type": {"amount"}, "value_amount": {"2.00"}, "description": {"d"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	if id, _ := promoCustomerID(t, d, "KEEP"); id != "cust-live-uuid" {
		t.Fatalf("binding lost: %q", id)
	}
}

func TestPromotionsPageEdit_UnknownCustomerRefused(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	postForm(mux, "/api/promotions", url.Values{"code": {"REF"}, "type": {"amount"}, "value_amount": {"1.00"}, "description": {"orig"}, "customer_id": {"cust-live-uuid"}}, &manager)
	form := url.Values{"type": {"amount"}, "value_amount": {"9.00"}, "description": {"changed"}, "customer_id": {"nope"}}

	rec := postFormHtmx(mux, "/api/promotions/REF/edit", form, &manager)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "That customer no longer exists on this till") {
		t.Fatalf("htmx: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "foreign key") {
		t.Fatal("raw FK text leaked")
	}
	rec = postForm(mux, "/api/promotions/REF/edit", form, &manager)
	if rec.Header().Get("Location") != "/promotions?err=promotions.error.customer_unknown" {
		t.Fatalf("non-htmx loc=%q", rec.Header().Get("Location"))
	}
	var desc string
	_ = d.Db.QueryRow(`SELECT description FROM promotions WHERE code='REF'`).Scan(&desc)
	if id, _ := promoCustomerID(t, d, "REF"); id != "cust-live-uuid" || desc != "orig" {
		t.Fatalf("DB changed: id=%q desc=%q", id, desc)
	}
	if n, _ := promoEditAudits(t, d, "REF"); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
}

func TestPromotionsPageEdit_DeletedCustomerBindingSurvivesEdit(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	if _, err := d.Db.Exec(`INSERT INTO promotions (code,type,value,customer_id,is_active) VALUES ('SHELLK','amount',100,'cust-shell-uuid',1)`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/promotions/SHELLK/edit", url.Values{
		"type": {"amount"}, "value_amount": {"1.00"}, "description": {"x"}, "customer_id": {"cust-shell-uuid"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit of deleted-customer promo: %d", rec.Code)
	}
	if id, _ := promoCustomerID(t, d, "SHELLK"); id != "cust-shell-uuid" {
		t.Fatalf("binding changed: %q", id)
	}
}

func TestPromotionsPageCreate_UnknownCustomerRefused(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	form := url.Values{"code": {"NEWX"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"nope"}}
	rec := postFormHtmx(mux, "/api/promotions", form, &manager)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "That customer no longer exists on this till") {
		t.Fatalf("htmx: %d %s", rec.Code, rec.Body.String())
	}
	rec = postForm(mux, "/api/promotions", form, &manager)
	if rec.Header().Get("Location") != "/promotions?err=promotions.error.customer_unknown" {
		t.Fatalf("loc=%q", rec.Header().Get("Location"))
	}
	var n int
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code='NEWX'`).Scan(&n)
	if n != 0 {
		t.Fatal("promotion created")
	}
}

func TestPromotionsPagePermissions_CustomerChangeForbidden(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	postForm(mux, "/api/promotions", url.Values{"code": {"PERM"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-live-uuid"}}, &manager)
	rec := postForm(mux, "/api/promotions/PERM/edit", url.Values{
		"type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-other-uuid"},
	}, &cashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier edit = %d", rec.Code)
	}
	if id, _ := promoCustomerID(t, d, "PERM"); id != "cust-live-uuid" {
		t.Fatalf("binding changed: %q", id)
	}
}
