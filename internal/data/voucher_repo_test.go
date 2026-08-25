package data

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Voucher repo tests (ut-docs#1008) — real migrated schema via b8OpenDB
// (internal/db.Open runs migration 067), never a hand-built twin.

func vSeedVoucher(t *testing.T, ctx context.Context, repo *POSRepo, id string, amount int64) {
	t.Helper()
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: id, HolderLabel: "Sample Holder", OriginalAmountMinor: amount,
		BalanceMinor: amount, Currency: "EUR", IssuedSaleID: "sale-" + id,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("CreateVoucher %s: %v", id, err)
	}
}

func TestVoucherRepo_CreateAndGetBalance(t *testing.T) {
	d := b8OpenDB(t, "voucher-create.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	vSeedVoucher(t, ctx, repo, "GS-A", 1500)

	v, err := repo.GetVoucherBalance(ctx, "GS-A")
	if err != nil {
		t.Fatalf("GetVoucherBalance: %v", err)
	}
	if v.ID != "GS-A" || v.HolderLabel != "Sample Holder" || v.BalanceMinor != 1500 ||
		v.OriginalAmountMinor != 1500 || v.Status != "active" || v.VoucherType != "multi_purpose" || v.Currency != "EUR" {
		t.Fatalf("voucher = %+v", v)
	}

	if _, err := repo.GetVoucherBalance(ctx, "GS-MISSING"); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("missing voucher: err = %v, want ErrVoucherNotFound", err)
	}
}

func TestVoucherRepo_DebitValidatesBalanceAndStatus(t *testing.T) {
	d := b8OpenDB(t, "voucher-debit.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	vSeedVoucher(t, ctx, repo, "GS-B", 1000)

	// Overspend refused with the typed error, balance untouched.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-B", 1500); !errors.Is(err, ErrVoucherInsufficientBalance) {
		t.Fatalf("overspend: err = %v, want ErrVoucherInsufficientBalance", err)
	}
	v, err := repo.GetVoucherBalance(ctx, "GS-B")
	if err != nil || v.BalanceMinor != 1000 {
		t.Fatalf("balance after refused debit = %d (err %v), want 1000", v.BalanceMinor, err)
	}

	// Partial debit keeps it active; draining flips to redeemed.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-B", 400); err != nil {
		t.Fatalf("partial debit: %v", err)
	}
	if v, _ = repo.GetVoucherBalance(ctx, "GS-B"); v.BalanceMinor != 600 || v.Status != "active" {
		t.Fatalf("after partial debit: balance=%d status=%q", v.BalanceMinor, v.Status)
	}
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-B", 600); err != nil {
		t.Fatalf("draining debit: %v", err)
	}
	if v, _ = repo.GetVoucherBalance(ctx, "GS-B"); v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("after draining: balance=%d status=%q", v.BalanceMinor, v.Status)
	}

	// A non-active voucher refuses further debits.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-B", 1); !errors.Is(err, ErrVoucherNotActive) {
		t.Fatalf("debit on redeemed voucher: err = %v, want ErrVoucherNotActive", err)
	}
	// Unknown voucher.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-NONE", 1); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("debit on unknown voucher: err = %v, want ErrVoucherNotFound", err)
	}
}

// VouchersIssuedRedeemedForRange matches the shop's LOCAL calendar day
// (date(created_at, 'localtime'), ut-docs#869 convention) — a yesterday
// transaction stays out of today's window in every host timezone.
func TestVoucherRepo_IssuedRedeemedForRange_LocalDayWindow(t *testing.T) {
	d := b8OpenDB(t, "voucher-range.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	vSeedVoucher(t, ctx, repo, "GS-C", 5000)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)

	rec := func(id, txType string, amount int64, at time.Time) {
		t.Helper()
		if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
			ID: id, VoucherID: "GS-C", SaleID: "sale-x", Type: txType,
			AmountMinor: amount, CreatedAt: b8At(at),
		}); err != nil {
			t.Fatalf("RecordVoucherTransaction %s: %v", id, err)
		}
	}
	rec("tx-prev", "issue", 9999, yesterday)            // out of window
	rec("tx-i1", "issue", 1500, today)                  // in
	rec("tx-i2", "issue", 2500, today.Add(4*time.Hour)) // in
	rec("tx-r1", "redemption", 1000, today)             // in

	day := b8ExpectedDay(t, d, today, 0, 0)
	sum, err := repo.VouchersIssuedRedeemedForRange(ctx, day, day)
	if err != nil {
		t.Fatalf("VouchersIssuedRedeemedForRange: %v", err)
	}
	if sum.IssuedCount != 2 || sum.IssuedMinor != 4000 {
		t.Fatalf("issued = %d/%d, want 2/4000", sum.IssuedCount, sum.IssuedMinor)
	}
	if sum.RedeemedCount != 1 || sum.RedeemedMinor != 1000 {
		t.Fatalf("redeemed = %d/%d, want 1/1000", sum.RedeemedCount, sum.RedeemedMinor)
	}
}

