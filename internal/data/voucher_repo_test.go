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
// (internal/db.Open runs migration 068), never a hand-built twin.

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

	v, err := repo.GetVoucherBalance(ctx, nil, "GS-A")
	if err != nil {
		t.Fatalf("GetVoucherBalance: %v", err)
	}
	if v.ID != "GS-A" || v.HolderLabel != "Sample Holder" || v.BalanceMinor != 1500 ||
		v.OriginalAmountMinor != 1500 || v.Status != "active" || v.VoucherType != "multi_purpose" || v.Currency != "EUR" {
		t.Fatalf("voucher = %+v", v)
	}

	if _, err := repo.GetVoucherBalance(ctx, nil, "GS-MISSING"); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("missing voucher: err = %v, want ErrVoucherNotFound", err)
	}
}

func TestVoucherRepo_DebitValidatesBalanceAndStatus(t *testing.T) {
	d := b8OpenDB(t, "voucher-debit.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	vSeedVoucher(t, ctx, repo, "GS-B", 1000)

	// Overspend refused with the typed error, balance untouched.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-B", 1500, false); !errors.Is(err, ErrVoucherInsufficientBalance) {
		t.Fatalf("overspend: err = %v, want ErrVoucherInsufficientBalance", err)
	}
	v, err := repo.GetVoucherBalance(ctx, nil, "GS-B")
	if err != nil || v.BalanceMinor != 1000 {
		t.Fatalf("balance after refused debit = %d (err %v), want 1000", v.BalanceMinor, err)
	}

	// Partial debit keeps it active; draining flips to redeemed.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-B", 400, false); err != nil {
		t.Fatalf("partial debit: %v", err)
	}
	if v, _ = repo.GetVoucherBalance(ctx, nil, "GS-B"); v.BalanceMinor != 600 || v.Status != "active" {
		t.Fatalf("after partial debit: balance=%d status=%q", v.BalanceMinor, v.Status)
	}
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-B", 600, false); err != nil {
		t.Fatalf("draining debit: %v", err)
	}
	if v, _ = repo.GetVoucherBalance(ctx, nil, "GS-B"); v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("after draining: balance=%d status=%q", v.BalanceMinor, v.Status)
	}

	// A non-active voucher refuses further debits.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-B", 1, false); !errors.Is(err, ErrVoucherNotActive) {
		t.Fatalf("debit on redeemed voucher: err = %v, want ErrVoucherNotActive", err)
	}
	// Unknown voucher.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-NONE", 1, false); !errors.Is(err, ErrVoucherNotFound) {
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

