package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6648: the cashier picks which payment (leg) of a split-bill sale a
// refund goes back to. Scan "ABC" = 2 x 100 + 20% tax = 240; qty_0=1 refunds 120.

func (r *refCardRig) postRefundLeg(t *testing.T, receipt string, qty int, leg, method string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/refund",
		strings.NewReader(fmt.Sprintf("receipt=%s&qty_0=%d&method=%s&refund_leg=%s", receipt, qty, method, leg)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

// legPaymentIDs lists a sale's payment ids in GetSaleDetail's order.
func legPaymentIDs(t *testing.T, r *refCardRig, receipt string) []string {
	t.Helper()
	rows, err := r.dp.Db.Query(`SELECT p.id FROM payments p JOIN sales s ON s.id=p.sale_id WHERE s.receipt_no=? ORDER BY p.paid_at, p.rowid`, receipt)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func refundLegRowsByID(t *testing.T, r *refCardRig) map[string]int64 {
	t.Helper()
	rows, err := r.dp.Db.Query(`SELECT original_payment_id, amount FROM refund_leg_refunds`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var a int64
		if err := rows.Scan(&id, &a); err != nil {
			t.Fatal(err)
		}
		out[id] += a
	}
	return out
}

func returnCount(t *testing.T, r *refCardRig) int {
	t.Helper()
	var n int
	_ = r.dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE sale_type='return'`).Scan(&n)
	return n
}

func newChoiceRig(t *testing.T) *refCardRig {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
	if _, err := rig.dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	return rig
}

func TestRefundLegChoice_ChosenLegGetsWholeRefund(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":90},{"method":"demopay","amount":150}]`)
	receipt := rig.receiptNo(t, 1)
	ids := legPaymentIDs(t, rig, receipt)
	rec := rig.postRefundLeg(t, receipt, 1, ids[1], "demopay")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	got := rig.refunds()
	if len(got) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(got))
	}
	if refs := originalRefs(t, got[0]); fmt.Sprint(refs) != "[pi_2]" {
		t.Errorf("original refs = %v, want [pi_2]", refs)
	}
	if a := legAmounts(t, got[0]); fmt.Sprint(a) != "[120]" {
		t.Errorf("leg amounts = %v, want [120]", a)
	}
	if rows := refundLegRowsByID(t, rig); len(rows) != 1 || rows[ids[1]] != 120 {
		t.Errorf("leg rows = %v, want {%s:120}", rows, ids[1])
	}
}

func TestRefundLegChoice_ChosenLegTooShortRefusesBeforeProvider(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt := rig.receiptNo(t, 1)
	ids := legPaymentIDs(t, rig, receipt)
	rec := rig.postRefundLeg(t, receipt, 1, ids[1], "demopay")
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if want := refundErrorText(t, "refund.error.leg_exhausted"); !strings.Contains(rec.Body.String(), want) {
		t.Errorf("body %q lacks %q", rec.Body.String(), want)
	}
	if n := len(rig.refunds()); n != 0 {
		t.Errorf("hook calls = %d, want 0", n)
	}
	if n := returnCount(t, rig); n != 0 {
		t.Errorf("return sales = %d, want 0", n)
	}
	if rows := refundLegRowsByID(t, rig); len(rows) != 0 {
		t.Errorf("leg rows = %v, want none", rows)
	}
}

func TestRefundLegChoice_NettedLegRefusesSecondRefund(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt := rig.receiptNo(t, 1)
	ids := legPaymentIDs(t, rig, receipt)
	if rec := rig.postRefundLeg(t, receipt, 1, ids[0], "demopay"); rec.Code != http.StatusOK {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	if a := legAmounts(t, rig.refunds()[0]); fmt.Sprint(a) != "[120]" {
		t.Errorf("first legs = %v, want [120]", a)
	}
	rec := rig.postRefundLeg(t, receipt, 1, ids[0], "demopay")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second code = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if want := refundErrorText(t, "refund.error.leg_exhausted"); !strings.Contains(rec.Body.String(), want) {
		t.Errorf("body %q lacks %q", rec.Body.String(), want)
	}
	if n := len(rig.refunds()); n != 1 {
		t.Errorf("hook calls = %d, want 1", n)
	}
}

func TestRefundLegChoice_AllKeepsPaymentOrder(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	// Tester: 120 of 240 so the order shows (reverse order would give [30 90]).
	rec := rig.postRefundLeg(t, rig.receiptNo(t, 1), 1, "all", "demopay")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	got := rig.refunds()
	if len(got) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(got))
	}
	if a := legAmounts(t, got[0]); fmt.Sprint(a) != "[120]" {
		t.Errorf("leg amounts = %v, want [120] (first leg first)", a)
	}
	if refs := originalRefs(t, got[0]); fmt.Sprint(refs) != "[pi_1]" {
		t.Errorf("original refs = %v, want [pi_1]", refs)
	}
}

