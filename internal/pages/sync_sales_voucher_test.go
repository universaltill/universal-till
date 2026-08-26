package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#1053: a voucher issued or redeemed on one till must replicate to
// peers over LAN sync. Before this card, applyJournal never reconstructed
// VoucherIssues and SaleDetailPayment had no voucher_id, so a replayed
// voucher sale landed on the primary with total missing the face value,
// voucher_issue_total = 0, and no vouchers/voucher_transactions rows.

// TestApplyJournal_ReplicatesVoucherIssueAndRedemption is the real
// producer→consumer round trip: real sales on a replica (pos.CompleteSale),
// journaled via buildJournal, replayed via applyJournal on a fresh primary —
// the resulting vouchers/voucher_transactions rows and sale totals must
// match what was journaled.
func TestApplyJournal_ReplicatesVoucherIssueAndRedemption(t *testing.T) {
	_, replicaDp := newSyncSalesTestDeps(t)
	_, primaryDp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	replicaRepo := data.NewPOSRepo(replicaDp.Db)

	locID, err := replicaRepo.EnsureStockLocation(ctx)
	if err != nil {
		t.Fatalf("ensure location: %v", err)
	}

	// Sale 1 on the replica: issues voucher GS-LAN-A (15.00), paid cash.
	if _, err := pos.CompleteSale(ctx, replicaDp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: "lan-v-issue-1", ReceiptNo: "T2-V001",
		Currency: "GBP", TaxInclusive: true, CashierID: "user1",
		AllowNegativeInventory: true,
		VoucherIssues: []pos.VoucherIssueInput{{
			VoucherID: "GS-LAN-A", HolderLabel: "Sample Holder", Amount: money.FromMinor(1500),
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1500)}},
	}); err != nil {
		t.Fatalf("replica issue sale: %v", err)
	}

	// Sale 2 on the replica: an article line, ANOTHER voucher issued
	// (GS-LAN-B, 5.00) and part-payment by redeeming GS-LAN-A — both flows
	// in one journaled sale.
	if _, err := pos.CompleteSale(ctx, replicaDp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: "lan-v-mixed-1", ReceiptNo: "T2-V002",
		Currency: "GBP", TaxInclusive: true, CashierID: "user1",
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm1", SKU: "ABC", Name: "Apple", Qty: 1,
			UnitPrice: money.FromMinor(100), TaxRateBasisPoints: 2000, LocationID: locID,
		}},
		VoucherIssues: []pos.VoucherIssueInput{{VoucherID: "GS-LAN-B", Amount: money.FromMinor(500)}},
		Payments: []pos.PaymentInput{
			{MethodID: "voucher", VoucherID: "GS-LAN-A", Amount: money.FromMinor(100)},
			{MethodID: "cash", Amount: money.FromMinor(500)},
		},
	}); err != nil {
		t.Fatalf("replica mixed sale: %v", err)
	}

	j1, found, err := buildJournal(ctx, replicaRepo, "T2-V001")
	if err != nil || !found {
		t.Fatalf("buildJournal T2-V001: found=%v err=%v", found, err)
	}
	j2, found, err := buildJournal(ctx, replicaRepo, "T2-V002")
	if err != nil || !found {
		t.Fatalf("buildJournal T2-V002: found=%v err=%v", found, err)
	}

	// The journal payload itself must carry the voucher flows.
	if len(j1.Sale.VoucherIssues) != 1 || j1.Sale.VoucherIssues[0].VoucherID != "GS-LAN-A" ||
		j1.Sale.VoucherIssues[0].HolderLabel != "Sample Holder" || j1.Sale.VoucherIssues[0].Amount != 1500 {
		t.Fatalf("journal 1 voucher issues = %+v, want GS-LAN-A/'Sample Holder'/1500", j1.Sale.VoucherIssues)
	}
	if len(j2.Sale.VoucherIssues) != 1 || j2.Sale.VoucherIssues[0].VoucherID != "GS-LAN-B" || j2.Sale.VoucherIssues[0].Amount != 500 {
		t.Fatalf("journal 2 voucher issues = %+v, want GS-LAN-B/500", j2.Sale.VoucherIssues)
	}
	var redeemPayment *data.SaleDetailPayment
	for i := range j2.Sale.Payments {
		if j2.Sale.Payments[i].Method == "voucher" {
			redeemPayment = &j2.Sale.Payments[i]
		}
	}
	if redeemPayment == nil || redeemPayment.VoucherID != "GS-LAN-A" {
		t.Fatalf("journal 2 voucher payment = %+v, want VoucherID GS-LAN-A", redeemPayment)
	}

	// Replay both, in order, on the primary.
	for _, j := range []journalSale{j1, j2} {
		applied, _, err := applyJournal(ctx, primaryDp, "till-2", j)
		if err != nil {
			t.Fatalf("applyJournal %s: %v", j.Sale.ReceiptNo, err)
		}
		if !applied {
			t.Fatalf("applyJournal %s: expected applied", j.Sale.ReceiptNo)
		}
	}

	// The primary now holds the same voucher liability the replica does.
	var balance, original int64
	var holder, status string
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT balance, original_amount, COALESCE(holder_label,''), status FROM vouchers WHERE id = 'GS-LAN-A'`).
		Scan(&balance, &original, &holder, &status); err != nil {
		t.Fatalf("primary voucher GS-LAN-A missing: %v", err)
	}
	if balance != 1400 || original != 1500 || holder != "Sample Holder" || status != "active" {
		t.Fatalf("GS-LAN-A on primary: balance=%d original=%d holder=%q status=%q, want 1400/1500/'Sample Holder'/'active'", balance, original, holder, status)
	}
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT balance FROM vouchers WHERE id = 'GS-LAN-B'`).Scan(&balance); err != nil {
		t.Fatalf("primary voucher GS-LAN-B missing: %v", err)
	}
	if balance != 500 {
		t.Fatalf("GS-LAN-B on primary: balance=%d, want 500", balance)
	}

	var issueCount, redemptionCount int
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM voucher_transactions WHERE type='issue'`).Scan(&issueCount); err != nil {
		t.Fatal(err)
	}
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM voucher_transactions WHERE type='redemption' AND voucher_id='GS-LAN-A' AND sale_id='lan-v-mixed-1' AND amount=100`).Scan(&redemptionCount); err != nil {
		t.Fatal(err)
	}
	if issueCount != 2 || redemptionCount != 1 {
		t.Fatalf("primary voucher_transactions: issues=%d redemption(GS-LAN-A/lan-v-mixed-1/100)=%d, want 2/1", issueCount, redemptionCount)
	}

	// …and the payment row keeps its voucher_id (cross-till reconciliation
	// happens on the primary, same reasoning as the card-present fields).
	var payVoucherID string
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT COALESCE(voucher_id,'') FROM payments WHERE sale_id='lan-v-mixed-1' AND method_id='voucher'`).Scan(&payVoucherID); err != nil {
		t.Fatalf("primary voucher payment row: %v", err)
	}
	if payVoucherID != "GS-LAN-A" {
		t.Fatalf("primary payments.voucher_id = %q, want GS-LAN-A", payVoucherID)
	}

	// Sale totals replicate exactly: total includes the issued face value,
	// voucher_issue_total carries it (the pre-fix bug journaled total short
	// by the face value and voucher_issue_total = 0).
	for _, saleID := range []string{"lan-v-issue-1", "lan-v-mixed-1"} {
		var rTotal, rVit, pTotal, pVit int64
		if err := replicaDp.Db.QueryRowContext(ctx, `SELECT total, voucher_issue_total FROM sales WHERE id = ?`, saleID).Scan(&rTotal, &rVit); err != nil {
			t.Fatalf("replica sale %s: %v", saleID, err)
		}
		if err := primaryDp.Db.QueryRowContext(ctx, `SELECT total, voucher_issue_total FROM sales WHERE id = ?`, saleID).Scan(&pTotal, &pVit); err != nil {
			t.Fatalf("primary sale %s: %v", saleID, err)
		}
		if rTotal != pTotal || rVit != pVit {
			t.Fatalf("sale %s totals drifted across replay: replica total=%d vit=%d, primary total=%d vit=%d", saleID, rTotal, rVit, pTotal, pVit)
		}
	}
}

// voucherRedemptionJournal builds one journaled sale paying for qty Apples
// entirely by redeeming voucherID — the shape a replica's buildJournal
// produces for a tracked redemption sale.
func voucherRedemptionJournal(saleID, receipt, voucherID string, amount int64) journalSale {
	return journalSale{Sale: data.SaleDetail{
		ID: saleID, ReceiptNo: receipt, Status: "completed", SaleType: "sale",
		Currency: "GBP", Subtotal: amount, Total: amount,
		CreatedAt: "2026-08-26T10:00:00Z", CashierID: "user1",
		Lines: []data.SaleDetailLine{
			{Name: "Apple", SKU: "ABC", ItemID: "itm1", UnitPrice: 100, Qty: float64(amount) / 100, LineTotal: amount},
		},
		Payments: []data.SaleDetailPayment{
			{Method: "voucher", Amount: amount, VoucherID: voucherID},
		},
	}}
}

// TestApplyJournal_DoubleRedemptionRaceForceAppliesAndSurfacesProblem: two
// replicas each redeemed the SAME multi-purpose voucher offline before
// either synced. The second journal entry to reach the primary finds the
// balance already below its redemption amount — it must force-apply (the
// money already moved at the till), leave the balance negative, and surface
// a Problems-panel warning, rather than 422-ing the batch as a poison entry
// forever. Same precedent as AllowNegativeInventory/warnIfStockNegative.
func TestApplyJournal_DoubleRedemptionRaceForceAppliesAndSurfacesProblem(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	// The voucher's issue has already replicated: 10.00 face value.
	if _, err := pos.CompleteSale(ctx, dp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: "race-issue-1", ReceiptNo: "T1-RACE-0",
		Currency: "GBP", TaxInclusive: true, AllowNegativeInventory: true,
		VoucherIssues: []pos.VoucherIssueInput{{VoucherID: "GS-RACE", Amount: money.FromMinor(1000)}},
		Payments:      []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	}); err != nil {
		t.Fatalf("issue sale: %v", err)
	}

	// First replica's redemption replays cleanly: 8.00 of 10.00.
	logging.ResetRecent()
	applied, _, err := applyJournal(ctx, dp, "till-2", voucherRedemptionJournal("race-redeem-1", "T2-RACE-1", "GS-RACE", 800))
	if err != nil || !applied {
		t.Fatalf("first redemption replay: applied=%v err=%v", applied, err)
	}
	var balance int64
	if err := dp.Db.QueryRowContext(ctx, `SELECT balance FROM vouchers WHERE id='GS-RACE'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 200 {
		t.Fatalf("balance after first replay = %d, want 200", balance)
	}
	// An in-balance redemption is not a Problem.
	if n := recentMatches("GS-RACE"); n != 0 {
		t.Fatalf("first (covered) replay logged %d voucher Problems, want 0\nrecent: %+v", n, logging.Recent())
	}

	// Second replica's racing redemption: another 8.00 against the
	// remaining 2.00. Must force-apply, not reject the batch.
	applied, _, err = applyJournal(ctx, dp, "till-3", voucherRedemptionJournal("race-redeem-2", "T3-RACE-1", "GS-RACE", 800))
	if err != nil {
		t.Fatalf("second (racing) redemption replay must force-apply, got err=%v", err)
	}
	if !applied {
		t.Fatal("second (racing) redemption replay: expected applied")
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT balance FROM vouchers WHERE id='GS-RACE'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != -600 {
		t.Fatalf("balance after forced replay = %d, want -600", balance)
	}
	// The overdraft surfaces as a back-office Problem naming the voucher,
	// the source sale and the till (logging.Recent() feeds the Problems
	// panel, same plumbing as warnIfStockNegative).
	if n := recentMatches("GS-RACE", "T3-RACE-1", "till-3"); n != 1 {
		t.Fatalf("expected exactly one Problem naming GS-RACE for T3-RACE-1 from till-3, got %d\nrecent: %+v", n, logging.Recent())
	}
	// The sale itself committed — the journal batch is not poisoned.
	var count int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE id='race-redeem-2'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("racing redemption sale rows = %d, want 1", count)
	}
}