// TestVoucherRepo_IssuedRedeemedForRange_SeparatesImportedOpeningBalance
// (ut-docs#1834): an opening-balance voucher import (internal/pages/
// import_vouchers_page.go) calls RecordVoucherTransaction(type: "issue")
// with an EMPTY SaleID — by construction, there is no sale. Without this
// separate bucket, VouchersIssuedRedeemedForRange (and its InstantWindow
// sibling) would silently count that row as "Issued today" on whatever
// calendar day the operator happened to run the import, inflating that
// day's Z-report and misleading the operator into thinking N vouchers were
// sold today via a real sale. A normal SALE-issued voucher
// (RecordVoucherTransaction with a real, non-empty SaleID — the only other
// in-tree caller of type='issue', internal/pos/sales.go) must still count
// exactly as before, as Issued, never as Imported.
func TestVoucherRepo_IssuedRedeemedForRange_SeparatesImportedOpeningBalance(t *testing.T) {
	d := b8OpenDB(t, "voucher-range-import-exclude.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	vSeedVoucher(t, ctx, repo, "GS-IMP-SOLD", 3000)
	vSeedVoucher(t, ctx, repo, "GS-IMP-OPENING", 7500)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())

	// A real sale-issued voucher: non-empty SaleID.
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
		ID: "tx-sold", VoucherID: "GS-IMP-SOLD", SaleID: "sale-real-1", Type: "issue",
		AmountMinor: 3000, CreatedAt: b8At(today),
	}); err != nil {
		t.Fatalf("RecordVoucherTransaction (sale-issued): %v", err)
	}
	// An imported opening balance: empty SaleID, exactly as
	// import_vouchers_page.go's commit path calls it.
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{
		ID: "tx-imported", VoucherID: "GS-IMP-OPENING", SaleID: "", Type: "issue",
		AmountMinor: 7500, CreatedAt: b8At(today),
	}); err != nil {
		t.Fatalf("RecordVoucherTransaction (imported opening balance): %v", err)
	}

	day := b8ExpectedDay(t, d, today, 0, 0)
	sum, err := repo.VouchersIssuedRedeemedForRange(ctx, day, day)
	if err != nil {
		t.Fatalf("VouchersIssuedRedeemedForRange: %v", err)
	}
	if sum.IssuedCount != 1 || sum.IssuedMinor != 3000 {
		t.Fatalf("issued = %d/%d, want 1/3000 (only the sale-issued voucher; the imported opening balance must not count here)", sum.IssuedCount, sum.IssuedMinor)
	}
	if sum.ImportedCount != 1 || sum.ImportedMinor != 7500 {
		t.Fatalf("imported = %d/%d, want 1/7500 (the opening-balance import, bucketed separately from Issued)", sum.ImportedCount, sum.ImportedMinor)
	}

	// Same separation on the InstantWindow sibling (ADR-0066 Decision 2).
	from := today.Add(-1 * time.Hour)
	to := today.Add(1 * time.Hour)
	sumInstant, err := repo.VouchersIssuedRedeemedForInstantWindow(ctx, from, to)
	if err != nil {
		t.Fatalf("VouchersIssuedRedeemedForInstantWindow: %v", err)
	}
	if sumInstant.IssuedCount != 1 || sumInstant.IssuedMinor != 3000 {
		t.Fatalf("instant window issued = %d/%d, want 1/3000 (imported opening balance must not count here)", sumInstant.IssuedCount, sumInstant.IssuedMinor)
	}
	if sumInstant.ImportedCount != 1 || sumInstant.ImportedMinor != 7500 {
		t.Fatalf("instant window imported = %d/%d, want 1/7500", sumInstant.ImportedCount, sumInstant.ImportedMinor)
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
				errs <- repo.DebitVoucherForRedemption(ctx, nil, id, 1000, false)
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
		v, err := repo.GetVoucherBalance(ctx, nil, id)
		if err != nil {
			t.Fatalf("iteration %d: read voucher: %v", i, err)
		}
		if v.BalanceMinor != 0 || v.Status != "redeemed" {
			t.Fatalf("iteration %d: balance=%d status=%q after the race, want 0/'redeemed' (a negative balance means the guard clause is gone)", i, v.BalanceMinor, v.Status)
		}
	}
}

