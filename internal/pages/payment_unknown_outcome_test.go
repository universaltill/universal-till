package pages

import (
	"errors"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const unknownOutcomeToast = "may have been charged"

// ut-docs#6600: a plugin that exits non-zero with decline_code
// outcome_unknown gets its own toast, the basket is kept, and a Problem names
// the method and request id.
func TestTender_OutcomeUnknownShowsOwnToastAndKeepsBasket(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	_, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return "UNKNOWN" })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) {
		t.Fatalf("want the outcome-unknown toast, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Payment declined") {
		t.Fatalf("outcome unknown must not read as a plain decline: %s", rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("no sale row, got %d", n)
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatalf("basket must be kept")
	}
	ids := authIDs()
	found := false
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "demopay") && strings.Contains(p.Msg, ids[0]) {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a Problems item naming method and request id; recent: %+v", logging.Recent())
	}
}

// ut-docs#6600/#6601: until settled the basket stays on that tender: another
// tender is refused without asking any plugin, and the same tender retried
// carries the SAME authorize request id (so the provider dedupes) and settles.
func TestTender_OutcomeUnknownLocksTenderUntilSettled(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	_, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		if n == 1 {
			return "UNKNOWN"
		}
		return `{"provider":"demopay","outcome":"approved"}`
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)

	rec := postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) {
		t.Fatalf("switching tender must be refused with the same toast: %s", rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("a locked basket must not sell on another tender, got %d sales", n)
	}

	rec = postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("retrying the same tender must settle: %d sales, body %s", n, rec.Body.String())
	}
	ids := authIDs()
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("retry must reuse the authorize request id, got %v", ids)
	}
}

// ut-docs#6602: a later leg declining reverses the captured earlier leg.
func TestTender_LaterLegDeclineReversesCapturedLeg(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		if n == 2 {
			return "DECLINE"
		}
		return `{"provider":"demopay","outcome":"approved"}`
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":60},{"method":"demopay","amount":60}]}`)
	got := refunds()
	if len(got) != 1 {
		t.Fatalf("want one reversal for the captured first leg, got %+v", got)
	}
	if amt, _ := got[0]["amount"].(float64); int64(amt) != 60 || got[0]["reversal"] != true || got[0]["authorize_request_id"] != authIDs()[0] {
		t.Fatalf("bad reversal payload: %+v", got[0])
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("no sale, got %d", n)
	}
}

// An unknown outcome on a later leg must NOT reverse the earlier leg: the
// retry re-asks with the same ids and settles both.
func TestTender_LaterLegUnknownOutcomeKeepsEarlierLeg(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		if n == 2 {
			return "UNKNOWN"
		}
		return `{"provider":"demopay","outcome":"approved"}`
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":60},{"method":"demopay","amount":60}]}`)
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) {
		t.Fatalf("want outcome-unknown toast: %s", rec.Body.String())
	}
	if n := len(refunds()); n != 0 {
		t.Fatalf("earlier leg must stay captured, got %d reversals", n)
	}
}

// failCompleteSale makes pos.CompleteSale fail inside its transaction (after
// the authorize loop) by aborting every payment-row insert.
func failCompleteSale(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Db.Exec(`CREATE TRIGGER fail_payments BEFORE INSERT ON payments BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
}

// ut-docs#6602: CompleteSale failing after authorize reverses the capture.
func TestTender_CompleteSaleFailureReversesCapturedLeg(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := seedCannotSignCardPlugin(t, dp, nil)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	failCompleteSale(t, dp)
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("setup: CompleteSale should have failed, got %d sales", n)
	}
	got := refunds()
	if len(got) != 1 || got[0]["reversal"] != true || got[0]["authorize_request_id"] != authIDs()[0] {
		t.Fatalf("want one reversal of the captured leg, got %+v", got)
	}
}

// ut-docs#6602: a reversal that fails raises a Problem with the reference.
func TestTender_CompleteSaleFailureReversalFailureRaisesProblem(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	_, authIDs := seedCannotSignCardPlugin(t, dp, errors.New("terminal offline"))
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	failCompleteSale(t, dp)
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	ids := authIDs()
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "demopay") && strings.Contains(p.Msg, ids[0]) && strings.Contains(p.Msg, "reversing captured payment FAILED") {
			return
		}
	}
	t.Fatalf("want a Problem naming method and authorize_request_id; recent: %+v", logging.Recent())
}

// A retry that fails for a reason that is not a plugin decline (timeout,
// plugin not loaded, DB error) says nothing about the money: the lock stays,
// other tenders stay refused, and the same request id is kept.
func TestTender_OutcomeUnknownRetryTimeoutKeepsLock(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	_, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		if n == 1 {
			return "UNKNOWN"
		}
		return "TIMEOUT"
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) {
		t.Fatalf("a timed-out retry must still say the outcome is unknown: %s", rec.Body.String())
	}
	rec = postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) || countRows(t, dp, "SELECT COUNT(*) FROM sales") != 0 {
		t.Fatalf("still locked: another tender must be refused: %s", rec.Body.String())
	}
	ids := authIDs()
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("request id must not change while locked: %v", ids)
	}
}

// A real plugin decline of the retry settles the question: the lock clears.
func TestTender_OutcomeUnknownRetryRealDeclineClearsLock(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		if n == 1 {
			return "UNKNOWN"
		}
		return "REALDECLINE"
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if dp.Engine.TenderLock() != "" {
		t.Fatal("a definite decline must clear the lock")
	}
}

func TestMaskTxnRef(t *testing.T) {
	for in, want := range map[string]string{"": `""`, "abc": "…", "pi_3Abcdef1234": "…1234"} {
		if got := maskTxnRef(in); got != want {
			t.Errorf("maskTxnRef(%q) = %q, want %q", in, got, want)
		}
	}
}
