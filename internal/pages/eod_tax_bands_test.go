package pages

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// Day-close per-VAT-rate breakdown tests (ut-docs#1003). The golden-day and
// range tests moved here from internal/data/pos_repo_lifecycle_test.go when
// the band computation moved out of dateRangeSummary (see eod_tax_bands.go);
// the service-charge and whole-sale-discount tests are NEW and deliberately
// go through the REAL pos.CompleteSale — not hand-inserted rows — because
// the original SQL-only implementation passed its hand-inserted fixtures
// while silently dropping both sale-level amounts on every real sale.

func etbOpenDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func etbExec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.DB.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

// etbAt renders a time in the RFC3339 format production writes to created_at.
func etbAt(tm time.Time) string { return tm.UTC().Format("2006-01-02T15:04:05Z") }

// etbDay computes the local-calendar-day grouping key for tm via SQLite's
// own date(..., 'localtime') — the same modifier the production window
// uses — so assertions hold in every host timezone (ut-docs#559/#869
// convention, mirrors internal/data's b8ExpectedDay).
func etbDay(t *testing.T, d *db.DB, tm time.Time) string {
	t.Helper()
	var day string
	if err := d.DB.QueryRow(`SELECT date(?, 'localtime')`, etbAt(tm)).Scan(&day); err != nil {
		t.Fatalf("control day query: %v", err)
	}
	return day
}

func etbItem(t *testing.T, d *db.DB, id string, basePrice int64) {
	t.Helper()
	etbExec(t, d, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES (?, ?, ?, ?, 1)`,
		id, "SKU-"+id, "Name "+id, basePrice)
}

// etbSale inserts a sale row; subtotal mirrors total (no discount/charge —
// the hand-inserted fixtures exercise the pure line-aggregation math; sales
// WITH a discount or service charge go through pos.CompleteSale below).
func etbSale(t *testing.T, d *db.DB, id, createdAt, status, saleType string, taxTotal, total int64) {
	t.Helper()
	etbExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
VALUES (?, ?, ?, ?, 'GBP', ?, 0, ?, ?, ?)`, id, "R-"+id, status, saleType, total, taxTotal, total, createdAt)
}