// ut-docs#1053: journal replay of a genuine offline double-spend must be able
// to force the debit past the balance gate (the remote sale already happened
// — same reasoning as pos.SaleInput.AllowNegativeInventory), leaving a
// negative balance the caller surfaces as a Problem. force changes ONLY the
// balance check: a voided/redeemed/unknown voucher stays a hard reject — a
// nonexistent or dead voucher is a different, worse problem than a balance
// race.
func TestVoucherRepo_DebitForceAllowsOverdraft(t *testing.T) {
	d := b8OpenDB(t, "voucher-force.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	vSeedVoucher(t, ctx, repo, "GS-FORCE", 500)

	// Control: the non-replay path is unaffected — overdraft still rejected.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-FORCE", 800, false); !errors.Is(err, ErrVoucherInsufficientBalance) {
		t.Fatalf("force=false overspend: err = %v, want ErrVoucherInsufficientBalance", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-FORCE"); v.BalanceMinor != 500 {
		t.Fatalf("balance after refused debit = %d, want 500", v.BalanceMinor)
	}

	// Replay path: the same debit force-applies and the balance goes negative.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-FORCE", 800, true); err != nil {
		t.Fatalf("force=true overspend: %v, want success (the remote sale already happened)", err)
	}
	v, err := repo.GetVoucherBalance(ctx, nil, "GS-FORCE")
	if err != nil {
		t.Fatalf("read voucher: %v", err)
	}
	if v.BalanceMinor != -300 || v.Status != "active" {
		t.Fatalf("after forced overdraft: balance=%d status=%q, want -300/'active'", v.BalanceMinor, v.Status)
	}

	// force does NOT bypass the not-found rejection.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-NOWHERE", 100, true); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("force on unknown voucher: err = %v, want ErrVoucherNotFound", err)
	}

	// force DOES bypass a 'redeemed' status — this is the exact-drain
	// double-spend shape (review finding, ut-docs#1053): the first replica's
	// replay drains the voucher to exactly zero, flipping status to
	// 'redeemed', and the second replica's replay of the SAME voucher's
	// redemption must still force-apply, not hard-reject with
	// ErrVoucherNotActive — that would permanently wedge the second
	// replica's journal on every subsequent sale (see the pages-layer
	// TestApplyJournal_DoubleRedemptionRaceForceAppliesAndSurfacesProblem-
	// style coverage for the exact-drain variant).
	vSeedVoucher(t, ctx, repo, "GS-EXACTDRAIN", 400)
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-EXACTDRAIN", 400, false); err != nil {
		t.Fatalf("drain GS-EXACTDRAIN to exactly zero: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-EXACTDRAIN"); v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("after exact drain: balance=%d status=%q, want 0/'redeemed'", v.BalanceMinor, v.Status)
	}
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-EXACTDRAIN", 100, true); err != nil {
		t.Fatalf("force redemption of an already-'redeemed' (exact-drain) voucher: %v, want success", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-EXACTDRAIN"); v.BalanceMinor != -100 || v.Status != "redeemed" {
		t.Fatalf("after second forced redemption: balance=%d status=%q, want -100/'redeemed'", v.BalanceMinor, v.Status)
	}

	// force still does NOT bypass 'void' — a voided voucher is a different,
	// worse problem than a balance/status race caused by replay ordering.
	vSeedVoucher(t, ctx, repo, "GS-VOIDED", 400)
	if _, err := d.DB.ExecContext(ctx, `UPDATE vouchers SET status = 'void', balance = 0 WHERE id = ?`, "GS-VOIDED"); err != nil {
		t.Fatalf("seed void GS-VOIDED: %v", err)
	}
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-VOIDED", 100, true); !errors.Is(err, ErrVoucherNotActive) {
		t.Fatalf("force on void voucher: err = %v, want ErrVoucherNotActive", err)
	}
}

