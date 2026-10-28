package data

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Voucher top-up as a sale (ut-docs#4252): TopUpVoucher credits an existing
// multi-purpose voucher inside the sale's transaction and records a
// 'top_up' ledger row (migration 080); voiding the sale takes the credit
// back; the day-close counts it as Issued.

const topUpTestCeiling = 100_000_000

func topUpNow() string { return time.Now().UTC().Format(time.RFC3339) }

func topUpRows(t *testing.T, repo *POSRepo, voucherID string) []VoucherTransaction {
	t.Helper()
	rows, err := repo.db.Query(`SELECT id, voucher_id, COALESCE(sale_id, ''), type, amount FROM voucher_transactions WHERE voucher_id = ? AND type IN ('top_up', 'adjustment') ORDER BY rowid`, voucherID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []VoucherTransaction
	for rows.Next() {
		var vt VoucherTransaction
		if err := rows.Scan(&vt.ID, &vt.VoucherID, &vt.SaleID, &vt.Type, &vt.AmountMinor); err != nil {
			t.Fatal(err)
		}
		out = append(out, vt)
	}
	return out
}

func topUpInTx(t *testing.T, repo *POSRepo, voucherID, saleID string, amount, ceiling int64) (Voucher, error) {
	t.Helper()
	return topUpInTxForce(t, repo, voucherID, saleID, amount, ceiling, false)
}

func topUpInTxForce(t *testing.T, repo *POSRepo, voucherID, saleID string, amount, ceiling int64, force bool) (Voucher, error) {
	t.Helper()
	tx, err := repo.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	v, err := repo.TopUpVoucher(context.Background(), tx, voucherID, saleID, amount, ceiling, force, topUpNow())
	if err != nil {
		_ = tx.Rollback()
		return v, err
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return v, nil
}

func TestTopUpVoucher_CreditsAndRecords(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-credit.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-1", 1000)
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-1", 400, false); err != nil {
		t.Fatal(err)
	}

	after, err := topUpInTx(t, repo, "GS-1", "sale-top", 2500, topUpTestCeiling)
	if err != nil {
		t.Fatalf("TopUpVoucher: %v", err)
	}
	if after.BalanceMinor != 3100 || after.Status != "active" || after.OriginalAmountMinor != 1000 {
		t.Fatalf("after top-up = %+v, want balance 3100 active, original 1000 unchanged", after)
	}
	got := topUpRows(t, repo, "GS-1")
	if len(got) != 1 || got[0].Type != "top_up" || got[0].SaleID != "sale-top" || got[0].AmountMinor != 2500 {
		t.Fatalf("ledger rows = %+v, want one top_up of 2500 for sale-top", got)
	}
	// The history the voucher page reads returns the top-up row.
	hist, err := repo.VoucherTransactions(ctx, "GS-1")
	if err != nil {
		t.Fatal(err)
	}
	if last := hist[len(hist)-1]; last.Type != "top_up" || last.AmountMinor != 2500 || last.SaleID != "sale-top" {
		t.Fatalf("history last row = %+v", last)
	}
}

func TestTopUpVoucher_RedeemedBecomesActive(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-redeemed.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-1", 1000)
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-1", 1000, false); err != nil {
		t.Fatal(err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-1"); v.Status != "redeemed" {
		t.Fatalf("precondition: status %s", v.Status)
	}
	after, err := topUpInTx(t, repo, "GS-1", "sale-top", 500, topUpTestCeiling)
	if err != nil {
		t.Fatalf("TopUpVoucher: %v", err)
	}
	if after.BalanceMinor != 500 || after.Status != "active" {
		t.Fatalf("after top-up = %+v, want 500 active", after)
	}
}

func TestTopUpVoucher_Refusals(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-refusals.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-OK", 1000)
	vSeedVoucher(t, ctx, repo, "GS-VOID", 1000)
	if _, err := d.DB.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VOID'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "GS-SP", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "EUR",
		Purpose: VoucherPurposeSingle, CreatedAt: topUpNow(),
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		id      string
		sale    string
		amount  int64
		ceiling int64
		want    error
	}{
		{"unknown", "GS-NOPE", "s1", 100, topUpTestCeiling, ErrVoucherNotFound},
		{"void", "GS-VOID", "s1", 100, topUpTestCeiling, ErrVoucherNotActive},
		{"single purpose", "GS-SP", "s1", 100, topUpTestCeiling, ErrVoucherNotMultiPurpose},
		{"above ceiling", "GS-OK", "s1", 1001, 2000, ErrVoucherTopUpOverCeiling},
		{"zero amount", "GS-OK", "s1", 0, topUpTestCeiling, ErrVoucherTopUpInvalid},
		{"no sale", "GS-OK", "", 100, topUpTestCeiling, ErrVoucherTopUpInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := topUpInTx(t, repo, tc.id, tc.sale, tc.amount, tc.ceiling)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// Exactly at the ceiling is fine.
	if _, err := topUpInTx(t, repo, "GS-OK", "s2", 1000, 2000); err != nil {
		t.Fatalf("top-up to exactly the ceiling: %v", err)
	}
	for _, id := range []string{"GS-VOID", "GS-SP"} {
		if rows := topUpRows(t, repo, id); len(rows) != 0 {
			t.Fatalf("%s: refused top-up left rows %+v", id, rows)
		}
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-VOID"); v.BalanceMinor != 0 || v.Status != "void" {
		t.Fatalf("void voucher changed: %+v", v)
	}
}

func TestTopUpVoucher_RequiresTx(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-notx.db")
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, context.Background(), repo, "GS-1", 1000)
	if _, err := repo.TopUpVoucher(context.Background(), nil, "GS-1", "s1", 100, topUpTestCeiling, false, topUpNow()); err == nil {
		t.Fatal("TopUpVoucher without a transaction must be refused")
	}
}

// voidSaleTx voids the sale's vouchers in one transaction, as
// pos.UpdateSaleStatus does.
func voidSaleTx(t *testing.T, repo *POSRepo, saleID string) error {
	t.Helper()
	tx, err := repo.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VoidVouchersIssuedInSale(context.Background(), tx, saleID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func TestVoidVouchersIssuedInSale_ReversesTopUp(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-void.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-1", 1000)
	if _, err := topUpInTx(t, repo, "GS-1", "sale-top", 2500, topUpTestCeiling); err != nil {
		t.Fatal(err)
	}
	if err := voidSaleTx(t, repo, "sale-top"); err != nil {
		t.Fatalf("void: %v", err)
	}
	v, _ := repo.GetVoucherBalance(ctx, nil, "GS-1")
	if v.BalanceMinor != 1000 || v.Status != "active" {
		t.Fatalf("after void = %+v, want the credit taken back (1000, active) and the voucher itself NOT voided", v)
	}
	// A repeat void debits nothing more.
	if err := voidSaleTx(t, repo, "sale-top"); err != nil {
		t.Fatalf("re-void: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("re-void debited twice: balance %d", v.BalanceMinor)
	}
	rows := topUpRows(t, repo, "GS-1")
	if len(rows) != 2 || rows[1].Type != "adjustment" || rows[1].AmountMinor != -2500 || rows[1].SaleID != "sale-top" {
		t.Fatalf("ledger = %+v, want the top_up plus ONE reversal (-2500) on the voided sale", rows)
	}
	// The history still adds up to the balance.
	hist, _ := repo.VoucherTransactions(ctx, "GS-1")
	var sum int64
	for _, h := range hist {
		switch h.Type {
		case "redemption":
			sum -= h.AmountMinor
		default:
			sum += h.AmountMinor
		}
	}
	// vSeedVoucher writes no issue row, so add the opening value.
	if sum+1000 != 1000 {
		t.Fatalf("history sums to %d (+1000 opening), want the balance 1000", sum+1000)
	}
}

func TestVoidVouchersIssuedInSale_TopUpBackToZeroIsRedeemed(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-void-zero.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-1", 1000)
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-1", 1000, false); err != nil {
		t.Fatal(err)
	}
	if _, err := topUpInTx(t, repo, "GS-1", "sale-top", 700, topUpTestCeiling); err != nil {
		t.Fatal(err)
	}
	if err := voidSaleTx(t, repo, "sale-top"); err != nil {
		t.Fatalf("void: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-1"); v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("after void = %+v, want 0 redeemed", v)
	}
}

func TestVoidVouchersIssuedInSale_TopUpSpentRefuses(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-void-spent.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-1", 1000)
	if _, err := topUpInTx(t, repo, "GS-1", "sale-top", 2500, topUpTestCeiling); err != nil {
		t.Fatal(err)
	}
	// Spend most of it: 3500 → 500, below the 2500 top-up.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-1", 3000, false); err != nil {
		t.Fatal(err)
	}
	if err := voidSaleTx(t, repo, "sale-top"); !errors.Is(err, ErrVoucherRedeemedCannotVoid) {
		t.Fatalf("void of a spent top-up: err = %v, want ErrVoucherRedeemedCannotVoid", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-1"); v.BalanceMinor != 500 {
		t.Fatalf("refused void changed the balance to %d", v.BalanceMinor)
	}
	if rows := topUpRows(t, repo, "GS-1"); len(rows) != 1 {
		t.Fatalf("refused void left a reversal row: %+v", rows)
	}
}

func TestVouchersIssuedRedeemed_TopUpCountsAsIssued(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-eod.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	b8Sale(t, d, "sale-new", b8At(today), "completed", "sale", 0, 1000)
	b8Sale(t, d, "sale-top", b8At(today), "completed", "sale", 0, 2500)
	b8Sale(t, d, "sale-top-void", b8At(today), "completed", "sale", 0, 400)

	vSeedVoucher(t, ctx, repo, "GS-NEW", 1000)
	vSeedVoucher(t, ctx, repo, "GS-OLD", 500)
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
		ID: "tx-i", VoucherID: "GS-NEW", SaleID: "sale-new", Type: "issue", AmountMinor: 1000, CreatedAt: b8At(today),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
		ID: "tx-t", VoucherID: "GS-OLD", SaleID: "sale-top", Type: "top_up", AmountMinor: 2500, CreatedAt: b8At(today),
	}); err != nil {
		t.Fatalf("RecordVoucherTransaction must accept top_up: %v", err)
	}
	// A top-up whose sale was voided is out, like a voided issue.
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
		ID: "tx-tv", VoucherID: "GS-OLD", SaleID: "sale-top-void", Type: "top_up", AmountMinor: 400, CreatedAt: b8At(today),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE sales SET status = 'voided' WHERE id = 'sale-top-void'`); err != nil {
		t.Fatal(err)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	sum, err := repo.VouchersIssuedRedeemedForRange(ctx, day, day)
	if err != nil {
		t.Fatal(err)
	}
	if sum.IssuedCount != 2 || sum.IssuedMinor != 3500 {
		t.Fatalf("range issued = %d/%d, want 2/3500 (issue + top-up)", sum.IssuedCount, sum.IssuedMinor)
	}
	inst, err := repo.VouchersIssuedRedeemedForInstantWindow(ctx, today.Add(-time.Hour), today.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if inst.IssuedCount != 2 || inst.IssuedMinor != 3500 {
		t.Fatalf("instant issued = %d/%d, want 2/3500 (issue + top-up)", inst.IssuedCount, inst.IssuedMinor)
	}
	if sum.ImportedCount != 0 || sum.AdjustedCount != 0 || inst.ImportedCount != 0 || inst.AdjustedCount != 0 {
		t.Fatalf("top-up leaked into another bucket: range %+v instant %+v", sum, inst)
	}
}

func TestVoucherTopUpsInSale_BalanceAfterAndReversal(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-in-sale.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	// A real issue row so the ledger carries the opening value.
	tx, _ := repo.db.Begin()
	if err := repo.CreateVoucher(ctx, tx, Voucher{ID: "GS-1", HolderLabel: "Sam", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "EUR", IssuedSaleID: "sale-a", CreatedAt: topUpNow()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVoucherTransaction(ctx, tx, VoucherTransaction{ID: "i1", VoucherID: "GS-1", SaleID: "sale-a", Type: "issue", AmountMinor: 1000, CreatedAt: topUpNow()}); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-1", 300, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{ID: "r1", VoucherID: "GS-1", SaleID: "sale-b", Type: "redemption", AmountMinor: 300, CreatedAt: topUpNow()}); err != nil {
		t.Fatal(err)
	}
	if _, err := topUpInTx(t, repo, "GS-1", "sale-top", 2000, topUpTestCeiling); err != nil {
		t.Fatal(err)
	}
	// A later redemption must not change what the top-up receipt says.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-1", 100, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{ID: "r2", VoucherID: "GS-1", SaleID: "sale-c", Type: "redemption", AmountMinor: 100, CreatedAt: topUpNow()}); err != nil {
		t.Fatal(err)
	}

	tops, err := repo.VoucherTopUpsInSale(ctx, "sale-top")
	if err != nil {
		t.Fatal(err)
	}
	if len(tops) != 1 {
		t.Fatalf("top-ups = %+v", tops)
	}
	got := tops[0]
	if got.VoucherID != "GS-1" || got.HolderLabel != "Sam" || got.AmountMinor != 2000 || got.BalanceAfterMinor != 2700 || got.Reversed {
		t.Fatalf("top-up = %+v, want GS-1/Sam +2000 → 2700, not reversed", got)
	}
	if none, _ := repo.VoucherTopUpsInSale(ctx, "sale-a"); len(none) != 0 {
		t.Fatalf("an issuing sale is not a top-up: %+v", none)
	}

	if err := voidSaleTx(t, repo, "sale-top"); err != nil {
		t.Fatal(err)
	}
	tops, _ = repo.VoucherTopUpsInSale(ctx, "sale-top")
	if len(tops) != 1 || !tops[0].Reversed || tops[0].BalanceAfterMinor != 2700 {
		t.Fatalf("after void top-ups = %+v, want reversed with the same balance-after", tops)
	}
}

// Review m2 (ut-docs#4252): voiding the sale that ISSUED a voucher after
// it was topped up is refused — the customer has paid for the added value,
// so the voucher can't simply be voided with it. Pinned so a later change
// to the void guard can't silently wipe a topped-up balance.
func TestVoidIssuingSale_RefusedAfterTopUp(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-void-issuer.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-1", 1000)
	issuer := "sale-GS-1"
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{ID: "vt-issue-GS-1", VoucherID: "GS-1", SaleID: issuer, Type: "issue", AmountMinor: 1000, CreatedAt: topUpNow()}); err != nil {
		t.Fatal(err)
	}
	if _, err := topUpInTx(t, repo, "GS-1", "sale-top", 2500, topUpTestCeiling); err != nil {
		t.Fatal(err)
	}
	if err := voidSaleTx(t, repo, issuer); !errors.Is(err, ErrVoucherRedeemedCannotVoid) {
		t.Fatalf("void of the issuing sale after a top-up: err = %v, want ErrVoucherRedeemedCannotVoid", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-1"); v.BalanceMinor != 3500 || v.Status != "active" {
		t.Fatalf("refused void changed the voucher: %+v", v)
	}
}

// Forced credit (ut-docs#4416): a replica's top-up was paid for at the till
// and the main till's journal replay must book it whatever the voucher's
// state is by then. Unknown and single-purpose still refuse; void and the
// ceiling are skipped; a void voucher stays void.
func TestTopUpVoucher_ForceCreditsVoidAndKeepsItVoid(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-force-void.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-VOID", 1000)
	if _, err := d.DB.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VOID'`); err != nil {
		t.Fatal(err)
	}
	if _, err := topUpInTxForce(t, repo, "GS-VOID", "s0", 100, topUpTestCeiling, false); !errors.Is(err, ErrVoucherNotActive) {
		t.Fatalf("unforced void top-up err = %v, want ErrVoucherNotActive", err)
	}
	after, err := topUpInTxForce(t, repo, "GS-VOID", "s1", 700, topUpTestCeiling, true)
	if err != nil {
		t.Fatalf("forced top-up of a void voucher: %v", err)
	}
	if after.BalanceMinor != 700 || after.Status != "void" {
		t.Fatalf("after forced top-up = %+v, want balance 700 and status still void", after)
	}
	rows := topUpRows(t, repo, "GS-VOID")
	if len(rows) != 1 || rows[0].Type != "top_up" || rows[0].AmountMinor != 700 || rows[0].SaleID != "s1" {
		t.Fatalf("ledger rows = %+v, want one top_up of 700", rows)
	}
}

func TestTopUpVoucher_ForceSkipsCeilingAndStillReactivatesRedeemed(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-force-ceiling.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-OK", 1000)
	if _, err := topUpInTxForce(t, repo, "GS-OK", "s0", 1001, 2000, false); !errors.Is(err, ErrVoucherTopUpOverCeiling) {
		t.Fatalf("unforced over-ceiling err = %v", err)
	}
	after, err := topUpInTxForce(t, repo, "GS-OK", "s1", 1500, 2000, true)
	if err != nil {
		t.Fatalf("forced over-ceiling top-up: %v", err)
	}
	if after.BalanceMinor != 2500 || after.Status != "active" {
		t.Fatalf("after = %+v, want 2500 active (ceiling skipped)", after)
	}

	vSeedVoucher(t, ctx, repo, "GS-RED", 1000)
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-RED", 1000, false); err != nil {
		t.Fatal(err)
	}
	after, err = topUpInTxForce(t, repo, "GS-RED", "s2", 300, topUpTestCeiling, true)
	if err != nil || after.BalanceMinor != 300 || after.Status != "active" {
		t.Fatalf("forced top-up of a redeemed voucher = %+v, %v; want 300 active", after, err)
	}
}

