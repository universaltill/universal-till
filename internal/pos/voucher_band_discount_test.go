package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#4233: a basket discount applies to the goods only — never to a
// single-purpose voucher being sold (#4214) — so VATBandsForSale must not
// prorate it into the voucher's share of a band. The card's example: goods
// 100.00 @19% + specific-item voucher 50.00 @7%, a 10.00 discount.

func bandByRate(t *testing.T, bands []VATBand, rateBP int) VATBand {
	t.Helper()
	for _, b := range bands {
		if b.RateBP == rateBP {
			return b
		}
	}
	t.Fatalf("no %d bp band in %+v", rateBP, bands)
	return VATBand{}
}

func TestVATBandsForSale_DiscountSkipsSinglePurposeVoucher(t *testing.T) {
	cases := []struct {
		name      string
		inclusive bool
		want19    VATBand
		want7     VATBand
	}{
		// Inclusive: 19% band 90.00 gross → net 75.63, VAT 14.37; the
		// voucher keeps its issue-time 50.00 / VAT 3.27.
		{"inclusive", true, VATBand{RateBP: 1900, Net: 7563, Tax: 1437, Gross: 9000}, VATBand{RateBP: 700, Net: 4673, Tax: 327, Gross: 5000}},
		// Exclusive: the discount comes off the goods net (100.00 → 90.00),
		// tax stays as computed; the voucher's 50.00 + 3.50 is untouched.
		{"exclusive", false, VATBand{RateBP: 1900, Net: 9000, Tax: 1900, Gross: 10900}, VATBand{RateBP: 700, Net: 5000, Tax: 350, Gross: 5350}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goodsTax, goodsGross := ComputeTaxBasisPoints(money.FromMinor(10000), 1900, tc.inclusive)
			lines := []VATLine{
				{RateBP: 1900, LineTotal: goodsGross.Minor(), TaxAmount: goodsTax.Minor()},
				SinglePurposeVoucherVATLine(700, 5000, tc.inclusive),
			}
			bands := VATBandsForSale(lines, 1000, tc.inclusive, 0, 0)
			if got := bandByRate(t, bands, 1900); got != tc.want19 {
				t.Errorf("19%% band = %+v, want %+v", got, tc.want19)
			}
			if got := bandByRate(t, bands, 700); got != tc.want7 {
				t.Errorf("7%% band = %+v, want %+v (the discount must not reach the voucher)", got, tc.want7)
			}
		})
	}
}

// A goods line and a voucher in the SAME rate band: only the goods part of
// the band is discounted (and, inclusive, re-derived); the voucher part
// keeps exactly the tax it was issued with. A 19% goods line rides along
// so the discount is genuinely split across two goods bands.
func TestVATBandsForSale_MixedBandDiscountsGoodsPartOnly(t *testing.T) {
	for _, inclusive := range []bool{true, false} {
		goods7Tax, goods7Gross := ComputeTaxBasisPoints(money.FromMinor(4000), 700, inclusive)
		goods19Tax, goods19Gross := ComputeTaxBasisPoints(money.FromMinor(6000), 1900, inclusive)
		voucher := SinglePurposeVoucherVATLine(700, 5000, inclusive)
		withVoucher := VATBandsForSale([]VATLine{
			{RateBP: 700, LineTotal: goods7Gross.Minor(), TaxAmount: goods7Tax.Minor()},
			voucher,
			{RateBP: 1900, LineTotal: goods19Gross.Minor(), TaxAmount: goods19Tax.Minor()},
		}, 1000, inclusive, 0, 0)
		goodsOnly := VATBandsForSale([]VATLine{
			{RateBP: 700, LineTotal: goods7Gross.Minor(), TaxAmount: goods7Tax.Minor()},
			{RateBP: 1900, LineTotal: goods19Gross.Minor(), TaxAmount: goods19Tax.Minor()},
		}, 1000, inclusive, 0, 0)
		// The voucher adds exactly its own issue-time figures to the 7%
		// band; the goods' discounted figures are what a goods-only sale
		// with the same discount declares.
		want7 := bandByRate(t, goodsOnly, 700)
		want7.Gross += voucher.LineTotal
		want7.Tax += voucher.TaxAmount
		want7.Net += voucher.LineTotal - voucher.TaxAmount
		if got := bandByRate(t, withVoucher, 700); got != want7 {
			t.Errorf("inclusive=%v: 7%% band = %+v, want %+v", inclusive, got, want7)
		}
		if got, want := bandByRate(t, withVoucher, 1900), bandByRate(t, goodsOnly, 1900); got != want {
			t.Errorf("inclusive=%v: 19%% band = %+v, want %+v (same discount share as without the voucher)", inclusive, got, want)
		}
	}
}

