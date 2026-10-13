package data

import (
	"context"
	"errors"
	"testing"
)

// Gift vouchers page (ut-docs#4251): the read side behind GET /vouchers,
// GET /vouchers/{id} and GET /api/vouchers/export.

func vlSeed(t *testing.T, ctx context.Context, repo *POSRepo, v Voucher) {
	t.Helper()
	if v.BalanceMinor == 0 && v.Status == "" {
		v.BalanceMinor = v.OriginalAmountMinor
	}
	if v.Currency == "" {
		v.Currency = "EUR"
	}
	if err := repo.CreateVoucher(ctx, nil, v); err != nil {
		t.Fatalf("CreateVoucher %s: %v", v.ID, err)
	}
}

func vlIDs(rows []VoucherListRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func vlEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListVouchers_NewestFirstSearchStatusLimit(t *testing.T) {
	d := b8OpenDB(t, "voucher-list.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('sale-1','R-0001',2500,2500)`); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	vlSeed(t, ctx, repo, Voucher{ID: "GS-100", HolderLabel: "Alice Example", OriginalAmountMinor: 2500, IssuedSaleID: "sale-1", CreatedAt: "2026-10-01T10:00:00Z"})
	vlSeed(t, ctx, repo, Voucher{ID: "GS-200", HolderLabel: "Bob", OriginalAmountMinor: 1000, CreatedAt: "2026-10-02T10:00:00Z"})
	vlSeed(t, ctx, repo, Voucher{ID: "50%_OFF", HolderLabel: "", OriginalAmountMinor: 500, BalanceMinor: 0, Status: "redeemed", CreatedAt: "2026-10-03T10:00:00Z"})
	vlSeed(t, ctx, repo, Voucher{ID: "GS-300", HolderLabel: "Carol", OriginalAmountMinor: 700, BalanceMinor: 0, Status: "void", Purpose: VoucherPurposeSingle, CreatedAt: "2026-10-04T10:00:00Z"})

	all, err := repo.ListVouchers(ctx, VoucherFilter{})
	if err != nil {
		t.Fatalf("ListVouchers: %v", err)
	}
	if got, want := vlIDs(all), []string{"GS-300", "50%_OFF", "GS-200", "GS-100"}; !vlEqual(got, want) {
		t.Fatalf("newest first: got %v want %v", got, want)
	}
	last := all[3]
	if last.IssuedReceiptNo != "R-0001" || last.IssuedSaleID != "sale-1" || last.HolderLabel != "Alice Example" ||
		last.OriginalAmountMinor != 2500 || last.BalanceMinor != 2500 || last.Status != "active" || last.Purpose != VoucherPurposeMulti {
		t.Fatalf("row fields: %+v", last)
	}
	if all[0].Purpose != VoucherPurposeSingle || all[0].IssuedReceiptNo != "" {
		t.Fatalf("single-purpose row: %+v", all[0])
	}

	// Case-insensitive substring on number or holder.
	got, err := repo.ListVouchers(ctx, VoucherFilter{Query: "alice"})
	if err != nil || !vlEqual(vlIDs(got), []string{"GS-100"}) {
		t.Fatalf("holder search: %v %v", vlIDs(got), err)
	}
	got, _ = repo.ListVouchers(ctx, VoucherFilter{Query: "gs-2"})
	if !vlEqual(vlIDs(got), []string{"GS-200"}) {
		t.Fatalf("code search: %v", vlIDs(got))
	}
	// LIKE wildcards in the term match themselves only.
	got, _ = repo.ListVouchers(ctx, VoucherFilter{Query: "%"})
	if !vlEqual(vlIDs(got), []string{"50%_OFF"}) {
		t.Fatalf("%% must be literal: %v", vlIDs(got))
	}
	got, _ = repo.ListVouchers(ctx, VoucherFilter{Query: "_"})
	if !vlEqual(vlIDs(got), []string{"50%_OFF"}) {
		t.Fatalf("_ must be literal: %v", vlIDs(got))
	}

	got, _ = repo.ListVouchers(ctx, VoucherFilter{Status: "active"})
	if !vlEqual(vlIDs(got), []string{"GS-200", "GS-100"}) {
		t.Fatalf("status filter: %v", vlIDs(got))
	}
	got, _ = repo.ListVouchers(ctx, VoucherFilter{Status: "redeemed", Query: "off"})
	if !vlEqual(vlIDs(got), []string{"50%_OFF"}) {
		t.Fatalf("status+query: %v", vlIDs(got))
	}
	if _, err := repo.ListVouchers(ctx, VoucherFilter{Status: "bogus"}); err == nil {
		t.Fatal("an unknown status must be refused, not silently ignored")
	}

	got, _ = repo.ListVouchers(ctx, VoucherFilter{Limit: 2})
	if !vlEqual(vlIDs(got), []string{"GS-300", "50%_OFF"}) {
		t.Fatalf("limit: %v", vlIDs(got))
	}
}

func TestVoucherTransactions_TimeOrderWithReceipt(t *testing.T) {
	d := b8OpenDB(t, "voucher-history.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('sale-a','R-A',0,0), ('sale-b','R-B',0,0)`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}
	vlSeed(t, ctx, repo, Voucher{ID: "GS-H", OriginalAmountMinor: 3000, IssuedSaleID: "sale-a", CreatedAt: "2026-10-01T09:00:00Z"})
	for _, tx := range []VoucherTransaction{
		{ID: "t2", VoucherID: "GS-H", SaleID: "sale-b", Type: "redemption", AmountMinor: 1000, CreatedAt: "2026-10-05T09:00:00Z"},
		{ID: "t1", VoucherID: "GS-H", SaleID: "sale-a", Type: "issue", AmountMinor: 3000, CreatedAt: "2026-10-01T09:00:00Z"},
		{ID: "t3", VoucherID: "GS-H", SaleID: "sale-gone", Type: "redemption", AmountMinor: 500, CreatedAt: "2026-10-06T09:00:00Z"},
	} {
		if err := repo.RecordVoucherTransaction(ctx, nil, tx); err != nil {
			t.Fatalf("record %s: %v", tx.ID, err)
		}
	}

	hist, err := repo.VoucherTransactions(ctx, "GS-H")
	if err != nil {
		t.Fatalf("VoucherTransactions: %v", err)
	}
	if len(hist) != 3 || hist[0].ID != "t1" || hist[1].ID != "t2" || hist[2].ID != "t3" {
		t.Fatalf("time order: %+v", hist)
	}
	if hist[0].ReceiptNo != "R-A" || hist[1].ReceiptNo != "R-B" || hist[2].ReceiptNo != "" || hist[2].SaleID != "sale-gone" {
		t.Fatalf("receipt join: %+v", hist)
	}
	if hist[1].Type != "redemption" || hist[1].AmountMinor != 1000 {
		t.Fatalf("row: %+v", hist[1])
	}

	if _, err := repo.VoucherTransactions(ctx, "GS-NOPE"); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("unknown voucher: err = %v, want ErrVoucherNotFound", err)
	}
}
