package pos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// Single-purpose vouchers (ut-docs#1037, §3 Abs. 13-15 UStG): the good and
// its VAT rate are known at issue, so VAT is due at ISSUE, not deferred to
// redemption the way a multi-purpose voucher's is (ut-docs#1008). Same real
// migrated schema as voucher_sale_test.go (setupVoucherDB), every sale
// through the real CompleteSale.

// singlePurposeIssue is the card's golden voucher: 25.00 for a 19% good.
func singlePurposeIssue(id string) VoucherIssueInput {
	return VoucherIssueInput{
		VoucherID:          id,
		HolderLabel:        "Sample Holder",
		Amount:             money.FromMinor(2500),
		Purpose:            data.VoucherPurposeSingle,
		VATRateBasisPoints: 1900,
	}
}

// Golden: one single-purpose voucher at 19% is a taxable supply at issue —
// it lands in subtotal/taxTotal like a line (inclusive: 2500 gross, net
// 2101, tax 399; exclusive: 2500 net, tax 475, total 2975) and NEVER in
// voucherIssueTotal, which stays the multi-purpose 0% liability alone.
func TestComputeSaleTotals_SinglePurposeVoucherTaxedAtIssue(t *testing.T) {
	cases := []struct {
		name                             string
		inclusive                        bool
		subtotal, tax, issueTotal, total int64
	}{
		{"inclusive", true, 2500, 399, 0, 2500},
		{"exclusive", false, 2500, 475, 0, 2975},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subtotal, taxTotal, _, voucherIssueTotal, total, err := computeSaleTotals(SaleInput{
				TaxInclusive:  tc.inclusive,
				VoucherIssues: []VoucherIssueInput{singlePurposeIssue("GS-SP-1")},
			})
			if err != nil {
				t.Fatalf("computeSaleTotals: %v", err)
			}
			if subtotal.Minor() != tc.subtotal || taxTotal.Minor() != tc.tax || voucherIssueTotal.Minor() != tc.issueTotal || total.Minor() != tc.total {
				t.Fatalf("totals = subtotal %d tax %d voucherIssueTotal %d total %d, want %d/%d/%d/%d",
					subtotal.Minor(), taxTotal.Minor(), voucherIssueTotal.Minor(), total.Minor(),
					tc.subtotal, tc.tax, tc.issueTotal, tc.total)
			}
		})
	}
}

// A single-purpose and a multi-purpose voucher in the SAME sale: the
// multi-purpose one keeps today's exact treatment (0% liability, only in
// voucherIssueTotal/total), the single-purpose one is taxed — the two
// never bleed into each other's figures. The multi-purpose half is
// asserted against a voucher-only control computed by the same function,
// which is this card's hard regression bar.
func TestComputeSaleTotals_MixedPurposesKeepMultiPurposeUnchanged(t *testing.T) {
	multi := VoucherIssueInput{VoucherID: "GS-MP-1", Amount: money.FromMinor(1500)}
	cSub, cTax, _, cIssue, cTotal, err := computeSaleTotals(SaleInput{TaxInclusive: true, VoucherIssues: []VoucherIssueInput{multi}})
	if err != nil {
		t.Fatalf("control: %v", err)
	}
	if cSub != 0 || cTax != 0 || cIssue.Minor() != 1500 || cTotal.Minor() != 1500 {
		t.Fatalf("multi-purpose control moved: subtotal %d tax %d issue %d total %d", cSub, cTax, cIssue, cTotal)
	}
	sub, tax, _, issue, total, err := computeSaleTotals(SaleInput{
		TaxInclusive:  true,
		VoucherIssues: []VoucherIssueInput{multi, singlePurposeIssue("GS-SP-2")},
	})
	if err != nil {
		t.Fatalf("mixed: %v", err)
	}
	if issue != cIssue {
		t.Fatalf("voucherIssueTotal = %d, want the multi-purpose face value %d alone", issue, cIssue)
	}
	if sub.Minor() != 2500 || tax.Minor() != 399 || total.Minor() != cTotal.Minor()+2500 {
		t.Fatalf("mixed totals = subtotal %d tax %d total %d, want 2500/399/%d", sub, tax, total, cTotal.Minor()+2500)
	}
}

