package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func newPromotionsTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}}
	mux := http.NewServeMux()
	registerPromotions(mux, d)
	return mux, d
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