// TestVoucherRepo_CreateDuplicateIDReturnsErrVoucherIDExists guards the
// second LAN-sync poison-entry trigger path (ut-docs#1127, ADR-0065): two
// tills can issue the same operator-supplied voucher code offline (vouchers.id
// is a TEXT PRIMARY KEY, not a generated id), and the second one's journal
// replay hits this PK conflict inside CompleteSale's transaction. Before this
// change, CreateVoucher just wrapped and returned the raw SQLite "UNIQUE
// constraint failed" error, giving applyJournal nothing to classify as
// permanent (vs. transient) -- same isUniqueViolation pattern already used
// for ErrPromotionCodeExists (pos_repo.go).
func TestVoucherRepo_CreateDuplicateIDReturnsErrVoucherIDExists(t *testing.T) {
	d := b8OpenDB(t, "voucher-dup-id.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	vSeedVoucher(t, ctx, repo, "GS-DUP", 1000)

	// A second, unrelated voucher issue colliding on the same operator-typed
	// code -- different amount/holder, same id.
	err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "GS-DUP", HolderLabel: "Someone Else", OriginalAmountMinor: 500,
		BalanceMinor: 500, Currency: "EUR", IssuedSaleID: "sale-other",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if !errors.Is(err, ErrVoucherIDExists) {
		t.Fatalf("duplicate voucher id: err = %v, want ErrVoucherIDExists", err)
	}

	// The original voucher is untouched by the failed second insert.
	v, gerr := repo.GetVoucherBalance(ctx, nil, "GS-DUP")
	if gerr != nil || v.HolderLabel != "Sample Holder" || v.OriginalAmountMinor != 1000 {
		t.Fatalf("original voucher after collision: %+v (err %v), want unchanged (Sample Holder/1000)", v, gerr)
	}
}

// EnsureVoucherLocalRow (ut-docs#1668): the cross-till redemption
// write-through's local-mirror step. INSERT OR IGNORE — fills a genuine gap
// (this till has never seen the voucher) but must never clobber a row this
// till already has, which is the exact hazard this card ruled out for a
// periodic primary-wins sync of this table.
func TestVoucherRepo_EnsureVoucherLocalRow_InsertsWhenMissing(t *testing.T) {
	d := b8OpenDB(t, "voucher-ensure-missing.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	if _, err := repo.GetVoucherBalance(ctx, nil, "GS-REMOTE"); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("precondition: GS-REMOTE must not exist locally yet, err = %v", err)
	}

	if err := repo.EnsureVoucherLocalRow(ctx, nil, Voucher{
		ID: "GS-REMOTE", HolderLabel: "Remote Holder", OriginalAmountMinor: 2000,
		BalanceMinor: 1200, Currency: "EUR", Status: "active",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("EnsureVoucherLocalRow: %v", err)
	}

	v, err := repo.GetVoucherBalance(ctx, nil, "GS-REMOTE")
	if err != nil {
		t.Fatalf("GetVoucherBalance after ensure: %v", err)
	}
	if v.HolderLabel != "Remote Holder" || v.OriginalAmountMinor != 2000 || v.BalanceMinor != 1200 || v.Status != "active" {
		t.Fatalf("mirrored voucher = %+v, want the pre-debit snapshot passed in", v)
	}

	// A subsequent local debit against the just-mirrored row must succeed
	// exactly as it would for a voucher this till issued itself.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-REMOTE", 500, false); err != nil {
		t.Fatalf("debit against mirrored row: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-REMOTE"); v.BalanceMinor != 700 {
		t.Fatalf("balance after debit = %d, want 700", v.BalanceMinor)
	}
}

func TestVoucherRepo_EnsureVoucherLocalRow_NeverClobbersExistingRow(t *testing.T) {
	d := b8OpenDB(t, "voucher-ensure-existing.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// This till already knows about the voucher — e.g. it issued it, or
	// redeemed against it earlier this shift — with its OWN, more recent
	// local balance.
	vSeedVoucher(t, ctx, repo, "GS-LOCAL", 1000)
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-LOCAL", 300, false); err != nil {
		t.Fatalf("seed local debit: %v", err)
	}

	// A stale/different snapshot (as if fetched from a primary that hasn't
	// seen this till's own redemption yet) must NOT overwrite it.
	if err := repo.EnsureVoucherLocalRow(ctx, nil, Voucher{
		ID: "GS-LOCAL", HolderLabel: "Someone Else", OriginalAmountMinor: 1000,
		BalanceMinor: 1000, Currency: "EUR", Status: "active",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("EnsureVoucherLocalRow: %v", err)
	}

	v, err := repo.GetVoucherBalance(ctx, nil, "GS-LOCAL")
	if err != nil {
		t.Fatalf("GetVoucherBalance: %v", err)
	}
	if v.HolderLabel != "Sample Holder" || v.BalanceMinor != 700 {
		t.Fatalf("EnsureVoucherLocalRow must never clobber an existing local row, got %+v", v)
	}
}