// Validation happens BEFORE any banding: an unknown purpose, or a
// single-purpose voucher with an out-of-range rate, is refused outright.
// A 0% rate is a legitimate single-purpose rate (a zero-rated good), not
// "unset" — that is why the rate is validated as a range, not as non-zero.
func TestComputeSaleTotals_SinglePurposeVoucherValidation(t *testing.T) {
	bad := []struct {
		name string
		in   VoucherIssueInput
		want string
	}{
		{"unknown purpose", VoucherIssueInput{Amount: money.FromMinor(100), Purpose: "gift"}, "purpose"},
		{"negative rate", VoucherIssueInput{Amount: money.FromMinor(100), Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: -1}, "vat rate"},
		{"rate over 100%", VoucherIssueInput{Amount: money.FromMinor(100), Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: 10001}, "vat rate"},
		{"rate on a multi-purpose voucher", VoucherIssueInput{Amount: money.FromMinor(100), VATRateBasisPoints: 1900}, "vat rate"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, _, err := computeSaleTotals(SaleInput{TaxInclusive: true, VoucherIssues: []VoucherIssueInput{tc.in}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
	// 0% is accepted: taxed at zero, still in subtotal/total, still not a
	// multi-purpose liability.
	sub, tax, _, issue, total, err := computeSaleTotals(SaleInput{TaxInclusive: true, VoucherIssues: []VoucherIssueInput{{
		Amount: money.FromMinor(100), Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: 0,
	}}})
	if err != nil || sub.Minor() != 100 || tax != 0 || issue != 0 || total.Minor() != 100 {
		t.Fatalf("0%% single-purpose: err=%v subtotal %d tax %d issue %d total %d, want 100/0/0/100", err, sub, tax, issue, total)
	}
}

// Golden through the real engine: the persisted sale carries the VAT
// (tax_total 399), voucher_issue_total stays 0 (not a multi-purpose
// liability), the vouchers row records purpose + the rate it was taxed at,
// and the sale's own VAT bands (the same VATBandsForSale the day-close and
// invoice use) show the voucher under 19% — the card's acceptance
// criterion that a single-purpose issue appears in the per-rate bands.
func TestCompleteSale_SinglePurposeVoucherIssueGolden(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)

	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{singlePurposeIssue("GS-SP-G")},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2500)}},
	})
	if err != nil {
		t.Fatalf("CompleteSale (single-purpose voucher-only): %v", err)
	}

	subtotal, taxTotal, total := saleRow(t, sqlDB, saleID)
	if subtotal != 2500 || taxTotal != 399 || total != 2500 {
		t.Fatalf("sale row = subtotal %d tax_total %d total %d, want 2500/399/2500", subtotal, taxTotal, total)
	}
	var voucherIssueTotal int64
	if err := sqlDB.QueryRow(`SELECT voucher_issue_total FROM sales WHERE id = ?`, saleID).Scan(&voucherIssueTotal); err != nil {
		t.Fatalf("read voucher_issue_total: %v", err)
	}
	if voucherIssueTotal != 0 {
		t.Fatalf("voucher_issue_total = %d, want 0 (single-purpose is not the 0%% liability)", voucherIssueTotal)
	}

	var purpose, vtype, status string
	var rate *int
	var original, balance int64
	if err := sqlDB.QueryRow(`SELECT purpose, issue_vat_rate_bp, voucher_type, status, original_amount, balance FROM vouchers WHERE id = 'GS-SP-G'`).
		Scan(&purpose, &rate, &vtype, &status, &original, &balance); err != nil {
		t.Fatalf("voucher row: %v", err)
	}
	if purpose != data.VoucherPurposeSingle || rate == nil || *rate != 1900 || vtype != "multi_purpose" || status != "active" || original != 2500 || balance != 2500 {
		t.Fatalf("voucher row = purpose %q rate %v voucher_type %q status %q original %d balance %d", purpose, rate, vtype, status, original, balance)
	}

	// Per-rate bands, reconstructed the way the day-close does it from the
	// persisted figures: one 19% band carrying exactly the sale's tax.
	v, err := data.NewPOSRepo(sqlDB).GetVoucherBalance(ctx, nil, "GS-SP-G")
	if err != nil {
		t.Fatalf("GetVoucherBalance: %v", err)
	}
	lineTax, lineGross := ComputeTaxBasisPoints(money.FromMinor(v.OriginalAmountMinor), *v.IssueVATRateBP, true)
	bands := VATBandsForSale([]VATLine{{RateBP: *v.IssueVATRateBP, LineTotal: lineGross.Minor(), TaxAmount: lineTax.Minor()}}, 0, true, 0, 0)
	if len(bands) != 1 || bands[0].RateBP != 1900 || bands[0].Tax != taxTotal || bands[0].Gross != 2500 || bands[0].Net != 2101 {
		t.Fatalf("bands = %+v, want one 19%% band net 2101 / tax %d / gross 2500", bands, taxTotal)
	}
}

