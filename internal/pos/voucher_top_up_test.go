package pos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// Top up a gift voucher as a sale (ut-docs#4252): a VoucherIssueInput with
// TopUp credits an EXISTING multi-purpose voucher in the sale's own
// transaction — a 0% liability the customer pays for, never taxed, never a
// new vouchers row.

func issueVoucherForTopUp(t *testing.T, sqlDB *sql.DB, id string, amount int64) string {
	t.Helper()
	saleID, err := CompleteSale(context.Background(), sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: id, HolderLabel: "Sam", Amount: money.FromMinor(amount)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(amount)}},
	})
	if err != nil {
		t.Fatalf("issue %s: %v", id, err)
	}
	return saleID
}

func voucherRow(t *testing.T, sqlDB *sql.DB, id string) (original, balance int64, status, issuedSale string) {
	t.Helper()
	if err := sqlDB.QueryRow(`SELECT original_amount, balance, status, issued_sale_id FROM vouchers WHERE id = ?`, id).
		Scan(&original, &balance, &status, &issuedSale); err != nil {
		t.Fatalf("read voucher %s: %v", id, err)
	}
	return
}

func topUpSale(sqlDB *sql.DB, id string, amount int64, withArticle bool, payments ...PaymentInput) (string, error) {
	in := SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: id, Amount: money.FromMinor(amount), TopUp: true}},
	}
	total := amount
	if withArticle {
		in.Lines = []SaleLineInput{articleLine()}
		total += 1000
	}
	if len(payments) == 0 {
		payments = []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(total)}}
	}
	in.Payments = payments
	return CompleteSale(context.Background(), sqlDB, in)
}

