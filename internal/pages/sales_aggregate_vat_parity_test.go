package pages

import (
	"context"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#2535: the cloud sales-aggregate's by_vat_rate for a till must be
// the Z-report's own VAT table when that till is the shop's only one — the
// upload bands the till-scoped read (data.SalesForTaxBandsForTill) through
// the same pos.EODTaxBandsFromSales the day-close uses, never a parallel sum
// over sale_lines. Sales go through the REAL engine (service charge,
// whole-sale discount, two rates, a return) — exactly the shapes a naive
// sale_lines sum gets wrong.
func TestSalesAggregateVATBands_MatchEODForSingleTill(t *testing.T) {
	d := etbOpenDB(t, "sales-aggregate-vat-parity.db")
	etbItem(t, d, "itm-a", 1190)
	etbItem(t, d, "itm-b", 535)
	ctx := context.Background()

	day := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		ServiceCharge:          money.FromMinor(1000),
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{
			{ItemID: "itm-a", Name: "A", Qty: 1, UnitPrice: money.FromMinor(1190), TaxRateBasisPoints: 1900, LocationID: "loc_main"},
			{ItemID: "itm-b", Name: "B", Qty: 2, UnitPrice: money.FromMinor(535), TaxRateBasisPoints: 700, LocationID: "loc_main"},
		},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(3260)}},
	})
	etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		SaleDiscount:           money.FromMinor(190),
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{
			{ItemID: "itm-a", Name: "A", Qty: 1, UnitPrice: money.FromMinor(1190), TaxRateBasisPoints: 1900, LocationID: "loc_main"},
		},
		Payments: []pos.PaymentInput{{MethodID: "card", Amount: money.FromMinor(1000)}},
	})
	etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "return", Currency: "EUR", TaxInclusive: true,
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{
			{ItemID: "itm-b", Name: "B", Qty: 1, UnitPrice: money.FromMinor(535), TaxRateBasisPoints: 700, LocationID: "loc_main"},
		},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(535)}},
	})

	rep := etbEndOfDay(t, d, day)
	if len(rep.TaxBands) != 2 {
		t.Fatalf("fixture must span two rates, got %+v", rep.TaxBands)
	}

	repo := data.NewPOSRepo(d.DB)
	keys, err := repo.SalesAggregateKeys(ctx, day, day, "self-till")
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %+v, err %v; want exactly one till", keys, err)
	}
	sales, err := repo.SalesForTaxBandsForTill(ctx, day, keys[0].TillID, "self-till")
	if err != nil {
		t.Fatalf("SalesForTaxBandsForTill: %v", err)
	}
	if got := pos.EODTaxBandsFromSales(sales); !reflect.DeepEqual(got, rep.TaxBands) {
		t.Fatalf("till-scoped bands %+v\n!= Z-report bands %+v", got, rep.TaxBands)
	}
}
