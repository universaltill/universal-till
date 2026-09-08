package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Primary-side reservation repo tests (ADR-0084, ut-docs#1716): the atomic,
// idempotent ReserveVoucherRedemption / ReleaseVoucherRedemption pair behind
// POST /api/sync/vouchers/{id}/redeem and /release, plus the
// VoucherRedemptionRecorded pre-check pos.CompleteSale shares with them.
// Real migrated schema via b8OpenDB (migration 012 adds the
// ux_voucher_tx_redemption_once index these rely on), same as
// voucher_repo_test.go.

func rsNow() string { return time.Now().UTC().Format(time.RFC3339) }

// rsReserve runs ReserveVoucherRedemption inside its own transaction the way
// the /redeem handler does (the repo method requires a non-nil tx —
// sync_vouchers.go owns begin/commit).
func rsReserve(ctx context.Context, d *sql.DB, repo *POSRepo, voucherID, saleID string, amount int64) (Voucher, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Voucher{}, err
	}
	v, err := repo.ReserveVoucherRedemption(ctx, tx, voucherID, saleID, amount, rsNow())
	if err != nil {
		_ = tx.Rollback()
		return Voucher{}, err
	}
	if err := tx.Commit(); err != nil {
		return Voucher{}, err
	}
	return v, nil
}

