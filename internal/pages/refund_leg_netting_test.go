package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/plugins"
)

// postRefundQty posts a refund of qty units of line 0 on the demopay method.
func (r *refCardRig) postRefundQty(t *testing.T, receipt string, qty int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/refund",
		strings.NewReader(fmt.Sprintf("receipt=%s&qty_0=%d&method=demopay", receipt, qty)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func legAmounts(t *testing.T, refund map[string]any) []float64 {
	t.Helper()
	raw, _ := refund["original_payments"].([]any)
	var out []float64
	for _, e := range raw {
		a, _ := e.(map[string]any)["refund_amount"].(float64)
		out = append(out, a)
	}
	return out
}

// ut-docs#6649: a second refund is netted per leg, so it moves on to the next
// leg instead of asking the first leg for more than it has left.
func TestRefundLegNetting_SecondRefundMovesToNextLeg(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
	if _, err := rig.dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt := rig.receiptNo(t, 1)

	if rec := rig.postRefundQty(t, receipt, 1); rec.Code >= 400 {
		t.Fatalf("first refund: %d %s", rec.Code, rec.Body.String())
	}
	if rec := rig.postRefundQty(t, receipt, 1); rec.Code >= 400 {
		t.Fatalf("second refund: %d %s", rec.Code, rec.Body.String())
	}
	got := rig.refunds()
	if len(got) != 2 {
		t.Fatalf("refund hook calls = %d, want 2", len(got))
	}
	if a := legAmounts(t, got[0]); fmt.Sprint(a) != "[120]" {
		t.Errorf("first refund legs = %v, want [120]", a)
	}
	if a := legAmounts(t, got[1]); fmt.Sprint(a) != "[30 90]" {
		t.Errorf("second refund legs = %v, want [30 90]", a)
	}
}

// A sale refunded before per-leg records existed: the method's earlier
// refunds are netted in payment order.
func TestRefundLegNetting_LegacyRefundWithoutRecordsNetsInPaymentOrder(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
	if _, err := rig.dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt := rig.receiptNo(t, 1)
	if rec := rig.postRefundQty(t, receipt, 1); rec.Code >= 400 {
		t.Fatalf("first refund: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := rig.dp.Db.Exec(`DELETE FROM refund_leg_refunds`); err != nil {
		t.Fatal(err)
	}
	if rec := rig.postRefundQty(t, receipt, 1); rec.Code >= 400 {
		t.Fatalf("second refund: %d %s", rec.Code, rec.Body.String())
	}
	got := rig.refunds()
	if a := legAmounts(t, got[1]); fmt.Sprint(a) != "[30 90]" {
		t.Errorf("second refund legs = %v, want [30 90]", a)
	}
}

// Never allocates more than a leg has left.
func TestNetRefundLegCaps(t *testing.T) {
	for _, c := range []struct {
		name      string
		amounts   []int64
		recorded  []int64
		unrecordd int64
		want      []int64
	}{
		{"none", []int64{150, 90}, []int64{0, 0}, 0, []int64{150, 90}},
		{"recorded", []int64{150, 90}, []int64{120, 0}, 0, []int64{30, 90}},
		{"legacy", []int64{150, 90}, []int64{0, 0}, 120, []int64{30, 90}},
		{"legacy spills", []int64{150, 90}, []int64{0, 0}, 200, []int64{0, 40}},
		{"mixed", []int64{150, 90}, []int64{0, 50}, 100, []int64{50, 40}},
		{"all gone", []int64{150, 90}, []int64{150, 90}, 10, []int64{0, 0}},
		{"over-recorded", []int64{150, 90}, []int64{999, 0}, 0, []int64{0, 90}},
	} {
		got := netRefundLegCaps(c.amounts, c.recorded, c.unrecordd)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: netRefundLegCaps = %v, want %v", c.name, got, c.want)
		}
	}
}

// ut-docs#6647: a refund_partial answer shows its own text, never suggests
// another method, and records no return.
func TestRefund_PartialDeclineShowsOwnTextAndRecordsNothing(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	for _, c := range []struct{ code, key string }{
		{"refund_partial", "refund.error.partial"},
		{"refund_outcome_unknown", "refund.error.outcome_unknown"},
		{"card_declined", "refund.error.provider_declined"},
	} {
		rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
		rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
		rig.setRefundError(&plugins.HandlerExitError{Code: 2, Stdout: []byte(`{"decline_code":"` + c.code + `","transaction_ref":"txn-SECRET"}`)})
		var before int
		_ = rig.dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE sale_type='return'`).Scan(&before)
		rec := rig.postRefundQty(t, rig.receiptNo(t, 1), 1)
		if rec.Code != http.StatusPaymentRequired {
			t.Fatalf("%s: code = %d: %s", c.code, rec.Code, rec.Body.String())
		}
		want := refundErrorText(t, c.key)
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: body %q lacks %q", c.code, rec.Body.String(), want)
		}
		if c.key != "refund.error.provider_declined" && strings.Contains(strings.ToLower(rec.Body.String()), "choose another method") {
			t.Errorf("%s: must not suggest another method: %s", c.code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "SECRET") {
			t.Errorf("%s: provider reference leaked: %s", c.code, rec.Body.String())
		}
		var after int
		_ = rig.dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE sale_type='return'`).Scan(&after)
		if after != before {
			t.Errorf("%s: a return was recorded", c.code)
		}
		var recs int
		_ = rig.dp.Db.QueryRow(`SELECT COUNT(*) FROM refund_leg_refunds`).Scan(&recs)
		if recs != 0 {
			t.Errorf("%s: leg records written for a declined refund", c.code)
		}
	}
}

// The legs of one tender share paid_at; their order must still be stable.
func TestSaleDetailPayments_StableOrderWhenPaidAtTies(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	if _, err := rig.dp.Db.Exec(`UPDATE payments SET paid_at = '2026-01-01 10:00:00'`); err != nil {
		t.Fatal(err)
	}
	// Make rowid order disagree with id order.
	if _, err := rig.dp.Db.Exec(`UPDATE payments SET id = 'zzz-' || id WHERE amount = 150`); err != nil {
		t.Fatal(err)
	}
	// An index that hands the rows back in (paid_at, id) order, the way a
	// real planner may: without ORDER BY ..., rowid the tie follows id.
	if _, err := rig.dp.Db.Exec(`CREATE INDEX tmp_pay_order ON payments (sale_id, paid_at, id)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		d, ok, err := data.NewPOSRepo(rig.dp.Db).GetSaleDetail(t.Context(), rig.receiptNo(t, 1))
		if err != nil || !ok {
			t.Fatal(err, ok)
		}
		if len(d.Payments) != 2 || d.Payments[0].Amount != 150 || d.Payments[1].Amount != 90 {
			t.Fatalf("legs out of insertion order: %+v", d.Payments)
		}
	}
}

// refundErrOverrides lets a test change what the fake payment plugin answers
// to payment.demopay.refund after the rig is built (keyed by the till DB).
var refundErrOverrides sync.Map

// refundHookGates: a test-set func called before the fake plugin answers a
// refund (keyed by the till DB), so it can block the provider call (#6742).
var refundHookGates sync.Map

func (r *refCardRig) setRefundError(err error) { refundErrOverrides.Store(r.dp.Db, err) }

func refundErrorText(t *testing.T, key string) string {
	t.Helper()
	txt := httpx.T("en", key)
	if txt == "" || txt == key {
		t.Fatalf("no English text for %s", key)
	}
	return txt
}

// Review finding: legs with less left than the refund needs must refuse the
// refund, never drop the per-leg list and let the plugin guess a charge.
func TestRefundLegNetting_ExhaustedLegsRefuseBeforeTheProvider(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
	if _, err := rig.dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rig.sell(t, `[{"method":"demopay","amount":150},{"method":"demopay","amount":90}]`)
	receipt := rig.receiptNo(t, 1)
	// Pretend the legs were refunded elsewhere down to 30 in total.
	if _, err := rig.dp.Db.Exec(`INSERT INTO refund_leg_refunds (id, original_sale_id, original_payment_id, return_sale_id, amount)
		SELECT 'r-' || id, sale_id, id, 'x', amount - CASE WHEN amount = 150 THEN 20 ELSE 10 END FROM payments`); err != nil {
		t.Fatal(err)
	}
	rec := rig.postRefundQty(t, receipt, 1)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 0 {
		t.Fatalf("provider was called %d times for a refund the legs cannot cover", n)
	}
}
