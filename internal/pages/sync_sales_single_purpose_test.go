package pages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// ADR-0105 (ut-docs#1037) on the LAN-sync journal: a single-purpose voucher
// issued on a replica must land on the primary with the SAME type and rate
// the replica stamped — never re-resolved from the primary's own current
// setting — and the primary's totals must re-derive identically (taxed at
// issue, not a liability).
func TestApplyJournal_SinglePurposeIssueKeepsStampedType(t *testing.T) {
	_, replicaDp := newSyncSalesTestDeps(t)
	_, primaryDp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	replicaRepo := data.NewPOSRepo(replicaDp.Db)

	// The replica sells single-purpose at 7%; the primary is (still) on
	// multi-purpose — a mid-rollout mismatch the replay must not "correct".
	settings := data.NewSettingsRepo(replicaDp.Db)
	if err := settings.Set(ctx, pos.SettingKeyVoucherDefaultType, data.VoucherTypeSinglePurpose); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, pos.SettingKeyVoucherSinglePurposeTaxRateBP, "700"); err != nil {
		t.Fatal(err)
	}
	if err := data.NewSettingsRepo(primaryDp.Db).Set(ctx, pos.SettingKeyVoucherDefaultType, data.VoucherTypeMultiPurpose); err != nil {
		t.Fatal(err)
	}

	if _, err := pos.CompleteSale(ctx, replicaDp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: "lan-sp-issue-1", ReceiptNo: "T2-SP001",
		Currency: "GBP", TaxInclusive: true, CashierID: "user1", // the sync fixture's shop currency
		VoucherIssues: []pos.VoucherIssueInput{{VoucherID: "SP-LAN-A", Amount: money.FromMinor(1070)}},
		Payments:      []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1070)}},
	}); err != nil {
		t.Fatalf("replica issue sale: %v", err)
	}

	j, found, err := buildJournal(ctx, replicaRepo, "T2-SP001")
	if err != nil || !found {
		t.Fatalf("buildJournal: found=%v err=%v", found, err)
	}
	if len(j.Sale.VoucherIssues) != 1 || j.Sale.VoucherIssues[0].VoucherType != data.VoucherTypeSinglePurpose || j.Sale.VoucherIssues[0].TaxRateBP != 700 {
		t.Fatalf("journal voucher issues = %+v, want single_purpose @ 700 bp on the wire", j.Sale.VoucherIssues)
	}

	applied, _, err := applyJournal(ctx, primaryDp, "till-2", j)
	if err != nil || !applied {
		t.Fatalf("applyJournal: applied=%v err=%v", applied, err)
	}

	var vtype string
	var rate sql.NullInt64
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT voucher_type, tax_rate_bp FROM vouchers WHERE id = 'SP-LAN-A'`).Scan(&vtype, &rate); err != nil {
		t.Fatalf("primary voucher SP-LAN-A missing: %v", err)
	}
	if vtype != data.VoucherTypeSinglePurpose || !rate.Valid || rate.Int64 != 700 {
		t.Fatalf("SP-LAN-A on primary = %s/%+v, want single_purpose/700 as the replica stamped (primary's own setting is multi_purpose)", vtype, rate)
	}
	var subtotal, taxTotal, total, voucherIssueTotal int64
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT subtotal, tax_total, total, voucher_issue_total FROM sales WHERE id = 'lan-sp-issue-1'`).
		Scan(&subtotal, &taxTotal, &total, &voucherIssueTotal); err != nil {
		t.Fatal(err)
	}
	// 1070 @7% inclusive: tax 70.
	if subtotal != 1070 || taxTotal != 70 || total != 1070 || voucherIssueTotal != 0 {
		t.Fatalf("replayed header = %d/%d/%d/%d, want 1070/70/1070/0 (taxed at issue, not a liability)", subtotal, taxTotal, total, voucherIssueTotal)
	}
}