func rsRelease(ctx context.Context, d *sql.DB, repo *POSRepo, voucherID, saleID string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := repo.ReleaseVoucherRedemption(ctx, tx, voucherID, saleID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func rsRedemptionRows(t *testing.T, d *sql.DB, voucherID, saleID string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = ? AND sale_id = ? AND type = 'redemption'`, voucherID, saleID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVoucherRepo_ReserveDebitsOnceAndReturnsPreDebitSnapshot(t *testing.T) {
	d := b8OpenDB(t, "voucher-reserve.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-RSV", 1000)

	if ok, err := repo.VoucherRedemptionRecorded(ctx, nil, "GS-RSV", "sale-1"); err != nil || ok {
		t.Fatalf("precondition: VoucherRedemptionRecorded = %v/%v, want false/nil", ok, err)
	}

	v, err := rsReserve(ctx, d.DB, repo, "GS-RSV", "sale-1", 300)
	if err != nil {
		t.Fatalf("ReserveVoucherRedemption: %v", err)
	}
	// Correction 5 (ADR-0084 brief addendum): the returned snapshot is the
	// PRE-debit one — it seeds the replica's local mirror row
	// (EnsureVoucherLocalRow), whose own imminent forced local debit must
	// not double-count the reduction.
	if v.ID != "GS-RSV" || v.BalanceMinor != 1000 || v.Status != "active" || v.HolderLabel != "Sample Holder" {
		t.Fatalf("returned snapshot = %+v, want the PRE-debit row (balance 1000, active)", v)
	}

	after, err := repo.GetVoucherBalance(ctx, nil, "GS-RSV")
	if err != nil || after.BalanceMinor != 700 || after.Status != "active" {
		t.Fatalf("DB state after reserve = %+v (err %v), want balance 700 / active", after, err)
	}
	if n := rsRedemptionRows(t, d.DB, "GS-RSV", "sale-1"); n != 1 {
		t.Fatalf("redemption rows for (GS-RSV, sale-1) = %d, want 1", n)
	}
	if ok, err := repo.VoucherRedemptionRecorded(ctx, nil, "GS-RSV", "sale-1"); err != nil || !ok {
		t.Fatalf("VoucherRedemptionRecorded after reserve = %v/%v, want true/nil", ok, err)
	}
	// A different sale id for the same voucher is NOT recorded.
	if ok, _ := repo.VoucherRedemptionRecorded(ctx, nil, "GS-RSV", "sale-other"); ok {
		t.Fatal("VoucherRedemptionRecorded must be keyed on the (voucher, sale) PAIR, not the voucher alone")
	}
}

func TestVoucherRepo_ReserveIsIdempotentOnRetry(t *testing.T) {
	d := b8OpenDB(t, "voucher-reserve-idem.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-IDEM", 1000)

	first, err := rsReserve(ctx, d.DB, repo, "GS-IDEM", "sale-1", 300)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	// The replica's first response was lost; it retries the identical call.
	second, err := rsReserve(ctx, d.DB, repo, "GS-IDEM", "sale-1", 300)
	if err != nil {
		t.Fatalf("retried reserve must succeed idempotently, got %v", err)
	}
	if second.BalanceMinor != first.BalanceMinor || second.Status != "active" {
		t.Fatalf("retry returned %+v, want the same pre-debit snapshot as the first call (%+v)", second, first)
	}
	after, _ := repo.GetVoucherBalance(ctx, nil, "GS-IDEM")
	if after.BalanceMinor != 700 {
		t.Fatalf("balance after retried reserve = %d, want 700 (debited exactly ONCE)", after.BalanceMinor)
	}
	if n := rsRedemptionRows(t, d.DB, "GS-IDEM", "sale-1"); n != 1 {
		t.Fatalf("redemption rows after retry = %d, want exactly 1", n)
	}
}

// A "retry" for an already-recorded (voucher_id, sale_id) that names a
// DIFFERENT amount than the one first recorded is refused, not treated as
// an idempotent no-op (independent review finding — /redeem is an
// externally-reachable endpoint, so external input must be validated even
// though this repo's own client never legitimately triggers this).
func TestVoucherRepo_ReserveRejectsRetryAtADifferentAmount(t *testing.T) {
	d := b8OpenDB(t, "voucher-reserve-mismatch.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-MISMATCH", 1000)

	if _, err := rsReserve(ctx, d.DB, repo, "GS-MISMATCH", "sale-1", 300); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if _, err := rsReserve(ctx, d.DB, repo, "GS-MISMATCH", "sale-1", 400); !errors.Is(err, ErrVoucherRedemptionAmountMismatch) {
		t.Fatalf("retry at a different amount: err = %v, want ErrVoucherRedemptionAmountMismatch", err)
	}
	// The mismatched retry must debit nothing further — balance still
	// reflects only the FIRST, successful reservation.
	after, _ := repo.GetVoucherBalance(ctx, nil, "GS-MISMATCH")
	if after.BalanceMinor != 700 {
		t.Fatalf("balance after a rejected mismatched retry = %d, want 700 (only the first reservation applied)", after.BalanceMinor)
	}
	if n := rsRedemptionRows(t, d.DB, "GS-MISMATCH", "sale-1"); n != 1 {
		t.Fatalf("redemption rows after a rejected mismatched retry = %d, want exactly 1", n)
	}
}

func TestVoucherRepo_ReservePropagatesDebitRefusalsAndDebitsNothing(t *testing.T) {
	d := b8OpenDB(t, "voucher-reserve-refuse.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-SHORT", 100)
	vSeedVoucher(t, ctx, repo, "GS-VOID", 500)
	if _, err := d.DB.ExecContext(ctx, `UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VOID'`); err != nil {
		t.Fatal(err)
	}

	if _, err := rsReserve(ctx, d.DB, repo, "GS-SHORT", "sale-1", 300); !errors.Is(err, ErrVoucherInsufficientBalance) {
		t.Fatalf("insufficient balance: err = %v, want ErrVoucherInsufficientBalance", err)
	}
	if _, err := rsReserve(ctx, d.DB, repo, "GS-VOID", "sale-2", 1); !errors.Is(err, ErrVoucherNotActive) {
		t.Fatalf("void voucher: err = %v, want ErrVoucherNotActive", err)
	}
	if _, err := rsReserve(ctx, d.DB, repo, "GS-NOPE", "sale-3", 1); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("unknown voucher: err = %v, want ErrVoucherNotFound", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-SHORT"); v.BalanceMinor != 100 {
		t.Fatalf("GS-SHORT balance after refused reserve = %d, want untouched 100", v.BalanceMinor)
	}
	var total int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE type = 'redemption'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("refused reserves wrote %d redemption rows, want 0", total)
	}

	// The repo method requires the caller's transaction.
	if _, err := repo.ReserveVoucherRedemption(ctx, nil, "GS-SHORT", "sale-x", 1, rsNow()); err == nil {
		t.Fatal("ReserveVoucherRedemption with a nil tx must be refused")
	}
	if _, err := rsReserve(ctx, d.DB, repo, "GS-SHORT", "", 1); err == nil {
		t.Fatal("ReserveVoucherRedemption with an empty sale id must be refused (it is the idempotency key)")
	}
}

