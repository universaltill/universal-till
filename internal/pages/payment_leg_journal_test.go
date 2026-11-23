package pages

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6653 (ADR-0136 Decision 9): the durable payment-leg journal around
// the tender pipeline and its boot reconciliation.

const journalCardTender = `{"payments":[{"method":"demopay","amount":120}]}`
const journalApproved = `{"provider":"demopay","outcome":"approved","transaction_ref":"ch_FULLREF9876"}`

func legRepo(dp *common.Deps) *data.PaymentLegJournalRepo {
	return data.NewPaymentLegJournalRepo(dp.Db)
}

func seedLeg(t *testing.T, dp *common.Deps, id string, states ...string) {
	t.Helper()
	for _, s := range states {
		e := data.LegEntry{RequestID: id, State: s, MethodID: "demopay", AmountMinor: 135, Currency: "EUR", TipMinor: 15, ReaderRef: "rdr_1", TransactionRef: "ch_FULLREF9876"}
		if err := legRepo(dp).Append(context.Background(), e); err != nil {
			t.Fatalf("seed %s %s: %v", id, s, err)
		}
	}
}

func TestLegJournal_CommittedCardSaleIsAskedCapturedSettledWithSaleID(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	_, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return journalApproved })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, journalCardTender)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1", n)
	}
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatal(err)
	}
	ids := authIDs()
	if len(ids) != 1 {
		t.Fatalf("authorize calls = %d, want 1", len(ids))
	}
	got, ok, err := legRepo(dp).Latest(context.Background(), ids[0])
	if err != nil || !ok || got.State != data.LegSettled || got.SaleID != saleID || got.AmountMinor != 120 || got.TransactionRef != "ch_FULLREF9876" {
		t.Fatalf("Latest = %+v %v %v, want settled with sale %s, 120, full ref", got, ok, err, saleID)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM payment_leg_journal WHERE state IN ('asked','captured','settled')"); n != 3 {
		t.Fatalf("journal rows asked+captured+settled = %d, want 3", n)
	}
}

func TestLegJournal_CashSaleWritesNothing(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1", n)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM payment_leg_journal"); n != 0 {
		t.Fatalf("journal rows = %d, want 0 for a cash sale", n)
	}
}

func TestLegJournal_InsertFailureRefusesTenderAndNeverAsksThePlugin(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	_, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return journalApproved })
	if _, err := dp.Db.Exec(`DROP TABLE payment_leg_journal`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, journalCardTender)
	if got := authIDs(); len(got) != 0 {
		t.Fatalf("authorize stub called %d times, want 0: a leg that cannot be recorded is never asked", len(got))
	}
	if !strings.Contains(rec.Body.String(), "could not record the card payment safely") {
		t.Fatalf("want the journal-unavailable toast, got: %s", rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if len(dp.Engine.Basket().Lines) != 1 || dp.Engine.TenderLock() != "" {
		t.Fatalf("basket must be kept (1 line) and unlocked, lock=%q", dp.Engine.TenderLock())
	}
}

func TestLegReconcile_CapturedRowIsReversedExactlyOnceWithFullRefAndTip(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return journalApproved })
	seedLeg(t, dp, "att:0:abc", data.LegAsked, data.LegCaptured)
	cutoff := time.Now().Add(time.Minute)

	res := ReconcilePaymentLegs(context.Background(), dp, cutoff)
	if res.Reversed != 1 {
		t.Fatalf("result = %+v, want 1 reversed", res)
	}
	got := refunds()
	if len(got) != 1 {
		t.Fatalf("refund calls = %d, want exactly 1: %+v", len(got), got)
	}
	r := got[0]
	op, _ := r["original_payments"].([]any)
	if amt, _ := r["amount"].(float64); int64(amt) != 135 || r["reversal"] != true || r["authorize_request_id"] != "att:0:abc" || r["reader_id"] != "rdr_1" || len(op) != 1 ||
		op[0].(map[string]any)["transaction_ref"] != "ch_FULLREF9876" || op[0].(map[string]any)["tip_amount"] != float64(15) {
		t.Fatalf("reversal payload = %+v, want 135 reversal of att:0:abc, reader rdr_1, full ref, tip 15", r)
	}
	if last, _, _ := legRepo(dp).Latest(context.Background(), "att:0:abc"); last.State != data.LegReversed {
		t.Fatalf("journal state = %s, want reversed", last.State)
	}
	// second boot: nothing left to do
	before := countRows(t, dp, "SELECT COUNT(*) FROM payment_leg_journal")
	if res := ReconcilePaymentLegs(context.Background(), dp, cutoff); res != (LegReconcileResult{}) {
		t.Fatalf("second boot result = %+v, want nothing", res)
	}
	if after := countRows(t, dp, "SELECT COUNT(*) FROM payment_leg_journal"); after != before || len(refunds()) != 1 {
		t.Fatalf("second boot appended %d rows and sent %d refunds, want 0 and 1 total", after-before, len(refunds()))
	}
}

