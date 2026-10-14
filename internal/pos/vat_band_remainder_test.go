package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#4275: the largest-remainder pennies of a sale discount go on the
// highest band carrying goods worth more than nothing — never on a band
// whose only goods line is fully line-discounted (gross 0), which would
// end up negative. The card's repro: 1.00 @7%, 2.00 @10.7%, a 0.00 line
// @19%, a 1.00 sale discount → 0.67 / 1.33 / 0.00 (what ut-plugin-tax-de
// signs; fiscal-sign-ask.md § sale_discount step 3).
func TestVATBandsForSale_RemainderSkipsZeroGoodsBand(t *testing.T) {
	cases := []struct {
		name      string
		inclusive bool
		// want gross (inclusive) or net (exclusive) per band after the discount
		want7, want107, want19 int64
	}{
		{"inclusive", true, 67, 133, 0},
		// Exclusive prorates by gross share too (1.07 vs 2.21), off the net.
		{"exclusive", false, 68, 132, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := func(rateBP int, price int64) VATLine {
				tax, gross := ComputeTaxBasisPoints(money.FromMinor(price), rateBP, tc.inclusive)
				return VATLine{RateBP: rateBP, LineTotal: gross.Minor(), TaxAmount: tax.Minor()}
			}
			lines := []VATLine{line(700, 100), line(1070, 200), line(1900, 0)}
			var before int64
			for _, l := range lines {
				before += l.LineTotal
			}
			bands := VATBandsForSale(lines, 100, tc.inclusive, 0, 0)

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
			if want := before - 100; sum != want {
				t.Errorf("bands gross sum %d, want %d", sum, want)
			}
		})
	}
}