// The DB-level constraint, exercised through the repository's own writer:
// a second 'redemption' row for the same (voucher_id, sale_id) is refused by
// ux_voucher_tx_redemption_once and surfaces as the typed sentinel. (The
// literal-INSERT proof that the index exists independent of any Go code is
// internal/db's TestUxVoucherTxRedemptionOnceRejectsDuplicateRow.)
func TestVoucherRepo_RecordVoucherTransactionRejectsDuplicateRedemption(t *testing.T) {
	d := b8OpenDB(t, "voucher-dup-redemption.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-DUPR", 1000)

	row := VoucherTransaction{ID: "tx-a", VoucherID: "GS-DUPR", SaleID: "sale-1", Type: "redemption", AmountMinor: 100, CreatedAt: rsNow()}
	if err := repo.RecordVoucherTransaction(ctx, nil, row); err != nil {
		t.Fatalf("first row: %v", err)
	}
	row.ID = "tx-b"
	err := repo.RecordVoucherTransaction(ctx, nil, row)
	if !errors.Is(err, ErrVoucherRedemptionAlreadyRecorded) {
		t.Fatalf("second redemption row for the same (voucher, sale): err = %v, want ErrVoucherRedemptionAlreadyRecorded", err)
	}
	// Still only one row; an 'issue' for the same pair is unaffected.
	if n := rsRedemptionRows(t, d.DB, "GS-DUPR", "sale-1"); n != 1 {
		t.Fatalf("redemption rows = %d, want 1", n)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{ID: "tx-c", VoucherID: "GS-DUPR", SaleID: "sale-1", Type: "issue", AmountMinor: 1000, CreatedAt: rsNow()}); err != nil {
		t.Fatalf("issue row for the same pair must not be caught by the partial index: %v", err)
	}
}

