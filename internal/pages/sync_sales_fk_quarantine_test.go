package pages

import (
	"context"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// TestApplyJournal_UnknownCashierIDIsQuarantined is ut-docs#1686's first
// regression case: a replica's own operator user id (never seeded on the
// primary -- 001_init.sql seeds only "system"/"kiosk") replaying a sale
// hits sales.cashier_id's real FK to users(id). Before this card, that was
// a raw, uncaught FOREIGN KEY violation (batch-reject, wedging the
// replica's cursor forever, ut-docs#1127/ADR-0065's whole reason for
// existing) -- it must now be quarantined with a diagnosable reason
// instead, same shape as the two voucher cases ut-docs#1681 sits next to.
func TestApplyJournal_UnknownCashierIDIsQuarantined(t *testing.T) {
	dp := newSyncSalesRealMigrationDeps(t)
	ctx := context.Background()

	// Voucher-only sale (no lines) so this test isolates the cashier FK --
	// same pattern TestApplyJournal_UnseededPaymentMethodDoesNotFKViolate
	// already uses to avoid touching sale_lines at all. "cash" is one of
	// the three real seeded payment_methods rows (001_init.sql).
	j := journalSale{Sale: data.SaleDetail{
		ID: "fk-1686-sale-1", ReceiptNo: "T9-FK-3", Status: "completed",
		SaleType: "sale", Currency: "GBP", Subtotal: 0, Total: 500,
		CreatedAt: "2026-09-07T10:00:00Z",
		// Never seeded on the primary -- the exact scenario the card
		// describes (a replica's own operator user completing a sale
		// offline, then replaying to a primary that has never heard of
		// that user id).
		CashierID: "replica-only-user-42",
		VoucherIssues: []data.SaleDetailVoucherIssue{
			{VoucherID: "GS-FK-1686-1", HolderLabel: "Test", Amount: 500},
		},
		Payments: []data.SaleDetailPayment{
			{Method: "cash", Amount: 500},
		},
	}}

	applied, quarantineReason, err := applyJournal(ctx, dp, "till-fk-1686-1", j)
	if err != nil {
		t.Fatalf("applyJournal must not raw-error on an unknown cashier id (FK violation): %v", err)
	}
	if applied {
		t.Fatal("expected the entry to be quarantined, not applied")
	}
	if quarantineReason == "" {
		t.Fatal("expected a non-empty quarantine reason")
	}
	const want = "unknown cashier/customer/register/table id on replica-local sale"
	if quarantineReason != want {
		t.Fatalf("quarantineReason = %q, want %q", quarantineReason, want)
	}

	// No sale row must have been written -- a quarantined entry never
	// applies (same contract as the two existing voucher cases).
	repo := data.NewPOSRepo(dp.Db)
	if exists, err := repo.SaleExists(ctx, "fk-1686-sale-1"); err != nil {
		t.Fatalf("SaleExists: %v", err)
	} else if exists {
		t.Fatal("expected no sale row for a quarantined entry")
	}

	// The durable quarantine record must exist with the same reason.
	entries, err := repo.ListJournalQuarantine(ctx, 10)
	if err != nil {
		t.Fatalf("ListJournalQuarantine: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.SaleID == "fk-1686-sale-1" {
			found = true
			if e.Reason != want {
				t.Fatalf("quarantine record reason = %q, want %q", e.Reason, want)
			}
		}
	}
	if !found {
		t.Fatal("expected a sync_journal_quarantine row for the quarantined sale")
	}
}

// TestApplyJournal_UnknownItemIDIsQuarantined is ut-docs#1686's second
// regression case: a sale line referencing an item the primary doesn't
// have a row for yet (a catalog that hasn't fully synced, or an item
// created on a replica while offline) hits sale_lines.item_id's real FK to
// items(id). Same fix, same allowlist branch, different insert stage.
func TestApplyJournal_UnknownItemIDIsQuarantined(t *testing.T) {
	dp := newSyncSalesRealMigrationDeps(t)
	ctx := context.Background()

	j := journalSale{Sale: data.SaleDetail{
		ID: "fk-1686-sale-2", ReceiptNo: "T9-FK-4", Status: "completed",
		SaleType: "sale", Currency: "GBP", Subtotal: 500, Total: 500,
		CreatedAt: "2026-09-07T10:00:00Z",
		// "system" is one of the two real seeded users -- isolates this
		// test to the item FK, same reasoning as the sibling #1681 test's
		// comment on CashierID.
		CashierID: "system",
		Lines: []data.SaleDetailLine{
			{
				Name: "Replica-only item", SKU: "RO-1",
				// Never seeded on the primary -- a replica-created item
				// (or a catalog sync lag) whose id the primary has no
				// items row for yet.
				ItemID: "replica-only-item-99",
				Qty:    1, UnitPrice: 500,
			},
		},
		Payments: []data.SaleDetailPayment{
			{Method: "cash", Amount: 500},
		},
	}}

	applied, quarantineReason, err := applyJournal(ctx, dp, "till-fk-1686-2", j)
	if err != nil {
		t.Fatalf("applyJournal must not raw-error on an unknown item id (FK violation): %v", err)
	}
	if applied {
		t.Fatal("expected the entry to be quarantined, not applied")
	}
	const want = "unknown item/variant id on replica-local sale line"
	if quarantineReason != want {
		t.Fatalf("quarantineReason = %q, want %q", quarantineReason, want)
	}

	repo := data.NewPOSRepo(dp.Db)
	if exists, err := repo.SaleExists(ctx, "fk-1686-sale-2"); err != nil {
		t.Fatalf("SaleExists: %v", err)
	} else if exists {
		t.Fatal("expected no sale row for a quarantined entry")
	}

	// Same durable-record check as the cashier test above -- both cases go
	// through the identical quarantineJournalEntry call, but pinning both
	// (not just one) catches a regression that breaks only one insert
	// stage's path there.
	entries, err := repo.ListJournalQuarantine(ctx, 10)
	if err != nil {
		t.Fatalf("ListJournalQuarantine: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.SaleID == "fk-1686-sale-2" {
			found = true
			if e.Reason != want {
				t.Fatalf("quarantine record reason = %q, want %q", e.Reason, want)
			}
		}
	}
	if !found {
		t.Fatal("expected a sync_journal_quarantine row for the quarantined sale")
	}
}

// TestPermanentJournalFailureReason_AllReasonsHaveALocaleKey pins the exact
// gap an independent review caught (ut-docs#1686): sync_quarantine_page.go's
// quarantineReasonKeys map translates the Reason column on /sync-quarantine,
// and its own doc comment warns that a reason permanentJournalFailureReason
// can return without a matching map entry renders as raw, untranslated
// English there -- guard-i18n.sh cannot catch this (the string is a DB
// value at that point, not a template literal). This test enumerates every
// error shape permanentJournalFailureReason recognises and fails loudly if
// any of its non-empty results has no locale-key entry, so a future case
// added to one without the other breaks the build instead of shipping a
// silent i18n gap a fourth time.
func TestPermanentJournalFailureReason_AllReasonsHaveALocaleKey(t *testing.T) {
	cases := []error{
		data.ErrVoucherNotFound,
		data.ErrVoucherIDExists,
		fmt.Errorf("insert sale: constraint failed: FOREIGN KEY constraint failed (787)"),
		fmt.Errorf("insert sale lines batch: constraint failed: FOREIGN KEY constraint failed (787)"),
		// Any other insert stage's FK violation -- the foreignKeyViolationReason
		// default branch, e.g. a payments.method_id race outside EnsurePaymentMethod's
		// window.
		fmt.Errorf("insert payment: constraint failed: FOREIGN KEY constraint failed (787)"),
	}
	for _, err := range cases {
		reason := permanentJournalFailureReason(err)
		if reason == "" {
			t.Fatalf("permanentJournalFailureReason(%v) = \"\", want a non-empty reason", err)
		}
		if _, ok := quarantineReasonKeys[reason]; !ok {
			t.Errorf("permanentJournalFailureReason(%v) = %q has no entry in quarantineReasonKeys (sync_quarantine_page.go) -- it will render as raw, untranslated English on /sync-quarantine", err, reason)
		}
	}
}
