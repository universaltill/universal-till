package pages

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func TestVATBreakdownGroupsByRecordedRate(t *testing.T) {
	sale := data.SaleDetail{Lines: []data.SaleDetailLine{
		{TaxRateBP: 2000, LineTotal: 120, TaxAmount: 20},
		{TaxRateBP: 2000, LineTotal: 240, TaxAmount: 40},
		{TaxRateBP: 0, LineTotal: 100, TaxAmount: 0},
	}}
	bands := vatBreakdown(sale)
	if len(bands) != 2 {
		t.Fatalf("expected 2 bands, got %v", bands)
	}
	if bands[0].RateBP != 0 || bands[0].Net != 100 || bands[0].Tax != 0 {
		t.Fatalf("zero band wrong: %+v", bands[0])
	}
	if bands[1].RateBP != 2000 || bands[1].Net != 300 || bands[1].Tax != 60 || bands[1].Gross != 360 {
		t.Fatalf("20%% band wrong: %+v", bands[1])
	}
}

// ADR-0060 (reviewer finding, 2026-08-24): once core taxes the service
// charge, an invoice whose VAT table is built from lines alone declares
// LESS VAT than the sale collected, and its gross stops matching the sale
// total. The charge's apportioned net/tax must land in the bands — via the
// same shared pos.ApportionServiceChargeTax the tender path uses — in both
// pricing modes.
func TestVATBreakdownApportionsServiceCharge(t *testing.T) {
	t.Run("exclusive", func(t *testing.T) {
		// One line: net 100 @20% (tax 20). Service charge 10, taxed at the
		// sale's own 20% = 2. Total 100 + 10 + 22 = 132.
		sale := data.SaleDetail{
			Subtotal: 100, TaxTotal: 22, Total: 132, ServiceCharge: 10,
			Lines: []data.SaleDetailLine{{TaxRateBP: 2000, LineTotal: 120, TaxAmount: 20}},
		}
		bands := vatBreakdown(sale)
		if len(bands) != 1 {
			t.Fatalf("expected 1 band, got %+v", bands)
		}
		if bands[0].Net != 110 || bands[0].Tax != 22 || bands[0].Gross != 132 {
			t.Fatalf("charge not apportioned into the band: %+v (want net 110 / tax 22 / gross 132)", bands[0])
		}
		assertBandsMatchSale(t, bands, sale)
	})

	t.Run("inclusive", func(t *testing.T) {
		// One line: gross 120 incl 20% (net 100, tax 20). Service charge 10,
		// inclusive, so its own 2 of tax sits INSIDE it. Total 120 + 10 = 130.
		sale := data.SaleDetail{
			Subtotal: 120, TaxTotal: 22, Total: 130, ServiceCharge: 10,
			Lines: []data.SaleDetailLine{{TaxRateBP: 2000, LineTotal: 120, TaxAmount: 20}},
		}
		if !saleIsTaxInclusive(sale) {
			t.Fatal("an inclusive sale carrying a service charge must still read as inclusive")
		}
		bands := vatBreakdown(sale)
		if len(bands) != 1 {
			t.Fatalf("expected 1 band, got %+v", bands)
		}
		if bands[0].Net != 108 || bands[0].Tax != 22 || bands[0].Gross != 130 {
			t.Fatalf("charge not apportioned into the band: %+v (want net 108 / tax 22 / gross 130)", bands[0])
		}
		assertBandsMatchSale(t, bands, sale)
	})

	t.Run("flat basis from the originating till is honoured", func(t *testing.T) {
		// Same exclusive sale, but the till's country plugin fixed a flat 7%
		// basis, persisted on the sale (migration 062): charge tax 1, not 2.
		sale := data.SaleDetail{
			Subtotal: 100, TaxTotal: 21, Total: 131, ServiceCharge: 10,
			ServiceChargeTaxBasisBP: 700,
			Lines:                   []data.SaleDetailLine{{TaxRateBP: 2000, LineTotal: 120, TaxAmount: 20}},
		}
		bands := vatBreakdown(sale)
		var tax int64
		for _, b := range bands {
			tax += b.Tax
		}
		if tax != 21 {
			t.Fatalf("want total band tax 21 (20 line + 1 at the flat 7%% basis), got %d", tax)
		}
		assertBandsMatchSale(t, bands, sale)
	})
}

// assertBandsMatchSale pins the two invariants an invoice's VAT table must
// always satisfy: every band adds up, and the bands together account for
// exactly what the customer paid.
func assertBandsMatchSale(t *testing.T, bands []vatBand, sale data.SaleDetail) {
	t.Helper()
	var gross, tax int64
	for _, b := range bands {
		if b.Net+b.Tax != b.Gross {
			t.Fatalf("band does not add up: %+v", b)
		}
		gross += b.Gross
		tax += b.Tax
	}
	if gross != sale.Total {
		t.Fatalf("bands gross %d != sale total %d", gross, sale.Total)
	}
	if tax != sale.TaxTotal {
		t.Fatalf("bands tax %d != sale tax_total %d", tax, sale.TaxTotal)
	}
}

