package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7041 (ADR-0136 Decision 9.8): a provider refund is a journaled leg,
// so a return that fails to record after the provider refunded is a durable,
// listed item, not a log line.

const failReturnInsert = `CREATE TRIGGER fail_return BEFORE INSERT ON sales WHEN NEW.sale_type = 'return' BEGIN SELECT RAISE(ABORT, 'forced'); END`

func (r *refCardRig) sellOne(t *testing.T) string {
	t.Helper()
	r.sell(t, `[{"method":"demopay","amount":120}]`)
	return r.receiptNo(t, 1)
}

func (r *refCardRig) postRefund(t *testing.T, receipt, attemptID string) *httptest.ResponseRecorder {
	t.Helper()
	f := url.Values{"receipt": {receipt}, "qty_0": {"1"}, "method": {"demopay"}}
	if attemptID != "" {
		f.Set("refund_attempt_id", attemptID)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func (r *refCardRig) exec(t *testing.T, q string) {
	t.Helper()
	if _, err := r.dp.Db.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (r *refCardRig) refundLegs(t *testing.T, states ...string) []data.LegEntry {
	t.Helper()
	all, err := data.NewPaymentLegJournalRepo(r.dp.Db).ListCurrent(context.Background(), states, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []data.LegEntry
	for _, e := range all {
		if strings.HasPrefix(e.RequestID, data.LegRefundKeyPrefix) {
			out = append(out, e)
		}
	}
	return out
}

func TestRefund_ProviderRefundedSaleFails_WarnedRowDurable(t *testing.T) {
	rig := newRefCardRig(t, func(n int) string { return `"pi_1"` }, "")
	receipt := rig.sellOne(t)
	refundRespOverrides.Store(rig.dp.Db, `{"outcome":"refunded","transaction_ref":"re_123456789"}`)
	t.Cleanup(func() { refundRespOverrides.Delete(rig.dp.Db) })
	rig.exec(t, failReturnInsert)

	rec := rig.postRefund(t, receipt, "att-1")

	if n := len(rig.refunds()); n != 1 {
		t.Fatalf("provider refund hook calls = %d, want 1", n)
	}
	if n := countRows(t, rig.dp, `SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`); n != 0 {
		t.Fatalf("return sales = %d, want 0", n)
	}
	if rec.Code < 400 || !strings.Contains(bodyText(rec), enText(t, "refund.error.refunded_not_recorded")) {
		t.Fatalf("response lacks refund.error.refunded_not_recorded: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pos.toast.") {
		t.Fatalf("response carries a generic tender toast: %s", rec.Body.String())
	}
	warned := rig.refundLegs(t, data.LegWarned)
	if len(warned) != 1 {
		t.Fatalf("warned refund legs = %d, want 1 (%+v)", len(warned), warned)
	}
	w := warned[0]
	var origID string
	_ = rig.dp.Db.QueryRow(`SELECT id FROM sales WHERE receipt_no = ?`, receipt).Scan(&origID)
	if id, ok := data.RefundLegOriginalSale(w.RequestID); !ok || id != origID {
		t.Fatalf("leg key %q does not carry the original sale %q", w.RequestID, origID)
	}
	if w.Reason != data.LegReasonRefundNoReturn || w.AmountMinor != 120 || w.Currency != "GBP" || w.MethodID != "demopay" || w.TransactionRef != "re_123456789" {
		t.Fatalf("leg = %+v", w)
	}

	// Durable: reopen the file and boot-reconcile.
	var path string
	var seq int
	var name string
	if err := rig.dp.Db.QueryRow(`SELECT seq, name, file FROM pragma_database_list WHERE name = 'main'`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := rig.dp.Db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.DB.Close() })
	d2 := &common.Deps{Db: reopened.DB}
	dtos, err := legAttentionDTOs(context.Background(), d2)
	if err != nil || len(dtos) != 1 {
		t.Fatalf("legAttentionDTOs = %+v, %v; want the warned refund leg", dtos, err)
	}
	if dtos[0].OriginalSaleID != origID || dtos[0].Reason != data.LegReasonRefundNoReturn || !strings.HasSuffix(dtos[0].TransactionRef, "6789") {
		t.Fatalf("dto = %+v", dtos[0])
	}
	before := countRows2(t, reopened.DB, `SELECT COUNT(*) FROM payment_leg_journal`)
	ReconcilePaymentLegs(context.Background(), d2, time.Now().Add(time.Minute))
	if after := countRows2(t, reopened.DB, `SELECT COUNT(*) FROM payment_leg_journal`); after != before {
		t.Fatalf("boot changed a warned refund leg: %d -> %d rows", before, after)
	}
}

func countRows2(t *testing.T, d *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRefund_ProviderRefundOK_LegSettled(t *testing.T) {
	rig := newRefCardRig(t, func(n int) string { return `"pi_1"` }, "")
	receipt := rig.sellOne(t)
	rec := rig.postRefund(t, receipt, "att-ok")
	if rec.Code >= 400 {
		t.Fatalf("refund = %d %s", rec.Code, rec.Body.String())
	}
	var retID string
	if err := rig.dp.Db.QueryRow(`SELECT id FROM sales WHERE sale_type = 'return'`).Scan(&retID); err != nil {
		t.Fatalf("no return sale: %v", err)
	}
	settled := rig.refundLegs(t, data.LegSettled)
	if len(settled) != 1 || settled[0].SaleID != retID {
		t.Fatalf("settled refund legs = %+v, want one with sale %s", settled, retID)
	}
	if dtos, _ := legAttentionDTOs(context.Background(), rig.dp); len(dtos) != 0 {
		t.Fatalf("attention list = %+v, want empty", dtos)
	}
}

func TestRefund_CashSaleFails_NoJournalRow(t *testing.T) {
	rig := newRefCardRig(t, func(n int) string { return `"pi_1"` }, "")
	rig.sell(t, `[{"method":"cash","amount":120}]`)
	receipt := rig.receiptNo(t, 1)
	rig.exec(t, failReturnInsert)
	f := url.Values{"receipt": {receipt}, "qty_0": {"1"}, "method": {"cash"}, "refund_attempt_id": {"c1"}}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Fatalf("refund = %d, want a failure", rec.Code)
	}
	if strings.Contains(bodyText(rec), enText(t, "refund.error.refunded_not_recorded")) {
		t.Fatal("a cash refund must not claim money was refunded")
	}
	if n := countRows(t, rig.dp, `SELECT COUNT(*) FROM payment_leg_journal WHERE request_id LIKE 'refund:%'`); n != 0 {
		t.Fatalf("refund journal rows = %d, want 0", n)
	}
}

func TestRefund_JournalWriteFails_ProviderNotAsked(t *testing.T) {
	rig := newRefCardRig(t, func(n int) string { return `"pi_1"` }, "")
	receipt := rig.sellOne(t)
	rig.exec(t, `CREATE TRIGGER fail_journal BEFORE INSERT ON payment_leg_journal BEGIN SELECT RAISE(ABORT, 'forced'); END`)
	rec := rig.postRefund(t, receipt, "att-j")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(bodyText(rec), enText(t, "refund.error.journal_unavailable")) {
		t.Fatalf("response = %d %s, want 503 journal_unavailable", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 0 {
		t.Fatalf("provider hook calls = %d, want 0", n)
	}
	if n := countRows(t, rig.dp, `SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`); n != 0 {
		t.Fatalf("return sales = %d, want 0", n)
	}
}

func TestRefund_RetryAfterWarned_RefusedBeforeProvider(t *testing.T) {
	rig := newRefCardRig(t, func(n int) string { return `"pi_1"` }, "")
	receipt := rig.sellOne(t)
	rig.exec(t, failReturnInsert)
	rig.postRefund(t, receipt, "att-r")
	rig.exec(t, `DROP TRIGGER fail_return`)

	rec := rig.postRefund(t, receipt, "att-r")
	if rec.Code != http.StatusConflict || !strings.Contains(bodyText(rec), enText(t, "refund.error.refunded_not_recorded")) {
		t.Fatalf("retry = %d %s, want 409 refunded_not_recorded", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 1 {
		t.Fatalf("provider hook calls = %d, want still 1", n)
	}
	if n := countRows(t, rig.dp, `SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`); n != 0 {
		t.Fatalf("return sales = %d, want 0", n)
	}
}

func TestRefund_OutcomeUnknown_RetryReasks(t *testing.T) {
	rig := newRefCardRig(t, func(n int) string { return `"pi_1"` }, "")
	receipt := rig.sellOne(t)
	rig.setRefundError(&plugins.HandlerExitError{Code: 2, Stdout: json.RawMessage(`{"decline_code":"refund_outcome_unknown"}`)})
	rec := rig.postRefund(t, receipt, "att-u")
	if rec.Code < 400 {
		t.Fatalf("first refund = %d, want a failure", rec.Code)
	}
	if l := rig.refundLegs(t, data.LegUnknown); len(l) != 1 {
		t.Fatalf("unknown refund legs = %+v, want 1", l)
	}
	refundErrOverrides.Delete(rig.dp.Db)
	rec = rig.postRefund(t, receipt, "att-u")
	if rec.Code >= 400 {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 2 {
		t.Fatalf("provider hook calls = %d, want 2 (the retry re-asks)", n)
	}
	settled := rig.refundLegs(t, data.LegSettled)
	if len(settled) != 1 || settled[0].Ask != 2 {
		t.Fatalf("settled = %+v, want one with ask 2", settled)
	}
}

func TestReconcile_CapturedRefundLegWarnedNeverReversed(t *testing.T) {
	rig := newRefCardRig(t, func(n int) string { return `"pi_1"` }, "")
	key := data.RefundLegKey("sale-x", "demopay", "att-b:abcd")
	repo := data.NewPaymentLegJournalRepo(rig.dp.Db)
	for _, s := range []string{data.LegAsked, data.LegCaptured} {
		if err := repo.Append(context.Background(), data.LegEntry{RequestID: key, State: s, MethodID: "demopay", AmountMinor: 120, Currency: "EUR", TransactionRef: "re_123456789"}); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().Add(time.Minute)
	res := ReconcilePaymentLegs(context.Background(), rig.dp, cutoff)
	if res.RefundNoReturn != 1 || res.Reversed != 0 || res.ReverseFailed != 0 {
		t.Fatalf("result = %+v, want 1 refund_no_return", res)
	}
	got, _, _ := repo.Latest(context.Background(), key)
	if got.State != data.LegWarned || got.Reason != data.LegReasonRefundNoReturn {
		t.Fatalf("leg = %s/%q", got.State, got.Reason)
	}
	if n := len(rig.refunds()); n != 0 {
		t.Fatalf("a reversal/refund was dispatched: %d", n)
	}
	if res := ReconcilePaymentLegs(context.Background(), rig.dp, cutoff); res != (LegReconcileResult{}) {
		t.Fatalf("second boot = %+v, want zero", res)
	}
}

// Review (ut-docs#7041, Tester O1): the warned write failed after the provider
// refunded, so the leg is still captured. A re-submit of the same refund must
// not reach the provider again (AskLeg lets a captured key through).
func TestRefund_RetryWhileCaptured_RefusedBeforeProvider(t *testing.T) {
	rig := newRefCardRig(t, func(n int) string { return `"pi_1"` }, "")
	receipt := rig.sellOne(t)
	rig.exec(t, failReturnInsert)
	rig.exec(t, `CREATE TRIGGER fail_warn BEFORE INSERT ON payment_leg_journal WHEN NEW.state = 'warned' BEGIN SELECT RAISE(ABORT, 'forced'); END`)
	rig.postRefund(t, receipt, "att-c")
	if l := rig.refundLegs(t, data.LegCaptured); len(l) != 1 {
		t.Fatalf("captured refund legs = %+v, want 1", l)
	}
	rig.exec(t, `DROP TRIGGER fail_return`)
	rig.exec(t, `DROP TRIGGER fail_warn`)

	rec := rig.postRefund(t, receipt, "att-c")
	if rec.Code != http.StatusConflict || !strings.Contains(bodyText(rec), enText(t, "refund.error.already_sent")) {
		t.Fatalf("retry = %d %s, want 409 already_sent", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 1 {
		t.Fatalf("provider hook calls = %d, want still 1", n)
	}
	if n := countRows(t, rig.dp, `SELECT COUNT(*) FROM sales WHERE sale_type = 'return'`); n != 0 {
		t.Fatalf("return sales = %d, want 0", n)
	}
}
