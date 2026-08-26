package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#1127 / ADR-0065: applyJournal previously rejected the WHOLE pushed
// batch on ANY error, and the replica's own push loop never advanced
// sync.push_cursor past a failed entry -- a single permanently-failing entry
// wedged that replica's entire subsequent replication forever. These tests
// pin the two confirmed-live trigger paths (ut-docs#1053) plus the
// batch/cursor-level behaviour the fix provides.

// voucherIssueJournal builds one journaled sale that issues voucherID —
// the shape a replica's buildJournal produces for a tracked voucher issue.
func voucherIssueJournal(saleID, receipt, voucherID, holder string, amount int64) journalSale {
	return journalSale{Sale: data.SaleDetail{
		ID: saleID, ReceiptNo: receipt, Status: "completed", SaleType: "sale",
		Currency: "GBP", Subtotal: 0, Total: amount,
		CreatedAt: "2026-08-26T10:00:00Z", CashierID: "user1",
		VoucherIssues: []data.SaleDetailVoucherIssue{
			{VoucherID: voucherID, HolderLabel: holder, Amount: amount},
		},
		Payments: []data.SaleDetailPayment{
			{Method: "cash", Amount: amount},
		},
	}}
}

// TestApplyJournal_QuarantinesUnknownVoucherOnRedemptionReplay covers
// ut-docs#1053 scenario 1: a pre-1.3.0 replica's voucher issue never
// journaled (the field didn't exist yet), so the primary has no vouchers
// row when the FIRST post-upgrade redemption of it replays.
// data.ErrVoucherNotFound is a hard reject even under AllowVoucherOverdraft
// -- before this fix, that error 422'd the whole batch forever; now it must
// quarantine just this entry and let the batch/cursor proceed.
func TestApplyJournal_QuarantinesUnknownVoucherOnRedemptionReplay(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	logging.ResetRecent()

	j := voucherRedemptionJournal("orphan-redeem-1", "T2-ORPHAN-1", "GS-NEVER-ISSUED", 500)
	applied, quarantineReason, err := applyJournal(ctx, dp, "till-2", j)
	if err != nil {
		t.Fatalf("unknown-voucher redemption must quarantine, not reject the batch: err = %v", err)
	}
	if applied {
		t.Fatal("expected applied=false for a quarantined entry")
	}
	if quarantineReason == "" {
		t.Fatal("expected a non-empty quarantine reason")
	}

	// No sale row was written -- the entry was skipped, not force-applied.
	repo := data.NewPOSRepo(dp.Db)
	if exists, err := repo.SaleExists(ctx, "orphan-redeem-1"); err != nil || exists {
		t.Fatalf("quarantined entry must not create a sale row: exists=%v err=%v", exists, err)
	}

	// Operator-visible Problem naming the receipt and the reason.
	if n := recentMatches("T2-ORPHAN-1", "till-2"); n != 1 {
		t.Fatalf("expected exactly one Problem naming T2-ORPHAN-1/till-2, got %d\nrecent: %+v", n, logging.Recent())
	}

	// Durable, queryable record.
	entries, err := repo.ListJournalQuarantine(ctx, 10)
	if err != nil {
		t.Fatalf("ListJournalQuarantine: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("quarantine entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].SaleID != "orphan-redeem-1" || entries[0].ReceiptNo != "T2-ORPHAN-1" || entries[0].TillID != "till-2" {
		t.Fatalf("quarantine entry = %+v", entries[0])
	}
}

// TestApplyJournal_QuarantinesCollidingVoucherIDOnIssueReplay covers
// ut-docs#1053 scenario 2: two tills issue the same operator-supplied
// voucher code offline (vouchers.id is a TEXT PRIMARY KEY); the second
// entry to replay collides inside CompleteSale's transaction, which rolls
// the whole entry back with data.ErrVoucherIDExists.
func TestApplyJournal_QuarantinesCollidingVoucherIDOnIssueReplay(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	// First till's issue replays cleanly.
	j1 := voucherIssueJournal("issue-first-1", "T2-COLLIDE-1", "GS-COLLIDE", "Alice", 1000)
	applied, reason, err := applyJournal(ctx, dp, "till-2", j1)
	if err != nil || !applied || reason != "" {
		t.Fatalf("first issue: applied=%v reason=%q err=%v, want applied", applied, reason, err)
	}

	// Second till independently issued the SAME code offline.
	logging.ResetRecent()
	j2 := voucherIssueJournal("issue-second-1", "T3-COLLIDE-1", "GS-COLLIDE", "Bob", 750)
	applied, quarantineReason, err := applyJournal(ctx, dp, "till-3", j2)
	if err != nil {
		t.Fatalf("colliding voucher id must quarantine, not reject the batch: err = %v", err)
	}
	if applied {
		t.Fatal("expected applied=false for the colliding entry")
	}
	if quarantineReason == "" {
		t.Fatal("expected a non-empty quarantine reason")
	}

	repo := data.NewPOSRepo(dp.Db)
	if exists, err := repo.SaleExists(ctx, "issue-second-1"); err != nil || exists {
		t.Fatalf("quarantined entry must not create a sale row: exists=%v err=%v", exists, err)
	}
	// The FIRST voucher (Alice's, 1000) is untouched by the collision.
	v, err := repo.GetVoucherBalance(ctx, "GS-COLLIDE")
	if err != nil || v.HolderLabel != "Alice" || v.OriginalAmountMinor != 1000 {
		t.Fatalf("original voucher after collision: %+v (err %v), want unchanged (Alice/1000)", v, err)
	}
	if n := recentMatches("T3-COLLIDE-1", "till-3"); n != 1 {
		t.Fatalf("expected exactly one Problem naming T3-COLLIDE-1/till-3, got %d\nrecent: %+v", n, logging.Recent())
	}
}

// TestApplyJournal_UnrecognisedFailureStillRejectsBatch guards the
// deliberately-conservative half of ADR-0065: an error NOT on the small
// permanent-failure allowlist must keep today's behaviour exactly (return an
// error, no quarantine record, no Problem) so a genuine bug or a version-skew
// condition still surfaces loudly rather than being silently skipped.
func TestApplyJournal_UnrecognisedFailureStillRejectsBatch(t *testing.T) {
	if reason := permanentJournalFailureReason(nil); reason != "" {
		t.Fatalf("nil error must not classify as permanent, got %q", reason)
	}
	if reason := permanentJournalFailureReason(context.DeadlineExceeded); reason != "" {
		t.Fatalf("an unrecognised error must not classify as permanent, got %q", reason)
	}
	if reason := permanentJournalFailureReason(data.ErrVoucherNotFound); reason == "" {
		t.Fatal("ErrVoucherNotFound must classify as permanent")
	}
	if reason := permanentJournalFailureReason(data.ErrVoucherIDExists); reason == "" {
		t.Fatal("ErrVoucherIDExists must classify as permanent")
	}
}

// TestRegisterSyncSales_QuarantinedEntryDoesNotBlockRestOfBatch is the
// end-to-end proof at the HTTP handler level: a batch carrying one poison
// entry (unknown voucher redemption) ALONGSIDE a normal, well-formed sale
// must return 200 with the good entry applied and the bad one quarantined —
// not a 422 that wedges the whole batch (and, via syncPushTick, the
// replica's cursor) forever.
func TestRegisterSyncSales_QuarantinedEntryDoesNotBlockRestOfBatch(t *testing.T) {
	mux, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)

	tills := data.NewTillsRepo(dp.Db)
	if _, err := tills.InsertTill(ctx, "Quarantine Test Till", hashBearer("token-quarantine")); err != nil {
		t.Fatalf("enroll till: %v", err)
	}

	good := seedJournalSale("batch-good-1", "T2-BATCH-GOOD", "sale", "", "itm1", 1, 100)
	poison := voucherRedemptionJournal("batch-poison-1", "T2-BATCH-POISON", "GS-NOPE", 500)
	batch := []journalSale{good, poison}
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sync/sales", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token-quarantine")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (poison entry quarantined, not batch-rejecting), got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Applied     int `json:"applied"`
			Skipped     int `json:"skipped"`
			Quarantined int `json:"quarantined"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	if out.Data.Applied != 1 || out.Data.Quarantined != 1 || out.Data.Skipped != 0 {
		t.Fatalf("response = %+v, want applied=1 quarantined=1 skipped=0", out.Data)
	}

	if exists, err := repo.SaleExists(ctx, "batch-good-1"); err != nil || !exists {
		t.Fatalf("the good entry must still have applied: exists=%v err=%v", exists, err)
	}
	if exists, err := repo.SaleExists(ctx, "batch-poison-1"); err != nil || exists {
		t.Fatalf("the poison entry must not have applied: exists=%v err=%v", exists, err)
	}
	entries, err := repo.ListJournalQuarantine(ctx, 10)
	if err != nil || len(entries) != 1 || entries[0].SaleID != "batch-poison-1" {
		t.Fatalf("quarantine record = %+v (err %v), want exactly one for batch-poison-1", entries, err)
	}
}

// TestSyncPushTick_QuarantinedEntryAdvancesCursor is the one thing none of
// the tests above actually pin: the whole point of ut-docs#1127/ADR-0065 is
// that the REPLICA's own push loop (syncPushTick) advances sync.push_cursor
// past a quarantined entry instead of re-submitting it forever. Every other
// test in this file stops at the primary's HTTP response; this one drives a
// real replica->primary push (same shape as
// TestSyncPushTick_PushesLocalSalesAndAdvancesCursor /
// TestSyncPushTick_RejectedResponse_CursorNotAdvanced in sync_sales_test.go)
// so a future change that makes syncPushTick retry on quarantined>0 would
// fail THIS test even though every applyJournal-level test above stays
// green -- independent review, 2026-08-26.
func TestSyncPushTick_QuarantinedEntryAdvancesCursor(t *testing.T) {
	primaryMux, primaryDp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	if _, err := data.NewTillsRepo(primaryDp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	server := httptest.NewServer(primaryMux)
	t.Cleanup(server.Close)

	_, replicaDp := newSyncSalesTestDeps(t)
	// A local sale paid entirely by redeeming a voucher the primary has
	// never heard of -- buildJournal's GetSaleDetail picks up the
	// payments.voucher_id column (ut-docs#1053, migration 072), so this
	// reproduces the real shape a replica pushes, not a hand-built journal
	// bypassing buildJournal.
	if _, err := replicaDp.Db.ExecContext(ctx, `
INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('push-poison-1', 'R-PUSH-POISON', 'completed', 'sale', 'GBP', 500, 0, 0, 500, '2026-01-01T10:00:00Z', '2026-01-01T10:00:00Z')`); err != nil {
		t.Fatalf("seed replica sale: %v", err)
	}
	if _, err := replicaDp.Db.ExecContext(ctx, `
INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('push-poison-1-line', 'push-poison-1', 1, 'itm1', 'Apple', 'ABC', 5, 100, 0, 0, 500, 500)`); err != nil {
		t.Fatalf("seed replica sale line: %v", err)
	}
	if _, err := replicaDp.Db.ExecContext(ctx, `
INSERT INTO payments(id, sale_id, method_id, amount, currency, paid_at, voucher_id)
VALUES('push-poison-1-pay', 'push-poison-1', 'voucher', 500, 'GBP', '2026-01-01T10:00:00Z', 'GS-NEVER-ISSUED')`); err != nil {
		t.Fatalf("seed replica payment: %v", err)
	}
	if err := replicaDp.Settings.Set(ctx, "sync.primary_url", server.URL); err != nil {
		t.Fatal(err)
	}
	if err := replicaDp.Settings.Set(ctx, "sync.bearer", "token-abc"); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	syncPushTick(ctx, replicaDp, client)

	// The core claim: the cursor advanced PAST the poison entry, exactly as
	// it would for a cleanly-applied sale -- this is what actually unwedges
	// the replica's subsequent replication, not just "the primary returned
	// 200" (which a client-side bug could ignore).
	cursor, _, _ := replicaDp.Settings.Get(ctx, "sync.push_cursor")
	if cursor != "2026-01-01T10:00:00Z" {
		t.Fatalf("expected sync.push_cursor to advance past the quarantined entry, got %q", cursor)
	}

	// The primary never applied it (no sale row)...
	var count int
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE id = 'push-poison-1'`).Scan(&count); err != nil {
		t.Fatalf("query primary sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the poison entry NOT applied on the primary, got count=%d", count)
	}
	// ...but DID record it as quarantined, durably.
	entries, err := data.NewPOSRepo(primaryDp.Db).ListJournalQuarantine(ctx, 10)
	if err != nil || len(entries) != 1 || entries[0].SaleID != "push-poison-1" {
		t.Fatalf("quarantine record = %+v (err %v), want exactly one for push-poison-1", entries, err)
	}
}