func TestCompleteSale_TopUpCreditsExistingVoucher(t *testing.T) {
	sqlDB := setupVoucherDB(t)
	issueSale := issueVoucherForTopUp(t, sqlDB, "GS-T1", 1500)

	saleID, err := topUpSale(sqlDB, "GS-T1", 2500, true)
	if err != nil {
		t.Fatalf("top-up sale: %v", err)
	}
	original, balance, status, issuedSale := voucherRow(t, sqlDB, "GS-T1")
	if balance != 4000 || original != 1500 || status != "active" || issuedSale != issueSale {
		t.Fatalf("voucher after top-up: original=%d balance=%d status=%s issued_sale=%s, want 1500/4000/active/%s", original, balance, status, issuedSale, issueSale)
	}
	var n int
	_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM vouchers`).Scan(&n)
	if n != 1 {
		t.Fatalf("a top-up created a vouchers row: %d rows", n)
	}
	var txType string
	var amount int64
	if err := sqlDB.QueryRow(`SELECT type, amount FROM voucher_transactions WHERE sale_id = ?`, saleID).Scan(&txType, &amount); err != nil {
		t.Fatalf("top-up ledger row: %v", err)
	}
	if txType != "top_up" || amount != 2500 {
		t.Fatalf("ledger row = %s/%d, want top_up/2500", txType, amount)
	}
	// 0% liability: the article (10.00 incl. 19%) is the only revenue and
	// the only tax; the customer pays both.
	subtotal, taxTotal, total := saleRow(t, sqlDB, saleID)
	if subtotal != 1000 || taxTotal != 160 || total != 3500 {
		t.Fatalf("sale figures = subtotal %d tax %d total %d, want 1000/160/3500", subtotal, taxTotal, total)
	}
	var vit int64
	if err := sqlDB.QueryRow(`SELECT voucher_issue_total FROM sales WHERE id = ?`, saleID).Scan(&vit); err != nil {
		t.Fatal(err)
	}
	if vit != 2500 {
		t.Fatalf("voucher_issue_total = %d, want 2500", vit)
	}
}

func TestCompleteSale_TopUpRefusals(t *testing.T) {
	sqlDB := setupVoucherDB(t)
	issueVoucherForTopUp(t, sqlDB, "GS-OK", 1000)
	issueVoucherForTopUp(t, sqlDB, "GS-VOID", 1000)
	if _, err := sqlDB.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VOID'`); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteSale(context.Background(), sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-SP", Amount: money.FromMinor(1000), Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: 1900}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	}); err != nil {
		t.Fatalf("issue single-purpose: %v", err)
	}
	countSales := func() int {
		var n int
		_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n)
		return n
	}
	before := countSales()

	if _, err := topUpSale(sqlDB, "GS-NOPE", 500, false); !errors.Is(err, data.ErrVoucherNotFound) {
		t.Fatalf("unknown voucher: err = %v, want ErrVoucherNotFound", err)
	}
	if _, err := topUpSale(sqlDB, "GS-VOID", 500, false); !errors.Is(err, data.ErrVoucherNotActive) {
		t.Fatalf("void voucher: err = %v, want ErrVoucherNotActive", err)
	}
	if _, err := topUpSale(sqlDB, "GS-SP", 500, false); !errors.Is(err, data.ErrVoucherNotMultiPurpose) {
		t.Fatalf("single-purpose voucher: err = %v, want ErrVoucherNotMultiPurpose", err)
	}
	if _, err := topUpSale(sqlDB, "", 500, false); err == nil {
		t.Fatal("a top-up with no voucher id must be refused (never generate a new number)")
	}
	if _, err := topUpSale(sqlDB, "GS-OK", int64(MaxVoucherIssueAmount.Minor()), false); !errors.Is(err, data.ErrVoucherTopUpOverCeiling) {
		t.Fatalf("over the balance ceiling: err = %v, want ErrVoucherTopUpOverCeiling", err)
	}
	// Paying for the top-up with the same voucher.
	if _, err := topUpSale(sqlDB, "GS-OK", 500, false, PaymentInput{MethodID: "voucher", VoucherID: "GS-OK", Amount: money.FromMinor(500)}); err == nil {
		t.Fatal("a top-up paid by the same voucher must be refused")
	}
	// A top-up must be multi-purpose with no rate.
	_, err := CompleteSale(context.Background(), sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-OK", Amount: money.FromMinor(500), TopUp: true, Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: 1900}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})
	if err == nil {
		t.Fatal("a single-purpose top-up must be refused")
	}
	// The same number issued and topped up in one sale.
	_, err = CompleteSale(context.Background(), sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{
			{VoucherID: "GS-NEW", Amount: money.FromMinor(500)},
			{VoucherID: "GS-NEW", Amount: money.FromMinor(500), TopUp: true},
		},
		Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})
	if err == nil {
		t.Fatal("issuing and topping up the same number in one sale must be refused")
	}
	if got := countSales(); got != before {
		t.Fatalf("refused top-ups persisted %d sale(s)", got-before)
	}
	if _, balance, _, _ := voucherRow(t, sqlDB, "GS-OK"); balance != 1000 {
		t.Fatalf("refused top-ups moved GS-OK to %d", balance)
	}
}

