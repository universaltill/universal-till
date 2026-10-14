package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#4277: a band's sale-discount share is capped at the goods it is
// taken off (goods gross inclusive, goods net exclusive) and any excess
// rolls down to the next band with room — so a tiny-goods top band never
// goes negative, and the bands still sum to what the customer paid. The
// card's repro: 1.00 @7%, 2.00 @10.7%, 0.01 @19%, a 3.00 sale discount
// (the basket allows up to 3.01). Before the fix the 19% band took the
// whole remainder of 2 and ended at −0.01 (fiscal-sign-ask.md
// § sale_discount step 3, 1.15.0).
func TestVATBandsForSale_DiscountShareCappedAtBandGoods(t *testing.T) {
	cases := []struct {
		name      string
		inclusive bool
		// want gross (inclusive) or net (exclusive) per band after the discount
		want7, want107, want19 int64
	}{
		// Floor shares 99 / 199 / 0, remainder 2: the 19% band takes the 1
		// it has, the 10.7% band the other.
		{"inclusive", true, 1, 0, 0},
		// Gross weights 1.07 / 2.21 / 0.01: floor shares 97 / 201 / 0, but
		// the 10.7% band only has 2.00 of net to discount — capped at 200,
		// and the remainder 3 fills 19% (1) then 7% (2).
		{"exclusive", false, 1, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := func(rateBP int, price int64) VATLine {
				tax, gross := ComputeTaxBasisPoints(money.FromMinor(price), rateBP, tc.inclusive)
				return VATLine{RateBP: rateBP, LineTotal: gross.Minor(), TaxAmount: tax.Minor()}
			}
			lines := []VATLine{line(700, 100), line(1070, 200), line(1900, 1)}
			var before int64
			for _, l := range lines {
				before += l.LineTotal
			}
			bands := VATBandsForSale(lines, 300, tc.inclusive, nil)

			pick := func(b VATBand) int64 {
				if tc.inclusive {
					return b.Gross
				}
				return b.Net
			}
			for _, w := range []struct {
				rate int
				want int64
			}{{700, tc.want7}, {1070, tc.want107}, {1900, tc.want19}} {
				b := bandByRate(t, bands, w.rate)
				if got := pick(b); got != w.want {
					t.Errorf("%d bp band = %+v, want %d", w.rate, b, w.want)
				}
				if b.Net+b.Tax != b.Gross {
					t.Errorf("%d bp band %+v: Net+Tax != Gross", w.rate, b)
				}
				if b.Gross < 0 || b.Net < 0 || b.Tax < 0 {
					t.Errorf("%d bp band %+v is negative", w.rate, b)
				}
			}
			// The bands still sum to what the customer paid.
			var sum int64
			for _, b := range bands {
				sum += b.Gross
			}
			if want := before - 300; sum != want {
				t.Errorf("bands gross sum %d, want %d", sum, want)
			}
		})
	}
}

// ut-docs#4277: the cap reaches the goods only — a single-purpose voucher
// sharing the top band keeps its full issue-time figures even when the
// band's goods can't absorb the remainder (ut-docs#4233). Inclusive:
// goods 1.00 @7%, 1.00 @10.7%, 0.01 @19% plus a 5.00 single-purpose
// voucher @19%, a 2.00 discount: floor shares 99 / 99 / 0, remainder 2 →
// the 19% goods take the 1 they have, the 10.7% band the other; nothing
// ever comes off the voucher (before the fix the band ended at 4.99).
func TestVATBandsForSale_CapNeverReachesVoucher(t *testing.T) {
	lines := []VATLine{
		{RateBP: 700, LineTotal: 100, TaxAmount: 7},
		{RateBP: 1070, LineTotal: 100, TaxAmount: 10},
		{RateBP: 1900, LineTotal: 1, TaxAmount: 0},
		{RateBP: 1900, LineTotal: 500, TaxAmount: 80, SinglePurposeVoucher: true},
	}
	bands := VATBandsForSale(lines, 200, true, nil)
	if b := bandByRate(t, bands, 700); b.Gross != 1 {
		t.Errorf("7%% band = %+v, want gross 1", b)
	}
	if b := bandByRate(t, bands, 1070); b.Gross != 0 {
		t.Errorf("10.7%% band = %+v, want gross 0", b)
	}
	b := bandByRate(t, bands, 1900)
	if b.Gross != 500 || b.Tax != 80 {
		t.Errorf("19%% band = %+v, want the voucher's own 500 gross / 80 tax", b)
	}
}

// ut-docs#4277: a discount larger than every band's goods (a fixed-amount
// discount on a basket selling no voucher — only a voucher sale caps it,
// ut-docs#4214) still lands its excess on the highest band carrying goods,
// so the bands keep summing to subtotal − discount (computeSaleTotals
// clamps tax and total afterwards).
func TestVATBandsForSale_OverDiscountStillSums(t *testing.T) {
	lines := []VATLine{
		{RateBP: 700, LineTotal: 100, TaxAmount: 7},
		{RateBP: 1900, LineTotal: 50, TaxAmount: 8},
	}
	bands := VATBandsForSale(lines, 200, true, nil)
	var sum int64
	for _, b := range bands {
		sum += b.Gross
	}
	if sum != 150-200 {
		t.Errorf("bands gross sum %d, want %d", sum, 150-200)
	}
	if b := bandByRate(t, bands, 700); b.Gross != 0 {
		t.Errorf("7%% band = %+v, want gross 0 (filled to its cap)", b)
	}
}
