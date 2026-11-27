package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7104 (ADR-0136 D9.8): while one refund of a sale is open (asked,
// captured, unknown or warned) a different refund of that sale is refused
// before the TSE start and the provider. Sale "ABC" = 240; qty_0=1 refunds 120.

const unknownRefundBody = `{"decline_code":"refund_outcome_unknown"}`

func (r *refCardRig) postRefundForm(t *testing.T, receipt, attemptID string, qty int, leg, method string) *httptest.ResponseRecorder {
	t.Helper()
	f := url.Values{"receipt": {receipt}, "qty_0": {string(rune('0' + qty))}, "method": {method}}
	if leg != "" {
		f.Set("refund_leg", leg)
	}
	if attemptID != "" {
		f.Set("refund_attempt_id", attemptID)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func (r *refCardRig) setUnknown() {
	r.setRefundError(&plugins.HandlerExitError{Code: 2, Stdout: json.RawMessage(unknownRefundBody)})
}

// openRig sells one 240 sale over two demopay payments and leaves a refund of
// the first one unknown. It returns the receipt, the leg ids, the fiscal-start
// counter (reset) and the first refund's attempt id.
func openRig(t *testing.T) (rig *refCardRig, receipt string, ids []string, starts *atomic.Int32) {
	t.Helper()
	rig = newChoiceRig(t)
	starts = new(atomic.Int32)
	t.Cleanup(func() { plugins.SharedBus(rig.dp.Db).ResetSubscribers() })
	subscribeFiscalSignStartHandler(t, rig.dp, "com.test.fiscal-start-7104", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		if p["sale_type"] == "return" {
			starts.Add(1)
		}
		return json.RawMessage(`{"status":"acknowledged","tx_id":"tx-7104","tx_revision":1}`), nil
	})
	rig.sell(t, `[{"method":"demopay","amount":120},{"method":"demopay","amount":120}]`)
	receipt = rig.receiptNo(t, 1)
	ids = legPaymentIDs(t, rig, receipt)
	rig.dp.WaitForAsyncWork()
	rig.setUnknown()
	if rec := rig.postRefundForm(t, receipt, "att-A", 1, ids[0], "demopay"); rec.Code < 400 {
		t.Fatalf("first refund = %d, want a failure leaving it unknown", rec.Code)
	}
	if l := rig.refundLegs(t, data.LegUnknown); len(l) != 1 {
		t.Fatalf("unknown legs = %+v, want 1", l)
	}
	rig.dp.WaitForAsyncWork()
	starts.Store(0)
	return rig, receipt, ids, starts
}

func (r *refCardRig) journalRows(t *testing.T) int {
	return r.count(t, `SELECT COUNT(*) FROM payment_leg_journal`)
}

func assertOtherRefundRefused(t *testing.T, rig *refCardRig, rec *httptest.ResponseRecorder, starts *atomic.Int32, asks, rows int) {
	t.Helper()
	if rec.Code != http.StatusConflict || !strings.Contains(bodyText(rec), enText(t, "refund.error.other_refund_open")) {
		t.Fatalf("refund = %d %s, want 409 other_refund_open", rec.Code, rec.Body.String())
	}
	rig.dp.WaitForAsyncWork()
	if n := len(rig.refunds()); n != asks {
		t.Fatalf("provider hook calls = %d, want still %d", n, asks)
	}
	if n := rig.journalRows(t); n != rows {
		t.Fatalf("journal rows = %d, want %d (no write)", n, rows)
	}
	if n := returnCount(t, rig); n != 0 {
		t.Fatalf("return sales = %d, want 0", n)
	}
	if n := starts.Load(); n != 0 {
		t.Fatalf("fiscal.sign.start fired %d time(s), want 0", n)
	}
}

func TestRefund_OtherPaymentWhileUnknown_Refused_7104(t *testing.T) {
	rig, receipt, ids, starts := openRig(t)
	rows := rig.journalRows(t)
	rec := rig.postRefundForm(t, receipt, "att-B", 1, ids[1], "demopay")
	assertOtherRefundRefused(t, rig, rec, starts, 1, rows)
}

func TestRefund_ChangedQtyWhileUnknown_Refused_7104(t *testing.T) {
	rig, receipt, _, starts := openRig(t)
	rows := rig.journalRows(t)
	// Same attempt id, quantity 2 over all payments: another payload, another key.
	rec := rig.postRefundForm(t, receipt, "att-A", 2, "", "demopay")
	assertOtherRefundRefused(t, rig, rec, starts, 1, rows)
}

func TestRefund_OtherMethodCashWhileUnknown_Refused_7104(t *testing.T) {
	rig, receipt, _, starts := openRig(t)
	rows := rig.journalRows(t)
	rec := rig.postRefundForm(t, receipt, "att-C", 1, "", "cash")
	assertOtherRefundRefused(t, rig, rec, starts, 1, rows)
}

func TestRefund_SameRefundStillReasks_7104(t *testing.T) {
	rig, receipt, ids, starts := openRig(t)
	refundErrOverrides.Delete(rig.dp.Db)
	rec := rig.postRefundForm(t, receipt, "att-A", 1, ids[0], "demopay")
	if rec.Code >= 400 {
		t.Fatalf("same refund = %d %s, want it re-asked", rec.Code, rec.Body.String())
	}
	rig.dp.WaitForAsyncWork()
	if n := len(rig.refunds()); n != 2 {
		t.Fatalf("provider hook calls = %d, want 2", n)
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("fiscal.sign.start = %d, want 1", n)
	}
	if l := rig.refundLegs(t, data.LegSettled); len(l) != 1 || l[0].Ask != 2 {
		t.Fatalf("settled = %+v, want one with ask 2", l)
	}
}

func TestRefund_UnblockedAfterSettledResolvedDeclined_7104(t *testing.T) {
	for _, c := range []string{"settled", "resolved", "declined"} {
		t.Run(c, func(t *testing.T) {
			rig, receipt, ids, _ := openRig(t)
			repo := data.NewPaymentLegJournalRepo(rig.dp.Db)
			cur := rig.refundLegs(t, data.LegUnknown)[0]
			switch c {
			case "settled":
				refundErrOverrides.Delete(rig.dp.Db)
				if rec := rig.postRefundForm(t, receipt, "att-A", 1, ids[0], "demopay"); rec.Code >= 400 {
					t.Fatalf("settling refund = %d %s", rec.Code, rec.Body.String())
				}
			case "resolved":
				e := cur
				e.State, e.Reason = data.LegWarned, ""
				if err := repo.Append(context.Background(), e); err != nil {
					t.Fatal(err)
				}
				e.State = data.LegResolved
				if err := repo.Append(context.Background(), e); err != nil {
					t.Fatal(err)
				}
			case "declined":
				// A clean decline of a different key: seed it as the only leg.
				rig.exec(t, `DELETE FROM payment_leg_journal`)
				key := data.RefundLegKey(rig.saleID(t, 1), "demopay", "att-D:0123456789abcdef")
				for _, s := range []string{data.LegAsked, data.LegDeclined} {
					if err := repo.Append(context.Background(), data.LegEntry{RequestID: key, State: s, MethodID: "demopay", AmountMinor: 120, Currency: "GBP"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			refundErrOverrides.Delete(rig.dp.Db)
			before := len(rig.refunds())
			rec := rig.postRefundForm(t, receipt, "att-B", 1, ids[1], "demopay")
			if rec.Code >= 400 {
				t.Fatalf("refund B = %d %s, want it to go through", rec.Code, rec.Body.String())
			}
			if n := len(rig.refunds()); n != before+1 {
				t.Fatalf("provider hook calls = %d, want %d", n, before+1)
			}
		})
	}
}

func TestRefund_EachOpenStateBlocks_7104(t *testing.T) {
	cases := []struct {
		name   string
		chain  []string
		blocks bool
	}{
		{"asked", []string{data.LegAsked}, true},
		{"captured", []string{data.LegAsked, data.LegCaptured}, true},
		{"unknown", []string{data.LegAsked, data.LegUnknown}, true},
		{"warned", []string{data.LegAsked, data.LegUnknown, data.LegWarned}, true},
		{"declined", []string{data.LegAsked, data.LegDeclined}, false},
		{"settled", []string{data.LegAsked, data.LegCaptured, data.LegSettled}, false},
		{"resolved", []string{data.LegAsked, data.LegUnknown, data.LegWarned, data.LegResolved}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newChoiceRig(t)
			rig.sell(t, `[{"method":"demopay","amount":120},{"method":"demopay","amount":120}]`)
			receipt := rig.receiptNo(t, 1)
			ids := legPaymentIDs(t, rig, receipt)
			repo := data.NewPaymentLegJournalRepo(rig.dp.Db)
			key := data.RefundLegKey(rig.saleID(t, 1), "demopay", "att-S:0123456789abcdef")
			for _, s := range c.chain {
				if err := repo.Append(context.Background(), data.LegEntry{RequestID: key, State: s, MethodID: "demopay", AmountMinor: 120, Currency: "GBP"}); err != nil {
					t.Fatal(err)
				}
			}
			rec := rig.postRefundForm(t, receipt, "att-B", 1, ids[1], "demopay")
			if c.blocks {
				if rec.Code != http.StatusConflict || !strings.Contains(bodyText(rec), enText(t, "refund.error.other_refund_open")) {
					t.Fatalf("refund = %d %s, want 409 other_refund_open", rec.Code, rec.Body.String())
				}
				if n := len(rig.refunds()); n != 0 {
					t.Fatalf("provider asked %d time(s)", n)
				}
			} else if rec.Code >= 400 {
				t.Fatalf("refund = %d %s, want it allowed", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRefund_OtherSaleDoesNotBlock_7104(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":120},{"method":"demopay","amount":120}]`)
	receipt := rig.receiptNo(t, 1)
	ids := legPaymentIDs(t, rig, receipt)
	repo := data.NewPaymentLegJournalRepo(rig.dp.Db)
	key := data.RefundLegKey("some-other-sale", "demopay", "att-S:0123456789abcdef")
	for _, s := range []string{data.LegAsked, data.LegUnknown} {
		if err := repo.Append(context.Background(), data.LegEntry{RequestID: key, State: s, MethodID: "demopay", AmountMinor: 120, Currency: "GBP"}); err != nil {
			t.Fatal(err)
		}
	}
	if rec := rig.postRefundForm(t, receipt, "att-B", 1, ids[1], "demopay"); rec.Code >= 400 {
		t.Fatalf("refund = %d %s, want it allowed", rec.Code, rec.Body.String())
	}
}

func TestRefund_JournalReadFails_503_7104(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":120},{"method":"demopay","amount":120}]`)
	receipt := rig.receiptNo(t, 1)
	ids := legPaymentIDs(t, rig, receipt)
	rig.exec(t, `ALTER TABLE payment_leg_journal RENAME TO payment_leg_journal_gone`)
	rec := rig.postRefundForm(t, receipt, "att-B", 1, ids[1], "demopay")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(bodyText(rec), enText(t, "refund.error.journal_unavailable")) {
		t.Fatalf("refund = %d %s, want 503 journal_unavailable", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 0 {
		t.Fatalf("provider asked %d time(s)", n)
	}
}

// The renamed-table test above cannot tell the new open-leg read from the
// same-key Latest read. Here Latest works (the new key has no row) and only the
// open-leg scan meets a row it cannot read: it must fail closed with 503.
func TestRefund_OpenLegScanFails_503_7104(t *testing.T) {
	rig := newChoiceRig(t)
	rig.sell(t, `[{"method":"demopay","amount":120},{"method":"demopay","amount":120}]`)
	receipt := rig.receiptNo(t, 1)
	ids := legPaymentIDs(t, rig, receipt)
	var saleID string
	if err := rig.dp.Db.QueryRow(`SELECT id FROM sales WHERE receipt_no = ?`, receipt).Scan(&saleID); err != nil {
		t.Fatal(err)
	}
	// amount_minor has INTEGER affinity, so the text stays text and Scan into int64 fails.
	rig.exec(t, `INSERT INTO payment_leg_journal (request_id, state, method_id, amount_minor, currency, at)
VALUES ('refund:`+saleID+`:demopay:poison:0123456789abcdef', 'unknown', 'demopay', 'not-a-number', 'EUR', '2026-01-01T00:00:00Z')`) // fixed-date:ok never read against a clock
	rec := rig.postRefundForm(t, receipt, "att-B", 1, ids[1], "demopay")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(bodyText(rec), enText(t, "refund.error.journal_unavailable")) {
		t.Fatalf("refund = %d %s, want 503 journal_unavailable", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 0 {
		t.Fatalf("provider asked %d time(s)", n)
	}
}

var attemptIDField = regexp.MustCompile(`name="refund_attempt_id" value="([^"]*)"`)

func pageAttemptID(t *testing.T, page string) string {
	t.Helper()
	m := attemptIDField.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no refund_attempt_id field in page")
	}
	return m[1]
}

func TestRefundPage_ReopenReusesOpenAttemptID_7104(t *testing.T) {
	rig, receipt, ids, _ := openRig(t)
	if got := pageAttemptID(t, getRefundPage(t, rig, receipt)); got != "att-A" {
		t.Fatalf("reopened attempt id = %q, want att-A", got)
	}
	// Round trip: the reopened form's id, same selections -> same key, re-asked.
	refundErrOverrides.Delete(rig.dp.Db)
	id := pageAttemptID(t, getRefundPage(t, rig, receipt))
	if rec := rig.postRefundForm(t, receipt, id, 1, ids[0], "demopay"); rec.Code >= 400 {
		t.Fatalf("repeat refund = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(rig.refunds()); n != 2 {
		t.Fatalf("provider hook calls = %d, want 2", n)
	}
	// Nothing open any more: a fresh uuid per load.
	a, b := pageAttemptID(t, getRefundPage(t, rig, receipt)), pageAttemptID(t, getRefundPage(t, rig, receipt))
	if a == "att-A" || len(a) != 36 || a == b {
		t.Fatalf("settled sale ids = %q, %q, want fresh uuids", a, b)
	}
}