func TestLegReconcile_UnknownAskedAndReversingAreWarnedOnceNeverRefunded(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return journalApproved })
	seedLeg(t, dp, "u", data.LegAsked, data.LegUnknown)
	seedLeg(t, dp, "a", data.LegAsked)
	seedLeg(t, dp, "r", data.LegAsked, data.LegCaptured, data.LegReversing)
	cutoff := time.Now().Add(time.Minute)

	res := ReconcilePaymentLegs(context.Background(), dp, cutoff)
	if res.Warned != 3 || res.Reversed != 0 {
		t.Fatalf("result = %+v, want 3 warned, 0 reversed", res)
	}
	for _, id := range []string{"u", "a", "r"} {
		got, _, _ := legRepo(dp).Latest(context.Background(), id)
		if got.State != data.LegWarned || got.Reason != data.LegReasonRestart {
			t.Fatalf("leg %s = %s/%q, want warned/till_restarted", id, got.State, got.Reason)
		}
	}
	if got := refunds(); len(got) != 0 {
		t.Fatalf("provider got %d refund calls, want 0: unknown, asked and reversing legs are never refunded or re-sent", len(got))
	}
	if res := ReconcilePaymentLegs(context.Background(), dp, cutoff); res != (LegReconcileResult{}) {
		t.Fatalf("second boot result = %+v, want nothing (each warned exactly once)", res)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM payment_leg_journal WHERE state = 'warned'"); n != 3 {
		t.Fatalf("warned rows = %d, want 3", n)
	}
}

func TestLegReconcile_CommittedSaleIsNotReversed(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return journalApproved })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, journalCardTender)
	if res := ReconcilePaymentLegs(context.Background(), dp, time.Now().Add(time.Minute)); res != (LegReconcileResult{}) {
		t.Fatalf("result = %+v, want nothing for a leg settled by a committed sale", res)
	}
	if got := refunds(); len(got) != 0 {
		t.Fatalf("a committed sale must not be reversed at boot: %+v", got)
	}
}

func TestLegReconcile_SkipsRowsFromTheCurrentProcess(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return journalApproved })
	started := time.Now().Add(-time.Minute) // this process started before the rows below were written
	seedLeg(t, dp, "live", data.LegAsked, data.LegCaptured)
	seedLeg(t, dp, "liveask", data.LegAsked)
	if res := ReconcilePaymentLegs(context.Background(), dp, started); res != (LegReconcileResult{}) {
		t.Fatalf("result = %+v, want nothing: rows written after process start belong to a live tender", res)
	}
	if got := refunds(); len(got) != 0 {
		t.Fatalf("a live tender's leg was reversed: %+v", got)
	}
	if got, _, _ := legRepo(dp).Latest(context.Background(), "live"); got.State != data.LegCaptured {
		t.Fatalf("live leg = %s, want captured untouched", got.State)
	}
}

func TestLegReconcile_ReversalErrorBecomesReverseFailedAndIsNotRetriedAtNextBoot(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, context.DeadlineExceeded, func(int) string { return journalApproved })
	seedLeg(t, dp, "f", data.LegAsked, data.LegCaptured)
	cutoff := time.Now().Add(time.Minute)
	if res := ReconcilePaymentLegs(context.Background(), dp, cutoff); res.ReverseFailed != 1 {
		t.Fatalf("result = %+v, want 1 reverse_failed", res)
	}
	if got, _, _ := legRepo(dp).Latest(context.Background(), "f"); got.State != data.LegReverseFailed {
		t.Fatalf("state = %s, want reverse_failed", got.State)
	}
	ReconcilePaymentLegs(context.Background(), dp, cutoff)
	if len(refunds()) != 1 {
		t.Fatalf("refund calls = %d, want 1 (Retry is a manager action, slice 3)", len(refunds()))
	}
}

func TestLegReconcile_HangingReversalDoesNotStopACashSale(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return journalApproved })
	seedLeg(t, dp, "hang", data.LegAsked, data.LegCaptured)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	refundHookGates.Store(dp.Db, func() {
		once.Do(func() { close(entered) })
		<-release
	})
	t.Cleanup(func() { refundHookGates.Delete(dp.Db) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		ReconcilePaymentLegs(context.Background(), dp, time.Now().Add(time.Minute))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the reversal never reached the provider stub")
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	saleDone := make(chan struct{})
	go func() {
		defer close(saleDone)
		postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	}()
	select {
	case <-saleDone:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a cash sale was blocked by a hanging boot reversal")
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		close(release)
		t.Fatalf("sales = %d, want 1", n)
	}
	close(release)
	<-done
}
