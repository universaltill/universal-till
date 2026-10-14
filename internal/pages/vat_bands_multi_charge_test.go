package pages

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#4301: a sale with 2+ charges (ADR-0062) is banded per charge —
// each at its own tax basis — by the Z report, the invoice VAT table and
// the cloud tax window, exactly as computeSaleTotals and fiscal.sign.ask
// apportion it (pos.ApportionChargesTax). Before the fix pos.VATBandsForSale
// apportioned the summed sales.service_charge_amount once at the lines'
// rates, so the flat-7% tourism levy below landed partly in the 20% band
// and the bands' tax no longer summed to the sale's tax_total.
func TestVATBands_MultiChargeBandedPerCharge(t *testing.T) {
	for _, inclusive := range []bool{false, true} {
		name := "exclusive"
		if inclusive {
			name = "inclusive"
		}
		t.Run(name, func(t *testing.T) {
			mux, dp := newPOSTestDeps(t)
			dp.UpdateState(func(s *common.RuntimeState) {
				s.ServiceChargeRateBasisPoints = 1250
				s.TaxInclusive = inclusive
			})
			engine := pos.NewServiceWithResolver(pos.Config{
				TaxInclusive:                 inclusive,
				TaxRateBasisPoints:           2000,
				ServiceChargeRateBasisPoints: 1250,
			}, stubResolver{
				"HI": {SKU: "HI", Name: "Main", Qty: 1, PriceCents: 1999, ItemID: "itm1", TaxRateBP: 2000},
				"LO": {SKU: "LO", Name: "Side", Qty: 1, PriceCents: 333, ItemID: "itm-plain2", TaxRateBP: 700},
			})
			engine.SetChargePolicyAsker(fakeMultiChargeAsker{})
			dp.Engine = engine
			for _, code := range []string{"HI", "LO"} {
				if _, err := dp.Engine.Scan(code); err != nil {
					t.Fatalf("scan %s: %v", code, err)
				}
			}
			if rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0"); rec.Code != http.StatusOK {
				t.Fatalf("tender: expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var saleID string
			var total, taxTotal int64
			if err := dp.Db.QueryRow(`SELECT id, total, tax_total FROM sales`).Scan(&saleID, &total, &taxTotal); err != nil {
				t.Fatalf("query sale: %v", err)
			}

			ctx := context.Background()
			repo := data.NewPOSRepo(dp.Db)
			detail, found, err := repo.GetSaleDetailByID(ctx, saleID)
			if err != nil || !found {
				t.Fatalf("sale detail: found=%v err=%v", found, err)
			}
			if len(detail.Charges) != 3 {
				t.Fatalf("sale detail charges = %+v, want the 3 itemized rows", detail.Charges)
			}

			// Expected per rate: the goods lines as recorded, plus every
			// charge apportioned on its own (the engine's and the signer's
			// banding).
			type band struct{ net, tax, gross int64 }
			want := map[int]band{}
			var chargeLines []pos.ChargeTaxLine
			for _, l := range detail.Lines {
				b := want[l.TaxRateBP]
				b.tax += l.TaxAmount
				b.gross += l.LineTotal
				b.net += l.LineTotal - l.TaxAmount
				want[l.TaxRateBP] = b
				net := l.LineTotal
				if !inclusive {
					net -= l.TaxAmount
				}
				chargeLines = append(chargeLines, pos.ChargeTaxLine{RateBP: l.TaxRateBP, Net: money.FromMinor(net)})
			}
			var charges []pos.ChargeInput
			for _, c := range detail.Charges {
				charges = append(charges, pos.ChargeInput{Key: c.Key, Amount: money.FromMinor(c.Amount), TaxBasisBP: c.TaxBasisBP, Base: c.Base})
			}
			for _, cb := range pos.ApportionChargesTax(charges, chargeLines, inclusive) {
				b := want[cb.RateBP]
				b.tax += cb.Tax.Minor()
				if inclusive {
					b.gross += cb.Amount.Minor()
					b.net += cb.Amount.Minor() - cb.Tax.Minor()
				} else {
					b.net += cb.Amount.Minor()
					b.gross += cb.Amount.Minor() + cb.Tax.Minor()
				}
				want[cb.RateBP] = b
			}

			check := func(src string, got map[int]band) {
				t.Helper()
				if len(got) != len(want) {
					t.Fatalf("%s: bands %+v, want %+v", src, got, want)
				}
				var sumTax, sumGross int64
				for rate, w := range want {
					if got[rate] != w {
						t.Errorf("%s: %d bp band %+v, want %+v", src, rate, got[rate], w)
					}
					sumTax += got[rate].tax
					sumGross += got[rate].gross
				}
				if sumTax != taxTotal || sumGross != total {
					t.Errorf("%s: bands sum tax %d gross %d, want the sale's tax_total %d and total %d", src, sumTax, sumGross, taxTotal, total)
				}
			}

			inv := map[int]band{}
			for _, b := range vatBreakdown(detail) {
				inv[b.RateBP] = band{b.Net, b.Tax, b.Gross}
			}
			check("invoice", inv)

			eodSales, err := repo.SalesForTaxBands(ctx, "2000-01-01", "2999-12-31")
			if err != nil {
				t.Fatalf("SalesForTaxBands: %v", err)
			}
			eod := map[int]band{}
			for _, b := range computeEODTaxBandsFromSales(eodSales) {
				eod[b.RateBP] = band{b.Net, b.Tax, b.Gross}
			}
			check("Z report", eod)

			cross := map[int]band{}
			for _, b := range computeEODMethodTaxBandsFromSales(eodSales) {
				c := cross[b.RateBP]
				c.net += b.Net
				c.tax += b.Tax
				c.gross += b.Gross
				cross[b.RateBP] = c
			}
			check("method x rate cross-tab", cross)

			from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			for _, read := range []struct {
				src string
				fn  func() ([]data.EODTaxBandSale, error)
			}{
				{"tax window", func() ([]data.EODTaxBandSale, error) {
					return repo.SalesForTaxWindow(ctx, from, from.AddDate(900, 0, 0))
				}},
				{"tax bands instant", func() ([]data.EODTaxBandSale, error) {
					return repo.SalesForTaxBandsInstant(ctx, from, from.AddDate(900, 0, 0))
				}},
			} {
				sales, err := read.fn()
				if err != nil {
					t.Fatalf("%s: %v", read.src, err)
				}
				got := map[int]band{}
				for _, b := range pos.EODTaxBandsFromSales(sales) {
					got[b.RateBP] = band{b.Net, b.Tax, b.Gross}
				}
				check(read.src, got)
			}
		})
	}
}

// ut-docs#4301's own repro (exclusive, no discount): lines 16.73 @0%,
// 0.03 @10.7%, 10.64 @19%; a 0.53 levy at a flat 10.7% next to a 4.29
// service charge apportioned at the lines' rates. The VAT bands must carry
// the same per-rate figures fiscal.sign.ask sends the signer.
func TestVATBandsForSale_MatchesFiscalPayloadWithFlatLevy(t *testing.T) {
	for _, inclusive := range []bool{false, true} {
		saleLines := []pos.SaleLineInput{
			{Name: "Zero", Qty: 1, UnitPrice: money.FromMinor(1673), TaxRateBasisPoints: 0},
			{Name: "Reduced", Qty: 1, UnitPrice: money.FromMinor(3), TaxRateBasisPoints: 1070},
			{Name: "Standard", Qty: 1, UnitPrice: money.FromMinor(1064), TaxRateBasisPoints: 1900},
		}
		charges := []pos.ChargeInput{
			{Key: "levy", Label: "Levy", Amount: money.FromMinor(53), TaxBasisBP: 1070, Base: pos.ChargeBaseNetLines},
			{Key: pos.ServiceChargeKey, Amount: money.FromMinor(429), Base: pos.ChargeBaseNetLines},
		}
		in := &pos.SaleInput{SaleID: "sale-4301", Currency: "EUR", TaxInclusive: inclusive, Charges: charges, Lines: saleLines}
		p := buildFiscalSignPayload(in, time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC))

		var lines []pos.VATLine
		for _, l := range saleLines {
			tax, gross := pos.ComputeTaxBasisPoints(l.UnitPrice, l.TaxRateBasisPoints, inclusive)
			lines = append(lines, pos.VATLine{RateBP: l.TaxRateBasisPoints, LineTotal: gross.Minor(), TaxAmount: tax.Minor()})
		}
		bands := pos.VATBandsForSale(lines, 0, inclusive, charges)
		if len(bands) != len(p.VATBreakdown) {
			t.Fatalf("inclusive=%v: bands %+v vs payload %+v", inclusive, bands, p.VATBreakdown)
		}
		for i, b := range bands {
			v := p.VATBreakdown[i]
			// The payload's net is in the sale's pricing mode: gross when
			// inclusive, net when exclusive.
			value := b.Net
			if inclusive {
				value = b.Gross
			}
			if b.RateBP != v.RateBP || value != v.Net || b.Tax != v.Tax {
				t.Errorf("inclusive=%v: band %+v, payload %+v", inclusive, b, v)
			}
		}
	}
}