func TestTopUpVoucher_ForceStillRefusesUnknownAndSinglePurpose(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-force-refusals.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "GS-SP", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "EUR",
		Purpose: VoucherPurposeSingle, CreatedAt: topUpNow(),
	}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]error{"GS-NOPE": ErrVoucherNotFound, "GS-SP": ErrVoucherNotMultiPurpose} {
		_, err := topUpInTxForce(t, repo, id, "s1", 100, topUpTestCeiling, true)
		if !errors.Is(err, want) || !errors.Is(err, ErrVoucherTopUpRefused) {
			t.Fatalf("%s: forced err = %v, want %v wrapped in ErrVoucherTopUpRefused", id, err, want)
		}
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-SP"); v.BalanceMinor != 1000 {
		t.Fatalf("single-purpose balance moved to %d", v.BalanceMinor)
	}
}

// ut-docs#4416: an additional till's mirror of a voucher issued on the main
// till has no 'issue' row — EnsureVoucherLocalRow seeds the main till's
// balance with no history. The top-up receipt there must still show that
// balance plus the top-up, and keep showing it after a later redemption or a
// void of the top-up sale.
func TestVoucherTopUpsInSale_MirrorWithoutIssueRow(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-mirror.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	if err := repo.EnsureVoucherLocalRow(ctx, nil, Voucher{ID: "GS-M", HolderLabel: "Sam", OriginalAmountMinor: 5000, BalanceMinor: 2000, Currency: "EUR", Purpose: VoucherPurposeMulti, Status: "active", IssuedSaleID: "main-sale", CreatedAt: topUpNow()}); err != nil {
		t.Fatal(err)
	}
	if _, err := topUpInTx(t, repo, "GS-M", "sale-top", 2500, topUpTestCeiling); err != nil {
		t.Fatal(err)
	}
	want := func(label string, reversed bool) {
		t.Helper()
		tops, err := repo.VoucherTopUpsInSale(ctx, "sale-top")
		if err != nil {
			t.Fatal(err)
		}
		if len(tops) != 1 || tops[0].BalanceAfterMinor != 4500 || tops[0].Reversed != reversed {
			t.Fatalf("%s: top-ups = %+v, want balance after 4500 (2000 mirrored + 2500), reversed=%v", label, tops, reversed)
		}
	}
	want("right after", false)
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-M", 700, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{ID: "rm1", VoucherID: "GS-M", SaleID: "sale-c", Type: "redemption", AmountMinor: 700, CreatedAt: topUpNow()}); err != nil {
		t.Fatal(err)
	}
	want("after a later redemption", false)
	if err := voidSaleTx(t, repo, "sale-top"); err != nil {
		t.Fatal(err)
	}
	want("after the top-up sale is voided", true)
}

// ut-docs#4416 review m3: a forced credit can land on a voided voucher (a
// journal replay, or an additional till's stale mirror). Voiding that paid
// sale must still take the credit back — the voucher stays void — instead
// of refusing as if the voucher had been spent.
func TestReverseTopUp_ForcedCreditOnVoidVoucherIsVoidable(t *testing.T) {
	d := b8OpenDB(t, "voucher-topup-force-void-reverse.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-VR", 1000)
	if _, err := d.DB.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VR'`); err != nil {
		t.Fatal(err)
	}
	if _, err := topUpInTxForce(t, repo, "GS-VR", "s-forced", 700, topUpTestCeiling, true); err != nil {
		t.Fatal(err)
	}
	if err := voidSaleTx(t, repo, "s-forced"); err != nil {
		t.Fatalf("void of the forced top-up sale: %v, want nil", err)
	}
	v, err := repo.GetVoucherBalance(ctx, nil, "GS-VR")
	if err != nil {
		t.Fatal(err)
	}
	if v.BalanceMinor != 0 || v.Status != "void" {
		t.Fatalf("after void: balance %d status %s, want 0 void", v.BalanceMinor, v.Status)
	}
}
