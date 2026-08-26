package data

import (
	"context"
	"testing"
)

// ut-docs#1053: a voucher issued or redeemed in a sale must survive the
// LAN-sync journal round trip. The journal payload IS GetSaleDetail's
// output, so the data layer must (a) persist which payment row redeemed
// which voucher (payments.voucher_id, migration 072) and (b) expose the
// sale's issued vouchers on SaleDetail.VoucherIssues — both via the real
// migrated schema (b8OpenDB), never a hand-built twin.

func TestPOSRepo_PaymentVoucherID_RoundTrip(t *testing.T) {
	d := b8OpenDB(t, "voucher-payment-roundtrip.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT OR IGNORE INTO payment_methods (id, name, type, sort_order) VALUES ('voucher','Voucher','voucher',9)`)
	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-vp','VP1','Coffee Beans',1000,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-vp','R-VP-1','completed','sale','EUR',1000,0,0,1000,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-vp','sale-vp',1,'itm-vp','Coffee Beans',1,1000,0,0,0,1000,1000)`)
	vSeedVoucher(t, ctx, repo, "GS-PAY-1", 1500)

	// A tracked voucher redemption keeps its voucher id on the payment row…
	if err := repo.InsertPayment(ctx, nil, "pay-vp-1", "sale-vp", "voucher", 600, "EUR", "", 0, 0, "employee", "GS-PAY-1", "2026-08-26T10:00:00Z", CardPresentFields{}); err != nil {
		t.Fatalf("insert voucher payment: %v", err)
	}
	// …and an untracked payment stays untracked (empty voucher_id).
	if err := repo.InsertPayment(ctx, nil, "pay-vp-2", "sale-vp", "cash", 400, "EUR", "", 0, 0, "employee", "", "2026-08-26T10:00:01Z", CardPresentFields{}); err != nil {
		t.Fatalf("insert cash payment: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-VP-1")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetail: ok=%v err=%v", ok, err)
	}
	if len(detail.Payments) != 2 {
		t.Fatalf("want 2 payments, got %d", len(detail.Payments))
	}
	if detail.Payments[0].VoucherID != "GS-PAY-1" {
		t.Fatalf("voucher payment's VoucherID = %q, want GS-PAY-1 — the redemption's voucher id was dropped", detail.Payments[0].VoucherID)
	}
	if detail.Payments[1].VoucherID != "" {
		t.Fatalf("cash payment's VoucherID = %q, want empty", detail.Payments[1].VoucherID)
	}
}

func TestPOSRepo_SaleDetailVoucherIssues_RoundTrip(t *testing.T) {
	d := b8OpenDB(t, "voucher-issues-roundtrip.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, voucher_issue_total, created_at) VALUES ('sale-vi','R-VI-1','completed','sale','EUR',0,0,0,2000,2000,datetime('now'))`)

	// Two vouchers issued IN this sale (one with a holder label, one without)…
	if err := repo.CreateVoucher(ctx, nil, Voucher{ID: "GS-ISS-1", HolderLabel: "Sample Holder", OriginalAmountMinor: 1500, BalanceMinor: 1500, Currency: "EUR", IssuedSaleID: "sale-vi", CreatedAt: "2026-08-26T10:00:00Z"}); err != nil {
		t.Fatalf("create voucher 1: %v", err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{ID: "vt-iss-1", VoucherID: "GS-ISS-1", SaleID: "sale-vi", Type: "issue", AmountMinor: 1500, CreatedAt: "2026-08-26T10:00:00Z"}); err != nil {
		t.Fatalf("record issue 1: %v", err)
	}
	if err := repo.CreateVoucher(ctx, nil, Voucher{ID: "GS-ISS-2", OriginalAmountMinor: 500, BalanceMinor: 500, Currency: "EUR", IssuedSaleID: "sale-vi", CreatedAt: "2026-08-26T10:00:00Z"}); err != nil {
		t.Fatalf("create voucher 2: %v", err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{ID: "vt-iss-2", VoucherID: "GS-ISS-2", SaleID: "sale-vi", Type: "issue", AmountMinor: 500, CreatedAt: "2026-08-26T10:00:00Z"}); err != nil {
		t.Fatalf("record issue 2: %v", err)
	}
	// …plus a redemption of an OLDER voucher in the same sale, which must
	// NOT appear in VoucherIssues (it rides payments[].voucher_id instead).
	vSeedVoucher(t, ctx, repo, "GS-OLD-1", 1000)
	if err := repo.RecordVoucherTransaction(ctx, nil, VoucherTransaction{ID: "vt-red-1", VoucherID: "GS-OLD-1", SaleID: "sale-vi", Type: "redemption", AmountMinor: 300, CreatedAt: "2026-08-26T10:00:01Z"}); err != nil {
		t.Fatalf("record redemption: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-VI-1")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetail: ok=%v err=%v", ok, err)
	}
	if len(detail.VoucherIssues) != 2 {
		t.Fatalf("want 2 voucher issues on the sale detail, got %d (%+v)", len(detail.VoucherIssues), detail.VoucherIssues)
	}
	byID := map[string]SaleDetailVoucherIssue{}
	for _, vi := range detail.VoucherIssues {
		byID[vi.VoucherID] = vi
	}
	if vi := byID["GS-ISS-1"]; vi.HolderLabel != "Sample Holder" || vi.Amount != 1500 {
		t.Fatalf("GS-ISS-1 = %+v, want holder 'Sample Holder' amount 1500", vi)
	}
	if vi := byID["GS-ISS-2"]; vi.HolderLabel != "" || vi.Amount != 500 {
		t.Fatalf("GS-ISS-2 = %+v, want empty holder, amount 500", vi)
	}

	// A voucher-free sale carries no VoucherIssues at all (omitempty keeps
	// the journal wire additive — the key must be absent, not []).
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-novi','R-NOVI-1','completed','sale','EUR',100,0,0,100,datetime('now'))`)
	plain, ok, err := repo.GetSaleDetail(ctx, "R-NOVI-1")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetail (plain): ok=%v err=%v", ok, err)
	}
	if len(plain.VoucherIssues) != 0 {
		t.Fatalf("voucher-free sale has %d VoucherIssues, want 0", len(plain.VoucherIssues))
	}
}
