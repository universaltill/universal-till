package pages

// ut-docs#5588: the promotion_edit audit row carries a field diff.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func promoEditAuditJSON(t *testing.T, d *common.Deps, code string) string {
	t.Helper()
	var s string
	if err := d.Db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type='promotion' AND entity_id=? AND action='promotion_edit'`, code).Scan(&s); err != nil {
		t.Fatalf("audit payload for %s: %v", code, err)
	}
	return s
}

func assertPromoEditChanges(t *testing.T, d *common.Deps, code string, want map[string]any) {
	t.Helper()
	raw := promoEditAuditJSON(t, d, code)
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("payload %q not JSON: %v", raw, err)
	}
	if !reflect.DeepEqual(got, map[string]any{"changes": want}) {
		t.Fatalf("payload = %s, want changes %v", raw, want)
	}
}

func createPromoForAudit(t *testing.T, mux *http.ServeMux, m *auth.User, form url.Values) {
	t.Helper()
	if rec := postForm(mux, "/api/promotions", form, m); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rec.Code)
	}
}

func TestPromotionsPageEdit_AuditRecordsCustomerChange(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	m := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	createPromoForAudit(t, mux, &m, url.Values{"code": {"CHG"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-live-uuid"}})
	if rec := postForm(mux, "/api/promotions/CHG/edit", url.Values{"type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-other-uuid"}}, &m); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	assertPromoEditChanges(t, d, "CHG", map[string]any{
		"customer_id": map[string]any{"from": "cust-live-uuid", "to": "cust-other-uuid"},
	})
	raw := promoEditAuditJSON(t, d, "CHG")
	if strings.Contains(raw, "Other Person") || strings.Contains(raw, "Co") {
		t.Fatalf("customer name leaked into audit payload: %s", raw)
	}
}

func TestPromotionsPageEdit_AuditRecordsValueChange(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	m := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	createPromoForAudit(t, mux, &m, url.Values{"code": {"VAL"}, "type": {"amount"}, "value_amount": {"1.00"}})
	if rec := postForm(mux, "/api/promotions/VAL/edit", url.Values{"type": {"amount"}, "value_amount": {"2.50"}}, &m); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	assertPromoEditChanges(t, d, "VAL", map[string]any{
		"value": map[string]any{"from": float64(100), "to": float64(250)},
	})
}

func TestPromotionsPageEdit_AuditRecordsCustomerClear(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	m := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	createPromoForAudit(t, mux, &m, url.Values{"code": {"CLR"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-live-uuid"}})
	if rec := postForm(mux, "/api/promotions/CLR/edit", url.Values{"type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {""}}, &m); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	assertPromoEditChanges(t, d, "CLR", map[string]any{
		"customer_id": map[string]any{"from": "cust-live-uuid", "to": ""},
	})
}

func TestPromotionsPageEdit_AuditOmittedCustomerNotInChanges(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	m := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	createPromoForAudit(t, mux, &m, url.Values{"code": {"KEEP"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-live-uuid"}})
	if rec := postForm(mux, "/api/promotions/KEEP/edit", url.Values{"type": {"amount"}, "value_amount": {"2.00"}}, &m); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	assertPromoEditChanges(t, d, "KEEP", map[string]any{
		"value": map[string]any{"from": float64(100), "to": float64(200)},
	})
}

func TestPromotionsPageEdit_AuditNoOpEditHasEmptyChanges(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	m := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	createPromoForAudit(t, mux, &m, url.Values{"code": {"NOP"}, "type": {"amount"}, "value_amount": {"1.00"}})
	if rec := postForm(mux, "/api/promotions/NOP/edit", url.Values{"type": {"amount"}, "value_amount": {"1.00"}}, &m); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	if n, _ := promoEditAudits(t, d, "NOP"); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	assertPromoEditChanges(t, d, "NOP", map[string]any{})
}

func TestPromotionsPageEdit_RefusedEditWritesNoAudit(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	m := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	createPromoForAudit(t, mux, &m, url.Values{"code": {"RF"}, "type": {"amount"}, "value_amount": {"1.00"}, "customer_id": {"cust-live-uuid"}})
	postForm(mux, "/api/promotions/RF/edit", url.Values{"type": {"amount"}, "value_amount": {"9.00"}, "customer_id": {"nope"}}, &m)
	if n, _ := promoEditAudits(t, d, "RF"); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
}

// Review (ut-docs#5588): the form re-sends the bare end date and the page
// stores it as "YYYY-MM-DD 23:59:59" (endsAtInclusive); the diff compares the
// stored forms, so resubmitting an unchanged dated, customer-bound promo
// must not report a spurious ends_at / starts_at / customer_id change.
func TestPromotionsPageEdit_AuditNoOpWithDatesAndCustomer(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPromoCust(t, d)
	m := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	form := url.Values{"code": {"DATED"}, "type": {"percent"}, "value_percent": {"10"}, "description": {"spring"},
		"starts_at": {"2031-03-01"}, "ends_at": {"2031-03-31"}, "customer_id": {"cust-live-uuid"}}
	createPromoForAudit(t, mux, &m, form)
	form.Del("code")
	if rec := postForm(mux, "/api/promotions/DATED/edit", form, &m); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	assertPromoEditChanges(t, d, "DATED", map[string]any{})
}
