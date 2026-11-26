package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#6858: till value-gate follow-ups. Tests that set a package-level
// hook are not parallel and reset it in t.Cleanup.

type forbiddenEnvelope struct {
	Data  any `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func requireForbiddenJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	var env forbiddenEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data != nil || env.Error == nil || env.Error.Code != "forbidden" || env.Error.Message == "" {
		t.Fatalf("envelope = %s (%v)", rec.Body.String(), err)
	}
}

// AC1b: a promo lands between the gate's read and the apply.
func TestPOSDiscount_PromoLandsBeforeApply_409NoAudit(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	saleDiscountTestHook = func() { e.dp.Engine.SetDiscount(money.FromMinor(40)) }
	t.Cleanup(func() { saleDiscountTestHook = nil })
	rec := postForm(e.mux, "/api/pos/discount", url.Values{"discount": {"100"}}, e.manager)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := e.dp.Engine.SaleDiscount().Minor(); got != 40 {
		t.Fatalf("discount = %d, want the promo's 40", got)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE action='sale_discount'`); n != 0 {
		t.Fatalf("sale_discount audit rows = %d, want 0", n)
	}
}

// AC1c: the tender gate and the audit read the engine discount once.
func TestPOSTender_AuditFromEqualsGateRead(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	tenderDiscountTestHook = func() { e.dp.Engine.SetDiscount(money.FromMinor(80)) }
	t.Cleanup(func() { tenderDiscountTestHook = nil })
	rec := gatePostTender(e, gateTenderBody("50"), e.manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_sale' AND action='sale_discount'`); n != 1 {
		t.Fatalf("want 1 sale_discount audit row, got %d", n)
	}
	var detail string
	if err := e.dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type='pos_sale' AND action='sale_discount'`).Scan(&detail); err != nil {
		t.Fatalf("want one sale_discount audit row: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(detail), &m); err != nil {
		t.Fatalf("detail %q: %v", detail, err)
	}
	if m["from"] != float64(0) || m["to"] != float64(50) {
		t.Fatalf("audit detail = %v, want from 0 to 50", m)
	}
}

// AC3a
func TestPOSReset_NonHtmx_Cashier403JSON(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	for _, form := range []url.Values{{}, {"override_pin": {gatePIN}}} {
		requireForbiddenJSON(t, postForm(e.mux, "/api/pos/reset", form, e.cashier))
		if len(e.dp.Engine.Basket().Lines) != 1 {
			t.Fatal("basket must be untouched")
		}
		if n := e.count(t, `SELECT COUNT(*) FROM cleared_baskets`); n != 0 {
			t.Fatalf("cleared_baskets = %d", n)
		}
		if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
			t.Fatalf("audit rows = %d", n)
		}
	}
	if rec := postFormHtmx(e.mux, "/api/pos/reset", url.Values{}, e.cashier); !isElevationPrompt(rec) {
		t.Fatalf("htmx must still be prompted, got %d", rec.Code)
	}
	if rec := postForm(e.mux, "/api/pos/reset", url.Values{}, e.manager); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("manager non-htmx: %d", rec.Code)
	}
	if len(e.dp.Engine.Basket().Lines) != 0 {
		t.Fatal("manager reset must clear the basket")
	}
}

// AC3b
func TestPOSLine_NonHtmx_Cashier403JSON(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
	}{
		{"reduction", url.Values{"qty": {"1"}}},
		{"raise", url.Values{"qty": {"2"}, "discount": {"10"}}},
		{"reduce_and_raise", url.Values{"qty": {"1"}, "discount": {"10"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newGateEnv(t)
			key := e.seedLine(t, 2)
			tc.form.Set("key", key)
			requireForbiddenJSON(t, postForm(e.mux, "/api/pos/line", tc.form, e.cashier))
			requireForbiddenJSON(t, postForm(e.mux, "/api/pos/line", withPIN(tc.form), e.cashier))
			if l := e.dp.Engine.Basket().Lines[0]; l.Qty != 2 || l.LineDiscount.Minor() != 0 {
				t.Fatalf("line changed: qty %v disc %d", l.Qty, l.LineDiscount.Minor())
			}
			if n := e.count(t, `SELECT COUNT(*) FROM shrinkage_events`); n != 0 {
				t.Fatalf("shrinkage_events = %d", n)
			}
			if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
				t.Fatalf("audit rows = %d", n)
			}
			if rec := postFormHtmx(e.mux, "/api/pos/line", tc.form, e.cashier); !isElevationPrompt(rec) {
				t.Fatalf("htmx must still be prompted, got %d", rec.Code)
			}
			if rec := postForm(e.mux, "/api/pos/line", tc.form, e.manager); rec.Code != http.StatusOK {
				t.Fatalf("manager non-htmx: %d", rec.Code)
			}
		})
	}
}

// AC3b, review: a reduce-and-raise post needs BOTH permissions, so a session
// holding only one of them is still refused before the PIN.
func TestPOSLine_NonHtmx_ReduceAndRaise_OnePermissionIsNotEnough(t *testing.T) {
	for _, granted := range []string{"void_comp_waste", "price_override"} {
		t.Run(granted, func(t *testing.T) {
			e := newGateEnv(t)
			key := e.seedLine(t, 2)
			if err := data.NewAuthRepo(e.dp.Db).SetRolePermission(t.Context(), nil, "cashier", granted, true); err != nil {
				t.Fatal(err)
			}
			form := url.Values{"key": {key}, "qty": {"1"}, "discount": {"10"}}
			requireForbiddenJSON(t, postForm(e.mux, "/api/pos/line", form, e.cashier))
			if l := e.dp.Engine.Basket().Lines[0]; l.Qty != 2 || l.LineDiscount.Minor() != 0 {
				t.Fatalf("line changed: qty %v disc %d", l.Qty, l.LineDiscount.Minor())
			}
		})
	}
}

func withPIN(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	out.Set("override_pin", gatePIN)
	return out
}

// AC4
func TestPOSDiscount_PlainRequest_DoesNotRetarget(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	rec := postForm(e.mux, "/api/pos/discount", url.Values{"discount": {"10"}}, e.manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("HX-Retarget"); got != "" {
		t.Fatalf("plain discount must not retarget, got %q", got)
	}
	prompt := postFormHtmx(e.mux, "/api/pos/discount", url.Values{"discount": {"500"}}, e.cashier)
	if !isElevationPrompt(prompt) || prompt.Header().Get("HX-Retarget") != "#shrinkage-hint" {
		t.Fatalf("prompt must target the hint slot: %d %v", prompt.Code, prompt.Header())
	}
	rec = approve(t, e, "/api/pos/discount", prompt, gatePIN)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Retarget") != "#basket" || rec.Header().Get("HX-Reswap") != "outerHTML" {
		t.Fatalf("approved retry must go back on the basket: %d %v", rec.Code, rec.Header())
	}
}