func TestRefundLegChoice_UnknownLegIs400NothingSent(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	r1, r2 := rig.receiptNo(t, 1), rig.receiptNo(t, 2)
	ids1, ids2 := legPaymentIDs(t, rig, r1), legPaymentIDs(t, rig, r2)
	for name, leg := range map[string]string{"other sale": ids2[0], "garbage": "nope"} {
		t.Run(name, func(t *testing.T) {
			rec := rig.postRefundLeg(t, r1, 1, leg, "demopay")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if want := refundErrorText(t, "refund.error.leg_unknown"); !strings.Contains(rec.Body.String(), want) {
				t.Errorf("body %q lacks %q", rec.Body.String(), want)
			}
			if n := len(rig.refunds()); n != 0 {
				t.Errorf("hook calls = %d, want 0", n)
			}
		})
	}
	t.Run("cash ignores the field", func(t *testing.T) {
		rec := rig.postRefundLeg(t, r1, 1, ids1[0], "cash")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestRefundLegChoice_SingleLegIgnoresField(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":240}]`)
	rec := rig.postRefundLeg(t, rig.receiptNo(t, 1), 1, "nope", "demopay")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	got := rig.refunds()
	if len(got) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(got))
	}
	op := got[0]["original_payments"].([]any)[0].(map[string]any)
	if _, has := op["refund_amount"]; has {
		t.Fatalf("single-leg entry must not carry refund_amount: %v", op)
	}
}

func getRefundPage(t *testing.T, rig *refCardRig, receipt string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/refund/"+receipt, nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET refund page = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestRefundPage_LegChoiceShowsRemainingPerLeg(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt := rig.receiptNo(t, 1)
	ids := legPaymentIDs(t, rig, receipt)
	if rec := rig.postRefundLeg(t, receipt, 1, ids[0], "demopay"); rec.Code != http.StatusOK {
		t.Fatalf("refund: %d %s", rec.Code, rec.Body.String())
	}
	body := getRefundPage(t, rig, receipt)
	for _, want := range []string{`data-legs-for="demopay"`, `data-remaining="30"`, `data-remaining="90"`, `value="all"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if n := strings.Count(body, `data-testid="refund-leg-option"`); n != 2 {
		t.Errorf("leg options = %d, want 2", n)
	}
	if n := strings.Count(body, `<option value="demopay"`); n != 1 {
		t.Errorf("demopay options = %d, want 1", n)
	}
}

func TestRefundPage_NoLegChoiceForSingleOrNoTracedLeg(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":240}]`)
	if body := getRefundPage(t, rig, rig.receiptNo(t, 1)); strings.Contains(body, `name="refund_leg"`) {
		t.Error("single traced leg must not render a leg choice")
	}
	rig.sell(t, `[{"method":"cash","amount":240}]`)
	if body := getRefundPage(t, rig, rig.receiptNo(t, 2)); strings.Contains(body, `name="refund_leg"`) {
		t.Error("cash sale must not render a leg choice")
	}
}

func TestAllocateRefundToLeg(t *testing.T) {
	for _, c := range []struct {
		name  string
		total int64
		caps  []int64
		i     int
		want  []int64
		ok    bool
	}{
		{"fits", 100, []int64{150, 90}, 0, []int64{100, 0}, true},
		{"exact", 90, []int64{150, 90}, 1, []int64{0, 90}, true},
		{"short", 120, []int64{150, 90}, 1, []int64{0, 120}, false},
		{"zero cap", 1, []int64{0, 90}, 0, []int64{1, 0}, false},
	} {
		got, ok := allocateRefundToLeg(c.total, c.caps, c.i)
		if ok != c.ok || fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// Tester (ut-docs#6648): both leg refusals sit BEFORE fiscal.sign.start, so a
// refused refund never opens a TSE-side transaction (ut-docs#1519). The valid
// refund at the end proves the counter sees a start on this path.
func TestRefundLegChoice_RefusalsOpenNoFiscalStart(t *testing.T) {
	rig := newChoiceRig(t)
	var starts atomic.Int32
	t.Cleanup(func() { plugins.SharedBus(rig.dp.Db).ResetSubscribers() })
	subscribeFiscalSignStartHandler(t, rig.dp, "com.test.fiscal-start-6648", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		if p["sale_type"] == "return" {
			starts.Add(1)
		}
		return json.RawMessage(`{"status":"acknowledged","tx_id":"tx-6648","tx_revision":1}`), nil
	})
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt := rig.receiptNo(t, 1)
	ids := legPaymentIDs(t, rig, receipt)
	rig.dp.WaitForAsyncWork()
	starts.Store(0)
	if rec := rig.postRefundLeg(t, receipt, 1, ids[1], "demopay"); rec.Code != http.StatusConflict {
		t.Fatalf("leg_exhausted code = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := rig.postRefundLeg(t, receipt, 1, "nope", "demopay"); rec.Code != http.StatusBadRequest {
		t.Fatalf("leg_unknown code = %d: %s", rec.Code, rec.Body.String())
	}
	rig.dp.WaitForAsyncWork()
	if n := starts.Load(); n != 0 {
		t.Fatalf("fiscal.sign.start fired %d time(s) for refused refunds, want 0", n)
	}
	if rec := rig.postRefundLeg(t, receipt, 1, ids[0], "demopay"); rec.Code != http.StatusOK {
		t.Fatalf("valid refund code = %d: %s", rec.Code, rec.Body.String())
	}
	rig.dp.WaitForAsyncWork()
	if n := starts.Load(); n != 1 {
		t.Fatalf("fiscal.sign.start fired %d time(s) for the valid refund, want 1 (counter not wired?)", n)
	}
}