// A whole-sale discount is not folded into any line — the bands must
// absorb it so the invoice equals what the customer paid (review find).
func TestVATBreakdownProratesSaleDiscount(t *testing.T) {
	t.Run("inclusive", func(t *testing.T) {
		sale := data.SaleDetail{
			Subtotal: 220, DiscountTotal: 22, TaxTotal: 20, Total: 198, // 198 == 220-22 → inclusive
			Lines: []data.SaleDetailLine{
				{TaxRateBP: 2000, LineTotal: 120, TaxAmount: 20},
				{TaxRateBP: 0, LineTotal: 100, TaxAmount: 0},
			},
		}
		bands := vatBreakdown(sale)
		var gross int64
		for _, b := range bands {
			gross += b.Gross
			if b.Net+b.Tax != b.Gross {
				t.Fatalf("band does not add up: %+v", b)
			}
		}
		if gross != sale.Total {
			t.Fatalf("bands gross %d != sale total %d", gross, sale.Total)
		}
		if bands[1].RateBP != 2000 || bands[1].Gross != 108 || bands[1].Tax != 18 {
			t.Fatalf("20%% band wrong after discount: %+v", bands[1])
		}
	})
	t.Run("exclusive", func(t *testing.T) {
		sale := data.SaleDetail{
			Subtotal: 100, DiscountTotal: 10, TaxTotal: 20, Total: 110, // 110 != 100-10 → exclusive
			Lines: []data.SaleDetailLine{
				{TaxRateBP: 2000, LineTotal: 120, TaxAmount: 20},
			},
		}
		bands := vatBreakdown(sale)
		if len(bands) != 1 || bands[0].Net != 90 || bands[0].Tax != 20 || bands[0].Gross != 110 {
			t.Fatalf("exclusive discount wrong: %+v", bands)
		}
		if bands[0].Gross != sale.Total {
			t.Fatalf("band gross %d != sale total %d", bands[0].Gross, sale.Total)
		}
	})
}

// Invoice numbers are gapless per series and the display number carries
// the till prefix — the legal core of the feature.
func TestInvoiceNumberingPerSeries(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "inv.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	// invoices.sale_id has an FK — use seeded demo sales? None exist.
	// Insert two minimal sales to hang invoices off.
	for _, id := range []string{"s1", "s2", "s3"} {
		if _, err := d.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, currency)
			VALUES (?, ?, 'completed', 'sale', 100, 20, 120, 'GBP')`, id, "R-"+id); err != nil {
			t.Fatalf("seed sale: %v", err)
		}
	}
	repo := data.NewInvoiceRepo(d.DB)
	mk := func(series, saleID, kind string) data.InvoiceRow {
		row, err := repo.Create(ctx, data.InvoiceInput{
			Series: series, Kind: kind, SaleID: saleID, CustomerName: "ACME",
			SellerJSON: "{}", VATBreakdownJSON: "[]",
			NetTotal: 100, TaxTotal: 20, GrossTotal: 120,
			IssuedAt: "2026-07-15T00:00:00Z", IssuedBy: "test",
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return row
	}
	a := mk("", "s1", "invoice")
	b := mk("", "s2", "invoice")
	c := mk("T2-", "s3", "invoice")
	if a.DisplayNo != "INV-000001" || b.DisplayNo != "INV-000002" {
		t.Fatalf("primary series wrong: %s %s", a.DisplayNo, b.DisplayNo)
	}
	if c.DisplayNo != "T2-INV-000001" {
		t.Fatalf("replica series wrong: %s", c.DisplayNo)
	}
	// One invoice per sale, ever.
	if _, err := repo.Create(ctx, data.InvoiceInput{
		Series: "", Kind: "invoice", SaleID: "s1", CustomerName: "X",
		SellerJSON: "{}", VATBreakdownJSON: "[]",
		IssuedAt: "2026-07-15T00:00:00Z", IssuedBy: "test",
	}); err == nil {
		t.Fatal("duplicate invoice for the same sale was allowed")
	}
	// But a credit note for the same sale is fine.
	if _, exists, _ := repo.BySale(ctx, "s1", "invoice"); !exists {
		t.Fatal("BySale lost the invoice")
	}
	if got, ok, _ := repo.ByDisplayNo(ctx, "T2-INV-000001"); !ok || got.ID != c.ID {
		t.Fatal("ByDisplayNo lookup failed")
	}
}