// The day-close report carries the voucher flows, folded in distinctly:
// EODReport gains issued/redeemed count+amount, while Gross/TaxNet keep
// coming from sales rows only (no double counting from voucher_transactions).
func TestEODReport_IncludesVoucherFlows(t *testing.T) {
	d := b8OpenDB(t, "voucher-eod.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())

	// One completed article sale (via direct row seed — this test targets the
	// aggregation, the CompleteSale wiring is covered in internal/pos).
	b8Sale(t, d, "sale-eod-1", b8At(today), "completed", "sale", 190, 1190)

	vSeedVoucher(t, ctx, repo, "GS-D", 1500)
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
		ID: "tx-eod-i", VoucherID: "GS-D", SaleID: "sale-eod-1", Type: "issue",
		AmountMinor: 1500, CreatedAt: b8At(today),
	}); err != nil {
		t.Fatalf("record issue: %v", err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
		ID: "tx-eod-r", VoucherID: "GS-D", SaleID: "sale-eod-1", Type: "redemption",
		AmountMinor: 300, CreatedAt: b8At(today),
	}); err != nil {
		t.Fatalf("record redemption: %v", err)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}
	if rep.VouchersIssuedCount != 1 || rep.VouchersIssued != 1500 {
		t.Fatalf("EOD vouchers issued = %d/%d, want 1/1500", rep.VouchersIssuedCount, rep.VouchersIssued)
	}
	if rep.VouchersRedeemedCount != 1 || rep.VouchersRedeemed != 300 {
		t.Fatalf("EOD vouchers redeemed = %d/%d, want 1/300", rep.VouchersRedeemedCount, rep.VouchersRedeemed)
	}
	// Distinct, not double counted: Gross stays the sales-row figure.
	if rep.Gross != 1190 {
		t.Fatalf("EOD gross = %d, want 1190 (sales rows only)", rep.Gross)
	}
}

// F8 (ut-docs#1008 review): the guarded-UPDATE race protection in
// DebitVoucherForRedemption, exercised for real. Two concurrent debits
// against a balance that covers exactly ONE of them: exactly one may
// succeed, the loser must get the typed refusal (insufficient-balance from
// the guarded UPDATE's affected-rows check when the race interleaves, or
// not-active from the pre-read when the winner already drained it), and the
// balance must land at exactly zero — never negative. Iterated across fresh
// vouchers so the read/update interleave actually occurs: with the WHERE
// guards removed from the UPDATE, an interleaved pair double-debits the
// balance to -amount, which this test catches.
func TestVoucherRepo_ConcurrentDebitOnlyOneWins(t *testing.T) {
	d := b8OpenDB(t, "voucher-race.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("GS-RACE-%d", i)
		vSeedVoucher(t, ctx, repo, id, 1000)

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs <- repo.DebitVoucherForRedemption(ctx, nil, id, 1000)
			}()
		}
		close(start)
		wg.Wait()
		close(errs)

		var okCount int
		for err := range errs {
			if err == nil {
				okCount++
				continue
			}
			if !errors.Is(err, ErrVoucherInsufficientBalance) && !errors.Is(err, ErrVoucherNotActive) {
				t.Fatalf("iteration %d: losing debit returned %v, want ErrVoucherInsufficientBalance or ErrVoucherNotActive", i, err)
			}
		}
		if okCount != 1 {
			t.Fatalf("iteration %d: %d debits succeeded, want exactly 1", i, okCount)
		}
		v, err := repo.GetVoucherBalance(ctx, id)
		if err != nil {
			t.Fatalf("iteration %d: read voucher: %v", i, err)
		}
		if v.BalanceMinor != 0 || v.Status != "redeemed" {
			t.Fatalf("iteration %d: balance=%d status=%q after the race, want 0/'redeemed' (a negative balance means the guard clause is gone)", i, v.BalanceMinor, v.Status)
		}
	}
}
