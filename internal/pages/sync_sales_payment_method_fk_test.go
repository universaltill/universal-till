package pages

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// newSyncSalesRealMigrationDeps builds *common.Deps against a REAL migrated
// schema (internal/db.Open, not this package's hand-rolled openPagesTestDB/
// seedForPages fixture) -- same duplicated-helper-per-package convention
// internal/cloudsync's own openMigratedDB already uses (see that file's
// comment), chosen deliberately here rather than reusing/editing
// openPagesTestDB so this regression test doesn't collide with ut-docs#1676's
// in-flight core swap of that shared fixture. payments.method_id's real FK
// to payment_methods(id) (internal/db/migrations/001_init.sql) is exactly
// what ut-docs#1681's bug needs enforced to be reproducible at all -- the
// old hand-rolled fixture carried no such FK, which is why this gap was
// invisible before.
func newSyncSalesRealMigrationDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "sync_sales_fk.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d.DB), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
	}
	t.Cleanup(dp.WaitForAsyncWork)
	return dp
}

// TestApplyJournal_UnseededPaymentMethodDoesNotFKViolate is ut-docs#1681's
// regression test: a replica can complete a sale tendered with a payment
// method the primary's payment_methods table has no row for yet (real
// migrations seed only cash/card/gift, see 001_init.sql) -- the live tender
// path (pos_api.go) and refund path (refund_page.go) both call
// repo.EnsurePaymentMethod first to upsert one; applyJournal (the LAN-sync
// journal-replay path) did not, so this exact scenario hit a raw, uncaught
// FOREIGN KEY constraint failure instead of replaying. Reproducing this
// needs a REAL migrated schema -- the FK doesn't exist in this package's
// old hand-rolled test fixture, which is exactly why the bug was invisible
// until ut-docs#1676's core-swap work ran the full suite against real
// migrations for the first time.
func TestApplyJournal_UnseededPaymentMethodDoesNotFKViolate(t *testing.T) {
	dp := newSyncSalesRealMigrationDeps(t)
	ctx := context.Background()

	// "voucher" is deliberately NOT one of the three seeded payment_methods
	// rows (cash/card/gift) -- the exact unseeded-method scenario the card
	// describes.
	j := journalSale{Sale: data.SaleDetail{
		ID: "fk-1681-sale-1", ReceiptNo: "T9-FK-1", Status: "completed",
		SaleType: "sale", Currency: "GBP", Subtotal: 0, Total: 500,
		// "system" is one of the two real seeded users (001_init.sql) --
		// unlike the synthetic "user1"/"m1"-style ids ut-docs#1682 found
		// elsewhere, sales.cashier_id has a real FK to users(id) too, so a
		// non-seeded id here would fail for a reason unrelated to this
		// test's actual target (payments.method_id).
		CreatedAt: "2026-09-06T10:00:00Z", CashierID: "system",
		// Load-bearing, not incidental: CompleteSale allows a sale with
		// zero Lines only if it has at least one VoucherIssue (a
		// voucher-only sale is legitimate, ut-docs#1008), and the issue's
		// Amount is what makes the 500 payment below a balanced tender
		// rather than an overpayment CompleteSale would reject.
		VoucherIssues: []data.SaleDetailVoucherIssue{
			{VoucherID: "GS-FK-1681", HolderLabel: "Test", Amount: 500},
		},
		Payments: []data.SaleDetailPayment{
			{Method: "voucher", Amount: 500},
		},
	}}

	applied, quarantineReason, err := applyJournal(ctx, dp, "till-fk-1681", j)
	if err != nil {
		t.Fatalf("applyJournal must not raw-error on an unseeded payment method (FK violation): %v", err)
	}
	if quarantineReason != "" {
		t.Fatalf("expected a clean replay, not a quarantine: %q", quarantineReason)
	}
	if !applied {
		t.Fatal("expected applied=true")
	}

	repo := data.NewPOSRepo(dp.Db)
	sale, ok, err := repo.GetSaleDetailByID(ctx, "fk-1681-sale-1")
	if err != nil {
		t.Fatalf("GetSaleDetailByID: %v", err)
	}
	if !ok {
		t.Fatal("expected the replayed sale to exist")
	}
	if len(sale.Payments) != 1 || sale.Payments[0].Method != "voucher" {
		t.Fatalf("expected the replayed payment to persist with method %q, got %+v", "voucher", sale.Payments)
	}

	// EnsurePaymentMethod must actually have upserted the row, not merely
	// avoided erroring some other way.
	var count int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_methods WHERE id = ?`, "voucher").Scan(&count); err != nil {
		t.Fatalf("query payment_methods: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected applyJournal to have upserted a payment_methods row for %q, found %d", "voucher", count)
	}
}

// TestApplyJournal_EmptyPaymentMethodDoesNotPolluteLivePaymentMethods pins
// the should-fix independent review caught: EnsurePaymentMethod doesn't
// validate its id, so an unguarded call on a malformed/malicious peer's
// empty payment method would upsert a junk, nameless payment_methods row --
// which then sorts into the cashier's live tender list -- before
// CompleteSale ever gets a chance to reject the entry the way it always
// has. The guard must change nothing about that rejection, only remove the
// side effect.
func TestApplyJournal_EmptyPaymentMethodDoesNotPolluteLivePaymentMethods(t *testing.T) {
	dp := newSyncSalesRealMigrationDeps(t)
	ctx := context.Background()

	j := journalSale{Sale: data.SaleDetail{
		ID: "fk-1681-sale-2", ReceiptNo: "T9-FK-2", Status: "completed",
		SaleType: "sale", Currency: "GBP", Subtotal: 0, Total: 500,
		CreatedAt: "2026-09-06T10:00:00Z", CashierID: "system",
		VoucherIssues: []data.SaleDetailVoucherIssue{
			{VoucherID: "GS-FK-1681-2", HolderLabel: "Test", Amount: 500},
		},
		Payments: []data.SaleDetailPayment{
			{Method: "", Amount: 500},
		},
	}}

	// CompleteSale must still reject this exactly as it always has -- the
	// guard only removes the ensure's side effect, it doesn't change
	// applyJournal's own validation surface (an empty method isn't on
	// missingJournalFields'/invalidJournalFields' checklist, so this is a
	// pos.CompleteSale-level rejection, batch-reject-and-retry, same as
	// any other unclassified CompleteSale error).
	applied, quarantineReason, err := applyJournal(ctx, dp, "till-fk-1681-2", j)
	if err == nil {
		t.Fatal("expected an error rejecting the sale (missing payment method), got nil")
	}
	if applied || quarantineReason != "" {
		t.Fatalf("expected a rejected batch, not applied/quarantined: applied=%v quarantineReason=%q", applied, quarantineReason)
	}

	var count int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_methods WHERE id = ''`).Scan(&count); err != nil {
		t.Fatalf("query payment_methods: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no junk empty-id payment_methods row, found %d", count)
	}
}