// TestApplyJournal_ExactDrainThenRacingRedemptionForceApplies covers the
// review-found blocker on ut-docs#1053's first implementation: a voucher
// whose FIRST replayed redemption drains it to EXACTLY zero flips its status
// to 'redeemed', not just its balance — and a pre-read gate that only
// widened the balance check under force (leaving status != 'active' still a
// hard reject) would hit ErrVoucherNotActive on the very next racing
// redemption, poisoning that replica's journal forever (registerSyncSales
// returns 422 for the whole batch, so the cursor never advances and every
// subsequent sale replays the same poisoned entry). This is the single most
// likely double-spend shape in practice — a customer spending the WHOLE face
// value of a voucher at two tills before either synced — so it must
// force-apply exactly like the partial-drain race covered above.
func TestApplyJournal_ExactDrainThenRacingRedemptionForceApplies(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	if _, err := pos.CompleteSale(ctx, dp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: "drain-issue-1", ReceiptNo: "T1-DRAIN-0",
		Currency: "GBP", TaxInclusive: true, AllowNegativeInventory: true,
		VoucherIssues: []pos.VoucherIssueInput{{VoucherID: "GS-DRAIN", Amount: money.FromMinor(1000)}},
		Payments:      []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	}); err != nil {
		t.Fatalf("issue sale: %v", err)
	}

	// First replica's redemption replays the FULL 10.00 — balance lands on
	// exactly zero, which the repo layer flips to status='redeemed'.
	applied, _, err := applyJournal(ctx, dp, "till-2", voucherRedemptionJournal("drain-redeem-1", "T2-DRAIN-1", "GS-DRAIN", 1000))
	if err != nil || !applied {
		t.Fatalf("first (exact-drain) redemption replay: applied=%v err=%v", applied, err)
	}
	var balance int64
	var status string
	if err := dp.Db.QueryRowContext(ctx, `SELECT balance, status FROM vouchers WHERE id='GS-DRAIN'`).Scan(&balance, &status); err != nil {
		t.Fatal(err)
	}
	if balance != 0 || status != "redeemed" {
		t.Fatalf("after exact-drain replay: balance=%d status=%q, want 0/'redeemed'", balance, status)
	}

	// Second replica's racing redemption against the now-'redeemed' voucher
	// must still force-apply, not hard-reject with ErrVoucherNotActive.
	applied, _, err = applyJournal(ctx, dp, "till-3", voucherRedemptionJournal("drain-redeem-2", "T3-DRAIN-1", "GS-DRAIN", 500))
	if err != nil {
		t.Fatalf("second (racing) redemption of an exact-drained voucher must force-apply, got err=%v", err)
	}
	if !applied {
		t.Fatal("second (racing) redemption of an exact-drained voucher: expected applied")
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT balance, status FROM vouchers WHERE id='GS-DRAIN'`).Scan(&balance, &status); err != nil {
		t.Fatal(err)
	}
	if balance != -500 || status != "redeemed" {
		t.Fatalf("after second forced replay: balance=%d status=%q, want -500/'redeemed'", balance, status)
	}
	if n := recentMatches("GS-DRAIN", "T3-DRAIN-1", "till-3"); n != 1 {
		t.Fatalf("expected exactly one Problem naming GS-DRAIN for T3-DRAIN-1 from till-3, got %d\nrecent: %+v", n, logging.Recent())
	}
	// The journal is not poisoned: the sale committed and the cursor logic
	// (StartSyncPush) would advance past this entry.
	var count int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE id='drain-redeem-2'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("racing redemption sale rows = %d, want 1", count)
	}
}