// A refused single-purpose redemption on replay is a PERMANENT failure of
// that one journal entry (the same inputs fail identically forever), so it
// must be quarantined rather than wedge the replica's whole batch —
// ut-docs#1127's allowlist, extended.
func TestPermanentJournalFailureReason_SinglePurposeMismatch(t *testing.T) {
	err := fmt.Errorf("payment 1: voucher %q: %w", "SP-X", data.ErrVoucherSinglePurposeMismatch)
	if reason := permanentJournalFailureReason(err); reason == "" {
		t.Fatalf("ErrVoucherSinglePurposeMismatch must classify as a permanent journal failure")
	}
	if reason := permanentJournalFailureReason(errors.New("database is locked")); reason != "" {
		t.Fatalf("a transient error must stay retryable, got %q", reason)
	}
}

// The primary's /redeem reservation refuses a PARTIAL single-purpose
// redemption with its own stable 409 reason (the sale-shape half of the
// rule is checked by the replica's own CompleteSale), commits nothing, and
// the lookup/redeem rows carry the stamped rate so the replica's local
// mirror can validate against it.
func TestSyncVouchers_RedeemRefusesPartialSinglePurpose(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	if err := repo.CreateVoucher(context.Background(), nil, data.Voucher{
		ID: "SP-SYNC", HolderLabel: "Alice", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "GBP",
		VoucherType: data.VoucherTypeSinglePurpose, TaxRateBP: 700, CreatedAt: "2026-09-19T09:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	rec := postSyncVoucher(mux, "SP-SYNC", "redeem", `{"sale_id":"sale-A","amount_minor":400}`, "bearer-t2")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"voucher_single_purpose_mismatch"`) {
		t.Fatalf("partial single-purpose redeem: status = %d body = %q, want 409 voucher_single_purpose_mismatch", rec.Code, rec.Body.String())
	}
	if v, _ := repo.GetVoucherBalance(context.Background(), nil, "SP-SYNC"); v.BalanceMinor != 1000 {
		t.Fatalf("refused reservation debited the voucher: balance %d", v.BalanceMinor)
	}

	rec = postSyncVoucher(mux, "SP-SYNC", "redeem", `{"sale_id":"sale-B","amount_minor":1000}`, "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("full-face single-purpose redeem: status = %d body = %q, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"voucher_type":"single_purpose"`) || !strings.Contains(rec.Body.String(), `"tax_rate_bp":700`) {
		t.Fatalf("redeem snapshot must carry the stamped type and rate for the replica's mirror, got %s", rec.Body.String())
	}
}

// The replica-side proxy classifies that 409 reason as a definitive
// refusal (the matching data sentinel), never a fall-back-to-local, and its
// mirror row keeps the rate the primary sent.
func TestVoucherProxy_SinglePurposeMismatchIsDefinitiveAndRateRoundTrips(t *testing.T) {
	row := voucherToSyncRow(data.Voucher{ID: "SP-1", OriginalAmountMinor: 700, BalanceMinor: 700, VoucherType: data.VoucherTypeSinglePurpose, TaxRateBP: 700})
	if row.TaxRateBP != 700 {
		t.Fatalf("voucherToSyncRow dropped tax_rate_bp: %+v", row)
	}
	if back := voucherFromSyncRow(row); back.TaxRateBP != 700 || back.VoucherType != data.VoucherTypeSinglePurpose {
		t.Fatalf("voucherFromSyncRow round trip = %+v", back)
	}
	if !errors.Is(syncVoucherRefusalError("SP-1", syncVoucherErrSinglePurposeMismatch), data.ErrVoucherSinglePurposeMismatch) {
		t.Fatalf("the proxy must map %q to data.ErrVoucherSinglePurposeMismatch", syncVoucherErrSinglePurposeMismatch)
	}
}

// The tender handler maps the sentinel to its own localized toast so the
// cashier is told WHY (sole exact payment, same rate) instead of the
// generic "could not be completed".
func TestClassifyTenderError_SinglePurposeMismatch(t *testing.T) {
	err := fmt.Errorf("payment 1: voucher %q: %w", "SP-X", data.ErrVoucherSinglePurposeMismatch)
	if got := classifyTenderError(err); got != "pos.toast.voucher_single_purpose_mismatch" {
		t.Fatalf("classifyTenderError = %q, want pos.toast.voucher_single_purpose_mismatch", got)
	}
}