func etbLine(t *testing.T, d *db.DB, saleID string, lineNo int, itemID, name string, qty float64, rateBP, taxAmt, before, after int64) {
	t.Helper()
	etbExec(t, d, `INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		fmt.Sprintf("%s-l%d", saleID, lineNo), saleID, lineNo, itemID, name, qty, after, rateBP, taxAmt, before, after)
}

// etbEndOfDay runs the REAL production pair — POSRepo.EndOfDay +
// attachEODTaxBands — exactly as generateEOD does.
func etbEndOfDay(t *testing.T, d *db.DB, day string) data.EODReport {
	t.Helper()
	repo := data.NewPOSRepo(d.DB)
	rep, err := repo.EndOfDay(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachEODTaxBands(context.Background(), repo, &rep); err != nil {
		t.Fatalf("attachEODTaxBands: %v", err)
	}
	return rep
}

// assertEODTaxBandIdentities pins the internal-consistency contract of
// EODReport.TaxBands (ut-docs#1003): the per-rate rows must add up EXACTLY
// (integer minor units, no re-rounding anywhere) to the report-level totals
// computed independently from the sales table by dateRangeSummary:
//
//	sum(band.Tax)   == rep.TaxNet          (tax net of returns)
//	sum(band.Gross) == rep.Net             (tax-inclusive, net of refunds)
//	sum(band.Net)   == rep.Net - rep.TaxNet (pre-tax net total)
//
// A Z-report whose printed rows don't add to its printed totals is legally
// unusable — asserted here rather than inspected.
func assertEODTaxBandIdentities(t *testing.T, rep data.EODReport) {
	t.Helper()
	var sumNet, sumTax, sumGross int64
	for _, b := range rep.TaxBands {
		if b.Gross != b.Net+b.Tax {
			t.Fatalf("band %d: Gross %d != Net %d + Tax %d", b.RateBP, b.Gross, b.Net, b.Tax)
		}
		sumNet += b.Net
		sumTax += b.Tax
		sumGross += b.Gross
	}
	if sumTax != rep.TaxNet {
		t.Fatalf("sum of band tax %d != report TaxNet %d", sumTax, rep.TaxNet)
	}
	if sumGross != rep.Net {
		t.Fatalf("sum of band gross %d != report Net %d", sumGross, rep.Net)
	}
	if sumNet != rep.Net-rep.TaxNet {
		t.Fatalf("sum of band net %d != report Net-TaxNet %d", sumNet, rep.Net-rep.TaxNet)
	}
}

func TestEndOfDay_TaxBands_PerRateNetTaxGross(t *testing.T) {
	d := etbOpenDB(t, "eod-bands-golden.db")

	// Local-noon-anchored, host-timezone-safe (ut-docs#869).
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	at := etbAt(today)

	etbItem(t, d, "vat-itm", 100) // shared FK target for lines

	// Reference day from the ut-docs#1003 card (all figures minor units):
	//   7% band: net 963.36, tax 67.44, gross 1030.80
	//  19% band: net 150.92, tax 28.68, gross 179.60
	//   0% band: net  15.00, tax  0.00, gross  15.00
	// Grand: tax 96.12, net(pre-tax) 1129.28, gross(tax-incl) 1225.40.
	//
	// Sale 1: the bulk of the 7% band, the whole 0% band, plus two
	// zero-value "note" marker lines (price 0.00, arbitrary nonzero rates)
	// that must NOT invent spurious bands.
	etbSale(t, d, "vat-s1", at, "completed", "sale", 6744, 104580)
	etbLine(t, d, "vat-s1", 1, "vat-itm", "Groceries 7%", 1, 700, 6744, 96336, 103080)
	etbLine(t, d, "vat-s1", 2, "vat-itm", "Zero-rated", 1, 0, 0, 1500, 1500)
	etbLine(t, d, "vat-s1", 3, "vat-itm", "-- note --", 1, 500, 0, 0, 0)
	etbLine(t, d, "vat-s1", 4, "vat-itm", "-- another note --", 1, 2300, 0, 0, 0)

	// Sale 2: the whole 19% band, plus an extra 7% line that the return
	// below cancels out exactly — proving the return reduces the CORRECT
	// band, not another one.
	etbSale(t, d, "vat-s2", at, "completed", "sale", 3078, 21170)
	etbLine(t, d, "vat-s2", 1, "vat-itm", "Standard 19%", 1, 1900, 2868, 15092, 17960)
	etbLine(t, d, "vat-s2", 2, "vat-itm", "Groceries 7%", 1, 700, 210, 3000, 3210)

	// Return in the 7% band only.
	etbSale(t, d, "vat-r1", at, "completed", "return", 210, 3210)
	etbLine(t, d, "vat-r1", 1, "vat-itm", "Groceries 7%", 1, 700, 210, 3000, 3210)

	// Voided sale: excluded entirely (would corrupt the 19% band otherwise).
	etbSale(t, d, "vat-v1", at, "voided", "sale", 999, 999)
	etbLine(t, d, "vat-v1", 1, "vat-itm", "Voided", 1, 1900, 999, 999, 999)

	rep := etbEndOfDay(t, d, etbDay(t, d, today))

	// Exactly 3 bands, ascending by rate — the note lines' 5%/23% rates
	// must not appear, and the 0% band must appear on its own (a real
	// zero-rated sale, never folded into another band).
	if len(rep.TaxBands) != 3 {
		t.Fatalf("expected 3 tax bands (0%%, 7%%, 19%%), got %+v", rep.TaxBands)
	}
	want := []data.TaxBand{
		{RateBP: 0, Net: 1500, Tax: 0, Gross: 1500},
		{RateBP: 700, Net: 96336, Tax: 6744, Gross: 103080},
		{RateBP: 1900, Net: 15092, Tax: 2868, Gross: 17960},
	}
	for i, w := range want {
		if rep.TaxBands[i] != w {
			t.Fatalf("band[%d] = %+v, want %+v", i, rep.TaxBands[i], w)
		}
	}
	for _, b := range rep.TaxBands {
		if b.RateBP == 500 || b.RateBP == 2300 {
			t.Fatalf("zero-value note lines invented a spurious %d bp band: %+v", b.RateBP, rep.TaxBands)
		}
	}

	// Grand totals per the card: tax 96.12, pre-tax net 1129.28,
	// tax-inclusive gross 1225.40 — via the report's own fields, then the
	// band-sum identities against them.
	if rep.TaxNet != 9612 {
		t.Fatalf("expected TaxNet 9612, got %d", rep.TaxNet)
	}
	if rep.Net != 122540 {
		t.Fatalf("expected Net (tax-inclusive, net of refunds) 122540, got %d", rep.Net)
	}
	assertEODTaxBandIdentities(t, rep)
}

func TestEndOfDayRange_TaxBandsAcrossDays(t *testing.T) {
	d := etbOpenDB(t, "eod-bands-range.db")

	now := time.Now()
	day1 := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	day2 := day1.AddDate(0, 0, 1)

	etbItem(t, d, "vat-itm", 100)

	etbSale(t, d, "vat-d1", etbAt(day1), "completed", "sale", 1900, 11900)
	etbLine(t, d, "vat-d1", 1, "vat-itm", "Standard 19%", 1, 1900, 1900, 10000, 11900)

	etbSale(t, d, "vat-d2", etbAt(day2), "completed", "sale", 1090, 8090)
	etbLine(t, d, "vat-d2", 1, "vat-itm", "Standard 19%", 1, 1900, 950, 5000, 5950)
	etbLine(t, d, "vat-d2", 2, "vat-itm", "Groceries 7%", 1, 700, 140, 2000, 2140)

	repo := data.NewPOSRepo(d.DB)
	rep, err := repo.EndOfDayRange(context.Background(), etbDay(t, d, day1), etbDay(t, d, day2))
	if err != nil {
		t.Fatal(err)
	}
	if err := attachEODTaxBands(context.Background(), repo, &rep); err != nil {
		t.Fatalf("attachEODTaxBands: %v", err)
	}

	// Bands merge across the whole range (same as Methods — not gated on
	// from == to), ascending by rate.
	want := []data.TaxBand{
		{RateBP: 700, Net: 2000, Tax: 140, Gross: 2140},
		{RateBP: 1900, Net: 15000, Tax: 2850, Gross: 17850},
	}
	if len(rep.TaxBands) != 2 {
		t.Fatalf("expected 2 tax bands over the range, got %+v", rep.TaxBands)
	}
	for i, w := range want {
		if rep.TaxBands[i] != w {
			t.Fatalf("band[%d] = %+v, want %+v", i, rep.TaxBands[i], w)
		}
	}
	assertEODTaxBandIdentities(t, rep)
}

// etbCompleteSale runs a REAL sale through pos.CompleteSale and returns the
// local calendar day it landed on (read back from the persisted row via
// SQLite's own 'localtime', so the assertion window is exactly the one the
// production query matches).
func etbCompleteSale(t *testing.T, d *db.DB, in pos.SaleInput) string {
	t.Helper()
	saleID, err := pos.CompleteSale(context.Background(), d.DB, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var day string
	if err := d.DB.QueryRow(`SELECT date(created_at, 'localtime') FROM sales WHERE id = ?`, saleID).Scan(&day); err != nil {
		t.Fatalf("sale day: %v", err)
	}
	return day
}

// A sale with a SERVICE CHARGE, through the real engine: the charge and its
// ADR-0061 tax live only on the sales row (no sale_lines row), so the
// original SQL-only band aggregation dropped both — sum(band.Tax) came up
// short of rep.TaxNet by exactly the charge's tax (here 160) and
// sum(band.Gross) short of rep.Net by the charge (1000): a Z-Bon whose rows
// didn't add to its own totals, under-declaring VAT collected on the charge.
//
// €11.90 @19% inclusive + €10.00 service charge (apportioned basis):
// line tax 190; charge tax 160 (net 840); TaxNet 350, Net/Total 2190.
func TestEODTaxBands_ServiceChargeSaleThroughCompleteSale(t *testing.T) {
	d := etbOpenDB(t, "eod-bands-charge.db")
	etbItem(t, d, "itm-sc", 1190)

	day := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		ServiceCharge:          money.FromMinor(1000),
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-sc", Name: "Dinner", Qty: 1,
			UnitPrice: money.FromMinor(1190), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2190)}},
	})

	rep := etbEndOfDay(t, d, day)
	if rep.TaxNet != 350 || rep.Net != 2190 {
		t.Fatalf("engine totals moved: TaxNet=%d Net=%d, want 350/2190", rep.TaxNet, rep.Net)
	}
	if len(rep.TaxBands) != 1 {
		t.Fatalf("expected 1 band, got %+v", rep.TaxBands)
	}
	if want := (data.TaxBand{RateBP: 1900, Net: 1840, Tax: 350, Gross: 2190}); rep.TaxBands[0] != want {
		t.Fatalf("band = %+v, want %+v (line 1000/190/1190 + charge 840/160/1000)", rep.TaxBands[0], want)
	}
	assertEODTaxBandIdentities(t, rep)
}

// A sale with a WHOLE-SALE discount, through the real engine: the discount
// lives on sales.discount_total / sale_discounts and is never distributed
// into any sale_lines row, so the original SQL-only aggregation reported
// sum(band.Gross) over rep.Net by exactly the discount (here 190).
//
// €11.90 @19% exclusive (net 10.00, tax 1.90) with a €1.90 whole-sale
// discount: total 1000, TaxNet 190 — the band must prorate the discount
// off the net (exclusive engine convention) to 810/190/1000.
func TestEODTaxBands_WholeSaleDiscountThroughCompleteSale(t *testing.T) {
	d := etbOpenDB(t, "eod-bands-discount.db")
	etbItem(t, d, "itm-disc", 1000)

	day := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: false,
		SaleDiscount:           money.FromMinor(190),
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-disc", Name: "Widget", Qty: 1,
			UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})

	rep := etbEndOfDay(t, d, day)
	if rep.TaxNet != 190 || rep.Net != 1000 {
		t.Fatalf("engine totals moved: TaxNet=%d Net=%d, want 190/1000", rep.TaxNet, rep.Net)
	}
	if len(rep.TaxBands) != 1 {
		t.Fatalf("expected 1 band, got %+v", rep.TaxBands)
	}
	if want := (data.TaxBand{RateBP: 1900, Net: 810, Tax: 190, Gross: 1000}); rep.TaxBands[0] != want {
		t.Fatalf("band = %+v, want %+v (net 1000 - discount 190, tax kept)", rep.TaxBands[0], want)
	}
	assertEODTaxBandIdentities(t, rep)
}

// fmtRateBP is defensive on negatives (nothing validates tax_rate_bp >= 0
// anywhere): -50 used to render as the garbage "0.-5%".
func TestFmtRateBP(t *testing.T) {
	cases := map[int]string{
		0:     "0%",
		700:   "7%",
		1900:  "19%",
		1050:  "10.5%",
		1005:  "10.05%",
		-500:  "-5%",
		-50:   "-0.5%",
		-1050: "-10.5%",
	}
	for bp, want := range cases {
		if got := fmtRateBP(bp); got != want {
			t.Errorf("fmtRateBP(%d) = %q, want %q", bp, got, want)
		}
	}
}

// A £1,000,000.00+ figure is 13 characters — under the old fixed 6/11/10/11
// column layout the row ran past print.Width (42) and the ESC/POS renderer
// clipped the gross's trailing digits off the printed Z-report (2026-08-25
// review finding). The adaptive layout collapses column padding so every
// digit still prints.
func TestBuildEODDoc_VATRateBandWideAmountsNotClipped(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-08-25", GeneratedAt: "2026-08-25T21:30:00Z",
		Net: 100000000, TaxNet: 15966387,
		TaxBands: []data.TaxBand{
			{RateBP: 1900, Net: 84033613, Tax: 15966387, Gross: 100000000},
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	for _, want := range []string{"£840,336.13", "£159,663.87", "£1,000,000.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide VAT band figure %q clipped from the Z-report:\n%s", want, out)
		}
	}
	// Row-level: the band ROW itself must carry every column. Contains()
	// over the whole doc is not enough — this fixture's NET/Tax totals
	// lines repeat two of the three figures, which masked a real clip when
	// column widths were measured in bytes while fmt pads and the renderer
	// clips in runes (each £ charged a phantom column; found during
	// ut-docs#1004).
	bandRow := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "£840,336.13") {
			bandRow = l
			break
		}
	}
	if bandRow == "" || !strings.Contains(bandRow, "£1,000,000.00") {
		t.Errorf("VAT band row lost its gross column: %q\n%s", bandRow, out)
	}
}