// ut-docs#1716 acceptance criterion #2, literally, at the repo layer: a
// voucher holding 100; two tills each reserve 60 (sum 120 > 100) for two
// DIFFERENT sale ids at the same moment. Exactly one may win; the other gets
// ErrVoucherInsufficientBalance; the balance lands on exactly 40.
//
// This is a real concurrent test, not a sequential simulation (brief
// addendum, correction 9): internal/db.Open uses _txlock=immediate with a 5s
// busy_timeout and no SetMaxOpenConns override, so two goroutines each
// opening BeginTx on the same *sql.DB genuinely serialize at BEGIN IMMEDIATE
// — the second waits for the first to commit, then runs against the
// now-reduced balance. Two guards independently catch it there and this test
// does NOT distinguish which one fires (independent review finding —
// verified by reverting each in isolation: both alone are still sufficient):
// DebitVoucherForRedemption's own UPDATE repeats `balance >= ?` in its WHERE
// clause, AND ReserveVoucherRedemption pre-reads the balance under the same
// transaction before calling it. The redundancy is deliberate defence in
// depth (see also TestUxVoucherTxRedemptionOnceRejectsDuplicateRow for the
// third, DB-level layer against a duplicate SAME sale id) — this test only
// proves the race is closed, not which layer closes it. Iterated so the
// interleave actually occurs across fresh vouchers.
func TestVoucherRepo_ConcurrentReserveOnlyOneWins(t *testing.T) {
	d := b8OpenDB(t, "voucher-reserve-race.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("GS-RACE-%d", i)
		vSeedVoucher(t, ctx, repo, id, 100)

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for g := 0; g < 2; g++ {
			wg.Add(1)
			saleID := fmt.Sprintf("sale-%d-till-%d", i, g)
			go func() {
				defer wg.Done()
				<-start
				_, err := rsReserve(ctx, d.DB, repo, id, saleID, 60)
				errs <- err
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
			if !errors.Is(err, ErrVoucherInsufficientBalance) {
				t.Fatalf("iteration %d: losing reserve returned %v, want ErrVoucherInsufficientBalance", i, err)
			}
		}
		if okCount != 1 {
			t.Fatalf("iteration %d: %d reserves succeeded, want exactly 1 (both succeeding is the over-redeem this card closes)", i, okCount)
		}
		v, err := repo.GetVoucherBalance(ctx, nil, id)
		if err != nil {
			t.Fatalf("iteration %d: read voucher: %v", i, err)
		}
		if v.BalanceMinor != 40 || v.Status != "active" {
			t.Fatalf("iteration %d: balance=%d status=%q after the race, want 40/'active' (negative = double-debited)", i, v.BalanceMinor, v.Status)
		}
		var rows int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = ? AND type = 'redemption'`, id).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("iteration %d: redemption rows = %d, want exactly 1", i, rows)
		}
	}
}

func TestVoucherRepo_ReleaseCreditsBackAndIsIdempotent(t *testing.T) {
	d := b8OpenDB(t, "voucher-release.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-REL", 1000)

	if _, err := rsReserve(ctx, d.DB, repo, "GS-REL", "sale-1", 300); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := rsRelease(ctx, d.DB, repo, "GS-REL", "sale-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	v, _ := repo.GetVoucherBalance(ctx, nil, "GS-REL")
	if v.BalanceMinor != 1000 || v.Status != "active" {
		t.Fatalf("after release: %+v, want balance 1000 / active", v)
	}
	if n := rsRedemptionRows(t, d.DB, "GS-REL", "sale-1"); n != 0 {
		t.Fatalf("redemption rows after release = %d, want 0", n)
	}
	// Already released: a no-op success, and the balance is NOT credited a
	// second time.
	if err := rsRelease(ctx, d.DB, repo, "GS-REL", "sale-1"); err != nil {
		t.Fatalf("second release must be a no-op success, got %v", err)
	}
	if v, _ = repo.GetVoucherBalance(ctx, nil, "GS-REL"); v.BalanceMinor != 1000 {
		t.Fatalf("balance after double release = %d, want 1000 (never credited twice)", v.BalanceMinor)
	}
	// Never reserved (a different sale id, an unknown voucher): no-op, no
	// error — this is what makes a release safe to fire for a refused
	// reservation.
	if err := rsRelease(ctx, d.DB, repo, "GS-REL", "sale-never"); err != nil {
		t.Fatalf("release of an unreserved sale id must be a no-op, got %v", err)
	}
	if err := rsRelease(ctx, d.DB, repo, "GS-NOWHERE", "sale-1"); err != nil {
		t.Fatalf("release of an unknown voucher must be a no-op, got %v", err)
	}
	if err := repo.ReleaseVoucherRedemption(ctx, nil, "GS-REL", "sale-1"); err == nil {
		t.Fatal("ReleaseVoucherRedemption with a nil tx must be refused")
	}
}

func TestVoucherRepo_ReleaseOfFullDrainFlipsRedeemedBackToActive(t *testing.T) {
	d := b8OpenDB(t, "voucher-release-drain.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-DRAIN", 500)

	if _, err := rsReserve(ctx, d.DB, repo, "GS-DRAIN", "sale-1", 500); err != nil {
		t.Fatalf("draining reserve: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-DRAIN"); v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("after draining reserve: %+v, want 0/'redeemed'", v)
	}
	if err := rsRelease(ctx, d.DB, repo, "GS-DRAIN", "sale-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	v, _ := repo.GetVoucherBalance(ctx, nil, "GS-DRAIN")
	if v.BalanceMinor != 500 || v.Status != "active" {
		t.Fatalf("after releasing a full drain: %+v, want 500/'active'", v)
	}
	// And it is redeemable again from a live till.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-DRAIN", 100, false); err != nil {
		t.Fatalf("debit after release: %v", err)
	}
}

// Release never resurrects a voided voucher: 'void' is a different, worse
// problem than a reservation being unwound, so a release that finds the
// voucher void deletes its own bookkeeping row and leaves balance/status
// exactly as the void set them.
func TestVoucherRepo_ReleaseNeverTouchesVoid(t *testing.T) {
	d := b8OpenDB(t, "voucher-release-void.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	vSeedVoucher(t, ctx, repo, "GS-RV", 500)
	if _, err := rsReserve(ctx, d.DB, repo, "GS-RV", "sale-1", 200); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-RV'`); err != nil {
		t.Fatal(err)
	}
	if err := rsRelease(ctx, d.DB, repo, "GS-RV", "sale-1"); err != nil {
		t.Fatalf("release on a void voucher must not error: %v", err)
	}
	v, _ := repo.GetVoucherBalance(ctx, nil, "GS-RV")
	if v.Status != "void" || v.BalanceMinor != 0 {
		t.Fatalf("release resurrected a void voucher: %+v", v)
	}
	if n := rsRedemptionRows(t, d.DB, "GS-RV", "sale-1"); n != 0 {
		t.Fatalf("release must still drop its own reservation row, got %d", n)
	}
}