func TestUpdateSaleStatus_VoidReversesTopUp(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	issueVoucherForTopUp(t, sqlDB, "GS-T1", 1000)
	saleID, err := topUpSale(sqlDB, "GS-T1", 2500, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateSaleStatus(ctx, sqlDB, saleID, "voided", "", "", "wrong voucher"); err != nil {
		t.Fatalf("void top-up sale: %v", err)
	}
	if _, balance, status, _ := voucherRow(t, sqlDB, "GS-T1"); balance != 1000 || status != "active" {
		t.Fatalf("after void: balance=%d status=%s, want 1000/active (credit taken back, voucher alive)", balance, status)
	}
	// A repeat void is refused by the status machine and debits nothing.
	if err := UpdateSaleStatus(ctx, sqlDB, saleID, "voided", "", "", "again"); err == nil {
		t.Fatal("re-voiding a voided sale must be refused")
	}
	if _, balance, _, _ := voucherRow(t, sqlDB, "GS-T1"); balance != 1000 {
		t.Fatalf("re-void debited again: balance=%d", balance)
	}
}

func TestUpdateSaleStatus_VoidRefusedWhenTopUpSpent(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	issueVoucherForTopUp(t, sqlDB, "GS-T1", 1000)
	saleID, err := topUpSale(sqlDB, "GS-T1", 2500, false)
	if err != nil {
		t.Fatal(err)
	}
	// Spend 3000 of the 3500: 500 left, below the 2500 added.
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{{ItemID: "itm1", Name: "Coffee Beans", Qty: 3, UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900, LocationID: "loc1"}},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-T1", Amount: money.FromMinor(3000)}},
	}); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if err := UpdateSaleStatus(ctx, sqlDB, saleID, "voided", "", "", "too late"); !errors.Is(err, data.ErrVoucherRedeemedCannotVoid) {
		t.Fatalf("void of a spent top-up: err = %v, want ErrVoucherRedeemedCannotVoid", err)
	}
	var st string
	_ = sqlDB.QueryRow(`SELECT status FROM sales WHERE id = ?`, saleID).Scan(&st)
	if st != "completed" {
		t.Fatalf("refused void left the sale %q", st)
	}
	if _, balance, _, _ := voucherRow(t, sqlDB, "GS-T1"); balance != 500 {
		t.Fatalf("refused void moved the balance to %d", balance)
	}
}

func TestCompleteSale_TopUpCountsAsIssuedOnDayEnd(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	issueVoucherForTopUp(t, sqlDB, "GS-T1", 1000)
	if _, err := topUpSale(sqlDB, "GS-T1", 2500, false); err != nil {
		t.Fatal(err)
	}
	var createdAt string
	_ = sqlDB.QueryRow(`SELECT created_at FROM voucher_transactions WHERE type = 'top_up'`).Scan(&createdAt)
	day := localDayOf(t, sqlDB, createdAt)
	sum, err := data.NewPOSRepo(sqlDB).VouchersIssuedRedeemedForRange(ctx, day, day)
	if err != nil {
		t.Fatal(err)
	}
	if sum.IssuedCount != 2 || sum.IssuedMinor != 3500 {
		t.Fatalf("issued = %d/%d, want 2/3500 (the issue and the top-up)", sum.IssuedCount, sum.IssuedMinor)
	}
}

// The basket side: a top-up line rides the basket like a voucher sale and
// survives a hold/resume (the snapshot is JSON).
func TestAddGiftVoucherLine_TopUp(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, giftVoucherTestResolver())
	base := BasketLine{ItemID: "gv", Name: "Gift voucher", ItemType: catalogtypes.ItemTypeGiftVoucher}
	if _, err := s.AddGiftVoucherLine(base, money.FromMinor(2500), GiftVoucherLine{Code: "GS-T1", HolderLabel: "Sam", TopUp: true, Purpose: data.VoucherPurposeSingle}); err == nil {
		t.Fatal("a single-purpose top-up line must be refused")
	}
	b, err := s.AddGiftVoucherLine(base, money.FromMinor(2500), GiftVoucherLine{Code: "GS-T1", HolderLabel: "Sam", TopUp: true})
	if err != nil {
		t.Fatalf("AddGiftVoucherLine top-up: %v", err)
	}
	if len(b.Lines) != 1 || b.Lines[0].GiftVoucher == nil || !b.Lines[0].GiftVoucher.TopUp || b.Lines[0].GiftVoucher.Purpose != data.VoucherPurposeMulti {
		t.Fatalf("basket line = %+v", b.Lines)
	}
	raw, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snap BasketSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	s2 := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, giftVoucherTestResolver())
	s2.RestoreHeld(snap, HeldOrigin{})
	lines := s2.Lines()
	if len(lines) != 1 || lines[0].GiftVoucher == nil || !lines[0].GiftVoucher.TopUp || lines[0].GiftVoucher.Code != "GS-T1" {
		t.Fatalf("restored line lost the top-up flag: %+v", lines)
	}
}
