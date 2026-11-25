package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7021 (ADR-0044 D1 amendment): a fiscal.sign.ask that was signed but
// whose CompleteSale then failed leaves a durable "signed, no sale" record.

const signedAnswer = `{"status":"approved","tse":{"transaction_number":77,"signature_counter":1234,"serial_number":"TSE-1","signature":"c2lnbmF0dXJl"}}`

func TestTender_SignedThenCompleteSaleFails_RecordsSignedNoSale(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return approvedAnswer })
	if _, err := dp.Db.Exec(`ALTER TABLE payments RENAME COLUMN voucher_id TO voucher_id_disabled`); err != nil {
		t.Fatalf("rename payments.voucher_id: %v", err)
	}
	var saleID string
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7021", func(_ context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p fiscalSignAskPayload
		_ = json.Unmarshal(ev.Payload, &p)
		saleID = p.SaleID
		return json.RawMessage(signedAnswer), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if !strings.Contains(rec.Body.String(), "could not be completed") {
		t.Fatalf("want the localized tender-failed toast, got %d: %s", rec.Code, rec.Body.String())
	}
	if saleID == "" {
		t.Fatal("signer never asked")
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if got, ids := refunds(), authIDs(); len(got) != 1 || len(ids) != 1 || got[0]["reversal"] != true {
		t.Fatalf("want exactly one reversal, got %+v", got)
	}
	e, ok, err := data.NewFiscalSignJournalRepo(dp.Db).Get(context.Background(), saleID)
	if err != nil || !ok {
		t.Fatalf("journal row for %s: ok=%v err=%v", saleID, ok, err)
	}
	if e.State != data.FiscalSignSignedNoSale || e.Outcome != "approved" || e.SaleType != "sale" || e.AmountMinor <= 0 || e.Currency == "" {
		t.Fatalf("row = %+v, want signed_no_sale/approved/sale with an amount", e)
	}
	if !strings.Contains(e.EvidenceJSON, "c2lnbmF0dXJl") {
		t.Fatalf("evidence_json = %q, want the signature kept", e.EvidenceJSON)
	}
	if strings.Contains(e.Reason, "voucher_id") || strings.Contains(e.Reason, "no such column") {
		t.Fatalf("reason leaks raw SQL: %q", e.Reason)
	}
	if n := countAuditRowsFor(t, dp, saleID, "fiscal_signed_no_sale"); n != 1 {
		t.Fatalf("fiscal_signed_no_sale audit rows = %d, want 1", n)
	}
	if !logging.HasOpenProblem(fiscalSignedNoSaleProblemPrefix + saleID) {
		t.Fatal("expected an open Problems item for the signed, unsaved sale")
	}
}

func TestRefund_SignedThenCompleteSaleFails_RecordsSignedNoSale(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	logging.ResetRecent()
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	var saleID string
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7021-refund", func(_ context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p fiscalSignAskPayload
		_ = json.Unmarshal(ev.Payload, &p)
		saleID = p.SaleID
		return json.RawMessage(signedAnswer), nil
	})
	_, receiptNo := seedCompletedSaleForRefund(t, dp)
	if _, err := dp.Db.Exec(`DROP TABLE stock_movements`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if saleID == "" {
		t.Fatal("signer never asked")
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales WHERE sale_type='return'"); n != 0 {
		t.Fatalf("return rows = %d, want 0", n)
	}
	e, ok, err := data.NewFiscalSignJournalRepo(dp.Db).Get(context.Background(), saleID)
	if err != nil || !ok || e.State != data.FiscalSignSignedNoSale || e.SaleType != "return" || !strings.Contains(e.EvidenceJSON, "c2lnbmF0dXJl") {
		t.Fatalf("row = %+v ok=%v err=%v, want signed_no_sale/return with evidence", e, ok, err)
	}
	if n := countAuditRowsFor(t, dp, saleID, "fiscal_signed_no_sale"); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	if !logging.HasOpenProblem(fiscalSignedNoSaleProblemPrefix + saleID) {
		t.Fatal("expected an open Problems item")
	}
}

func TestTender_FiscalSignJournalUnavailable_RefusesWithNothingAsked(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return approvedAnswer })
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7021-nojournal", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(signedAnswer), nil
	})
	if _, err := dp.Db.Exec(`DROP TABLE fiscal_sign_journal`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if !strings.Contains(rec.Body.String(), "could not be completed") {
		t.Fatalf("want the localized tender-failed toast, got %d: %s", rec.Code, rec.Body.String())
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 1 {
		t.Fatalf("basket must be kept, got %d lines", len(lines))
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("signer asked %d times, want 0", n)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if got := refunds(); len(got) != 1 || got[0]["reversal"] != true {
		t.Fatalf("want the captured leg reversed once, got %+v", got)
	}
}

func TestRefund_FiscalSignJournalUnavailable_CashRefundRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newRefundTestDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7021-nojournal-refund", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(signedAnswer), nil
	})
	_, receiptNo := seedCompletedSaleForRefund(t, dp)
	if _, err := dp.Db.Exec(`DROP TABLE fiscal_sign_journal`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Fatalf("refund must be refused, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("signer asked %d times, want 0", n)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales WHERE sale_type='return'"); n != 0 {
		t.Fatalf("return rows = %d, want 0", n)
	}
}

func TestTender_SignedAndCommitted_LeavesNoJournalRow(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	var saleID string
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7021-ok", func(_ context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p fiscalSignAskPayload
		_ = json.Unmarshal(ev.Payload, &p)
		saleID = p.SaleID
		return json.RawMessage(signedAnswer), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 || saleID == "" {
		t.Fatalf("sales = %d saleID=%q, want 1 committed", n, saleID)
	}
	if _, ok, _ := data.NewFiscalSignJournalRepo(dp.Db).Get(context.Background(), saleID); ok {
		t.Fatal("a committed sale must leave no journal row")
	}
	if logging.HasOpenProblem(fiscalSignedNoSaleProblemPrefix + saleID) {
		t.Fatal("no problem expected for a committed sale")
	}
}

func TestTender_CannotSign_LeavesNoSignedNoSaleRow(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	subscribeCannotSignSigner(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM fiscal_sign_journal WHERE state='signed_no_sale'"); n != 0 {
		t.Fatalf("signed_no_sale rows = %d, want 0 for a cannot-sign refusal", n)
	}
	if n := countAuditRows(t, dp, "fiscal_signed_no_sale"); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
}