// The persisted sale's tax_total (and so the receipt) carries the same
// fix: computeSaleTotals bands the voucher issue without the discount.
func TestComputeSaleTotals_DiscountSkipsSinglePurposeVoucherTax(t *testing.T) {
	cases := []struct {
		name             string
		inclusive        bool
		wantTax, wantTot int64
	}{
		{"inclusive", true, 1437 + 327, 14000},
		{"exclusive", false, 1900 + 350, 10000 + 5000 - 1000 + 1900 + 350},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, taxTotal, _, _, total, err := computeSaleTotals(SaleInput{
				TaxInclusive: tc.inclusive,
				Lines:        []SaleLineInput{{ItemID: "goods", LocationID: "loc-1", Qty: 1, UnitPrice: money.FromMinor(10000), TaxRateBasisPoints: 1900}},
				VoucherIssues: []VoucherIssueInput{{
					VoucherID: "GS-SP-4233", Amount: money.FromMinor(5000),
					Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: 700,
				}},
				SaleDiscount: money.FromMinor(1000),
			})
			if err != nil {
				t.Fatalf("computeSaleTotals: %v", err)
			}
			if taxTotal.Minor() != tc.wantTax || total.Minor() != tc.wantTot {
				t.Fatalf("tax %d total %d, want %d / %d", taxTotal.Minor(), total.Minor(), tc.wantTax, tc.wantTot)
			}
		})
	}
}

// The live basket panel shows the same VAT the sale will record.
func TestGiftVoucherLine_BasketDiscountSkipsSinglePurposeVoucherTax(t *testing.T) {
	resolver := mapResolver{
		"GV":    {SKU: "GV", Name: "Gift voucher", Qty: 1, ItemID: "itm-gv", TaxRateBP: 700, TaxCodeID: "tax_red", ItemType: "gift_voucher"},
		"GOODS": {SKU: "GOODS", Name: "Goods", Qty: 1, PriceCents: 10000, ItemID: "itm-goods", TaxRateBP: 1900, TaxCodeID: "tax_std"},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 1900, TaxInclusive: true}, resolver)
	_, _ = s.Scan("GOODS")
	addGV(t, s, 5000, GiftVoucherLine{Code: "4233", Purpose: data.VoucherPurposeSingle})
	s.SetDiscountPercent(1000)
	b := s.Basket()
	if b.Discount != money.FromMinor(1000) || b.Total != money.FromMinor(14000) || b.Tax != money.FromMinor(1437+327) {
		t.Fatalf("discount %v total %v tax %v, want 10.00 / 140.00 / 17.64", b.Discount, b.Total, b.Tax)
	}
}

// The day-close (Z report) bands — and through the same adapter the
// invoice and the cloud sales upload — read the voucher back from its
// vouchers row, not a sale line; it must still escape the discount.
func TestEODTaxBandsFromSales_DiscountSkipsSinglePurposeVoucher(t *testing.T) {
	sale := data.EODTaxBandSale{
		ID: "s-4233", SaleType: "sale",
		Subtotal: 15000, DiscountTotal: 1000, TaxTotal: 1437 + 327, Total: 14000,
		Lines:                      []data.EODTaxBandLine{{RateBP: 1900, LineTotal: 10000, TaxAmount: 1597}},
		SinglePurposeVoucherIssues: []data.EODTaxBandVoucherIssue{{RateBP: 700, Amount: 5000}},
	}
	bands := EODTaxBandsFromSales([]data.EODTaxBandSale{sale})
	want := []data.TaxBand{{RateBP: 700, Net: 4673, Tax: 327, Gross: 5000}, {RateBP: 1900, Net: 7563, Tax: 1437, Gross: 9000}}
	if len(bands) != len(want) {
		t.Fatalf("bands = %+v, want %+v", bands, want)
	}
	var tax int64
	for i := range want {
		if bands[i].RateBP != want[i].RateBP || bands[i].Net != want[i].Net || bands[i].Tax != want[i].Tax || bands[i].Gross != want[i].Gross {
			t.Errorf("band %d = %+v, want %+v", i, bands[i], want[i])
		}
		tax += bands[i].Tax
	}
	if tax != sale.TaxTotal {
		t.Errorf("sum(band.Tax) = %d, want the sale's tax_total %d", tax, sale.TaxTotal)
	}
}

// Review finding (#4233): the largest-remainder penny must never land on
// a band whose only gross is a voucher — here the 19% band's goods line is
// fully line-discounted to 0.00, so its goods carry nothing to discount.
func TestVATBandsForSale_RemainderNeverLandsOnVoucherOnlyGross(t *testing.T) {
	for _, inclusive := range []bool{true, false} {
		goodsTax, goodsGross := ComputeTaxBasisPoints(money.FromMinor(5000), 700, inclusive)
		voucher := SinglePurposeVoucherVATLine(1900, 5000, inclusive)
		bands := VATBandsForSale([]VATLine{
			{RateBP: 0, LineTotal: 5000},
			{RateBP: 700, LineTotal: goodsGross.Minor(), TaxAmount: goodsTax.Minor()},
			{RateBP: 1900, LineTotal: 0, TaxAmount: 0},
			voucher,
		}, 1, inclusive, 0, 0)
		got := bandByRate(t, bands, 1900)
		want := VATBand{RateBP: 1900, Gross: voucher.LineTotal, Tax: voucher.TaxAmount, Net: voucher.LineTotal - voucher.TaxAmount}
		if got != want {
			t.Errorf("inclusive=%v: 19%% band = %+v, want the voucher's own %+v", inclusive, got, want)
		}
		var gross int64
		for _, b := range bands {
			gross += b.Gross
		}
		wantGross := 5000 + goodsGross.Minor() + voucher.LineTotal - 1
		if gross != wantGross {
			t.Errorf("inclusive=%v: sum(gross) = %d, want %d", inclusive, gross, wantGross)
		}
	}
}