// The multi-purpose path is byte-for-byte unchanged: an issue with no
// Purpose persists exactly as before (purpose 'multi_purpose', NULL rate,
// 0% liability).
func TestCompleteSale_DefaultPurposeStaysMultiPurpose(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{{VoucherID: "GS-MP-D", Amount: money.FromMinor(1500)}},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1500)}},
	})
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	subtotal, taxTotal, total := saleRow(t, sqlDB, saleID)
	if subtotal != 0 || taxTotal != 0 || total != 1500 {
		t.Fatalf("multi-purpose sale row moved: subtotal %d tax %d total %d", subtotal, taxTotal, total)
	}
	v, err := data.NewPOSRepo(sqlDB).GetVoucherBalance(ctx, nil, "GS-MP-D")
	if err != nil {
		t.Fatalf("GetVoucherBalance: %v", err)
	}
	if v.Purpose != data.VoucherPurposeMulti || v.IssueVATRateBP != nil {
		t.Fatalf("voucher = purpose %q rate %v, want multi_purpose/nil", v.Purpose, v.IssueVATRateBP)
	}
}

// Double-tax guard: a single-purpose voucher was taxed at issue, so paying
// for goods with it through the ordinary tender path would tax the same
// consideration twice (the goods' own rates, on top of the issue). The
// old redemption path must refuse it outright — the whole sale rolls
// back, the voucher stays untouched.
func TestCompleteSale_TenderRedemptionRejectsSinglePurposeVoucher(t *testing.T) {
	ctx := context.Background()
	sqlDB := setupVoucherDB(t)
	if _, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []VoucherIssueInput{singlePurposeIssue("GS-SP-R")},
		Payments:      []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2500)}},
	}); err != nil {
		t.Fatalf("issue sale: %v", err)
	}
	var salesBefore int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesBefore); err != nil {
		t.Fatalf("count sales: %v", err)
	}

	_, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "voucher", VoucherID: "GS-SP-R", Amount: money.FromMinor(1000)}},
	})
	if !errors.Is(err, data.ErrVoucherNotMultiPurpose) {
		t.Fatalf("tender redemption of a single-purpose voucher: err = %v, want ErrVoucherNotMultiPurpose", err)
	}

	var salesAfter int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&salesAfter); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if salesAfter != salesBefore {
		t.Fatalf("refused sale persisted: sales %d -> %d", salesBefore, salesAfter)
	}
	v, err := data.NewPOSRepo(sqlDB).GetVoucherBalance(ctx, nil, "GS-SP-R")
	if err != nil || v.BalanceMinor != 2500 || v.Status != "active" {
		t.Fatalf("voucher after refused tender = %+v (err %v), want balance 2500 / active", v, err)
	}
}
