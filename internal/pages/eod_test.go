package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/print"
)

func at(hhmm string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04", "2026-07-14 "+hhmm)
	return t
}

func TestEODDue(t *testing.T) {
	cases := []struct {
		name    string
		now     time.Time
		enabled bool
		hhmm    string
		done    bool
		want    bool
	}{
		{"before closing time", at("18:00"), true, "21:30", false, false},
		{"at closing time", at("21:30"), true, "21:30", false, true},
		{"after closing time (catch-up at boot)", at("23:55"), true, "21:30", false, true},
		{"already generated", at("22:00"), true, "21:30", true, false},
		{"disabled", at("22:00"), false, "21:30", false, false},
		{"bad time string", at("22:00"), true, "9pm", false, false},
		{"empty time", at("22:00"), true, "", false, false},
	}
	for _, c := range cases {
		if got := eodDue(c.now, c.enabled, c.hhmm, c.done); got != c.want {
			t.Errorf("%s: eodDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildEODDoc(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 12, Gross: 15000, RefundCount: 1, RefundTotal: 500,
		Net: 14500, TaxNet: 2400,
		Methods:      []data.EODMethod{{Method: "cash", In: 9000, Out: 500}, {Method: "card", In: 6000}},
		Tips:         []data.EODTip{{Method: "card", Count: 4, Amount: 320}},
		FirstReceipt: "000000001", LastReceipt: "000000013",
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	for _, want := range []string{
		"END OF DAY 2026-07-14", "Sales (12)", "Refunds (1)", "NET",
		"£145.00", "Cash", "£85.00", // 9000 in − 500 out
		// ut-docs#1007: tips print separately, held out of revenue —
		// "4x Card" + total £3.20, never folded into the Cash/Card
		// payment-method rows above.
		"TIPS", "4x Card", "£3.20",
		"Receipts 000000001 - 000000013",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Z-report missing %q", want)
		}
	}
	// No voucher activity -> no GUTSCHEINE section at all (ut-docs#1008).
	if strings.Contains(out, "GUTSCHEINE") {
		t.Errorf("Z-report shows a voucher section with no voucher activity")
	}
	if strings.Contains(out, "BY VAT RATE") {
		t.Error("no TaxBands set — VAT footer section must be omitted")
	}
	// ut-docs#1012: zero cancellations -> the STORNOS section is entirely
	// absent, same "omitted rather than a permanent zero line" convention
	// as GUTSCHEINE/TIPS — never a bare "STORNOS ... £0.00" on every report.
	if strings.Contains(out, "STORNOS") {
		t.Error("zero cancellations must omit the STORNOS section entirely")
	}
	// No GeneratedBy/Annotation set -> neither footer line prints.
	if strings.Contains(out, "Erstellt von") || strings.Contains(out, "Anmerkung") {
		t.Error("Z-report with no GeneratedBy/Annotation must not print either footer line")
	}
}

// ut-docs#1012 #1: cancellations (a completed sale later voided/reversed —
// a "Storno") print as their own STORNOS footer section, separate from
// Refunds (a formal return processed afterward — "Retoure"), and OUTSIDE
// doc.Totals — an independent review found the original Totals-row design
// made a fiscal document's own top-to-bottom arithmetic look wrong (a
// reader summing the visible signed figures above NET would get a
// different number than NET actually shows).
func TestBuildEODDoc_Cancellations(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-08-20", GeneratedAt: "2026-08-20T21:30:00Z",
		SalesCount: 5, Gross: 20000, RefundCount: 1, RefundTotal: 500,
		CancelCount: 2, CancelTotal: 1000,
		Net: 19500, TaxNet: 3000,
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	for _, want := range []string{"STORNOS", "Voided (2)", "£10.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("Z-report missing %q in:\n%s", want, out)
		}
	}
	// Never folded into the Totals block — NET must still read 195.00
	// (200.00 gross - 5.00 refund; cancellations play no part), and the
	// visible Totals lines must sum to that on their own.
	if !strings.Contains(out, "£195.00") {
		t.Errorf("NET must be unaffected by CancelTotal, got:\n%s", out)
	}
}

// ut-docs#1012 #2: GeneratedBy/Annotation print as footer lines matching
// the reference Z-Bon's own "Erstellt von" / "Anmerkung" vocabulary.
// Annotation is optional — omitted entirely when blank, even when
// GeneratedBy is set.
func TestBuildEODDoc_GeneratedByAndAnnotation(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-08-20", GeneratedAt: "2026-08-20T21:30:00Z",
		SalesCount: 1, Gross: 500, Net: 500,
		GeneratedBy: "Jane Manager",
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	if !strings.Contains(out, "Erstellt von: Jane Manager") {
		t.Errorf("Z-report missing GeneratedBy footer line, got:\n%s", out)
	}
	if strings.Contains(out, "Anmerkung") {
		t.Error("no Annotation set — that footer line must be omitted")
	}

	// Kept under print.Width (42 cols) -- a footer line is truncated to
	// the printer's physical width like every other footer line in this
	// file, not wrapped, so a longer note would legitimately clip here;
	// that's a print-width constraint shared by the whole file, not
	// something this test is about.
	rep.Annotation = "till reconciled OK"
	out = string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	if !strings.Contains(out, "Anmerkung: till reconciled OK") {
		t.Errorf("Z-report missing Annotation footer line, got:\n%s", out)
	}
}

// ut-docs#1008: voucher flows print as their own GUTSCHEINE footer section —
// separate from (never summed into) the article/department figures, same
// footer precedent as BY DEPARTMENT / BY TILL.
func TestBuildEODDoc_VoucherSection(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 2, Gross: 5000, Net: 5000, TaxNet: 700,
		VouchersIssuedCount: 1, VouchersIssued: 1500,
		VouchersRedeemedCount: 2, VouchersRedeemed: 800,
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	for _, want := range []string{
		"GUTSCHEINE",
		"Issued (1)", "£15.00",
		"Redeemed (2)", "£8.00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Z-report voucher section missing %q\n%s", want, out)
		}
	}
}

// TestBuildEODDoc_VATRateBands: the German day-close's per-rate breakdown
// (ut-docs#1003) prints as a footer section, same precedent as the
// Departments/Tills footers — one line per band: rate, net, tax, gross.
func TestBuildEODDoc_VATRateBands(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-08-24", GeneratedAt: "2026-08-24T21:30:00Z",
		SalesCount: 3, Gross: 125750, RefundCount: 1, RefundTotal: 3210,
		Net: 122540, TaxNet: 9612,
		TaxBands: []data.TaxBand{
			{RateBP: 0, Net: 1500, Tax: 0, Gross: 1500},
			{RateBP: 700, Net: 96336, Tax: 6744, Gross: 103080},
			{RateBP: 1900, Net: 15092, Tax: 2868, Gross: 17960},
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	if !strings.Contains(out, "BY VAT RATE") {
		t.Fatalf("Z-report missing the VAT rate footer section:\n%s", out)
	}
	for _, want := range []string{
		"0%", "7%", "19%", // rates formatted as percentages, not basis points
		"£963.36", "£67.44", "£1,030.80", // 7% band: net, tax, gross
		"£150.92", "£28.68", "£179.60", // 19% band
		"£15.00", // 0% band
	} {
		if !strings.Contains(out, want) {
			t.Errorf("VAT band footer missing %q in:\n%s", want, out)
		}
	}
}

// TestBuildEODDoc_CashReconciliation reproduces ut-docs#1002's de-identified
// reference day-close block (ut-docs#1006): opening float 100.00, cash
// takings 411.10, calculated 511.10, counted 511.10, variance 0.00, skim
// -411.10, new float 100.00.
func TestBuildEODDoc_CashReconciliation(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 12, Gross: 41110, Net: 41110,
		Methods: []data.EODMethod{{Method: "cash", In: 41110}},
		CashReconciliation: &data.CashReconciliation{
			OpeningFloat: 10000,
			CashSales:    41110,
			Calculated:   51110,
			Counted:      51110,
			Variance:     0,
			Skim:         -41110,
			NewFloat:     10000,
			ShiftsClosed: 1,
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	for _, want := range []string{
		"CASH RECONCILIATION",
		"Opening float", "£100.00",
		"Cash sales", "£411.10",
		"Calculated", "£511.10",
		"Counted",
		"Variance", "£0.00",
		"Skim to safe", "-£411.10",
		"New float",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cash reconciliation block missing %q", want)
		}
	}
	if strings.Contains(out, "!!") {
		t.Error("zero variance must not carry the !! marker")
	}
	// No cash tips (TipsHeldOut zero value): no "Tips held out" line at all
	// -- same "no line beats a permanent -£0.00 line" convention as
	// GUTSCHEINE/STORNOS (ut-docs#1046).
	if strings.Contains(out, "Tips held out") {
		t.Error("Tips held out line must be omitted when TipsHeldOut is zero")
	}

	// A non-zero variance is flagged so it can't be missed on paper.
	rep.CashReconciliation.Counted = 51000
	rep.CashReconciliation.Variance = -110
	out = string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	if !strings.Contains(out, "!!") {
		t.Error("non-zero variance must be flagged with !!")
	}
	if !strings.Contains(out, "-£1.10") {
		t.Error("negative variance must render signed (-£1.10)")
	}

	// No reconciliation (no shift closed that day): the section is absent
	// and the report still renders — day-close is never blocked on it.
	rep.CashReconciliation = nil
	out = string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	if strings.Contains(out, "CASH RECONCILIATION") {
		t.Error("section must be omitted when no reconciliation exists")
	}
}

// A cash tip held out of CashSales (ut-docs#1046) prints its own "Tips held
// out" line inside CASH RECONCILIATION, positioned right after Cash sales.
func TestBuildEODDoc_CashReconciliation_TipsHeldOut(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 1, Gross: 40000, Net: 40000,
		Methods: []data.EODMethod{{Method: "cash", In: 42000}},
		Tips:    []data.EODTip{{Method: "cash", Count: 1, Amount: 2000}},
		CashReconciliation: &data.CashReconciliation{
			OpeningFloat: 10000,
			CashSales:    40000,
			TipsHeldOut:  2000,
			Calculated:   52000,
			Counted:      52000,
			ShiftsClosed: 1,
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	if !strings.Contains(out, "Tips held out") || !strings.Contains(out, "£20.00") {
		t.Errorf("expected a Tips held out £20.00 line, got:\n%s", out)
	}
	// Must appear inside CASH RECONCILIATION, right after Cash sales —
	// not confused with the separate top-level TIPS BY PAYMENT METHOD
	// section (ut-docs#1007), which reports the same figure independently.
	cashRecIdx := strings.Index(out, "CASH RECONCILIATION")
	tipsHeldIdx := strings.Index(out, "Tips held out")
	if cashRecIdx < 0 || tipsHeldIdx < cashRecIdx {
		t.Error("Tips held out line must be inside the CASH RECONCILIATION block")
	}
}

// A day with zero tipped payments (e.g. a terminal with tipping disabled
// and no cash tips either) must print no TIPS section at all, not an
// empty one — ut-docs#1007.
func TestBuildEODDoc_NoTips(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 1, Gross: 500, Net: 500,
		Methods:      []data.EODMethod{{Method: "cash", In: 500}},
		FirstReceipt: "000000001", LastReceipt: "000000001",
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	if strings.Contains(out, "TIPS") {
		t.Errorf("Z-report with zero tips must not print a TIPS section, got:\n%s", out)
	}
}

func TestRenderTextPreviewLayout(t *testing.T) {
	rd := receiptDesign{
		Header:      []string{"12 High Street", "VAT GB123"},
		Footer:      "See you soon!",
		ShowSKU:     true,
		ShowTax:     false,
		ShowBarcode: true,
	}
	txt := print.RenderText(sampleReceiptDoc("My Shop", rd))
	for _, want := range []string{"My Shop", "12 High Street", "VAT GB123", "[SKU-001]", "See you soon!", "000000123", "TOTAL"} {
		if !strings.Contains(txt, want) {
			t.Errorf("preview missing %q", want)
		}
	}
	if strings.Contains(txt, "Subtotal") || strings.Contains(txt, "Tax") {
		t.Error("ShowTax=false must hide subtotal/tax rows")
	}
	if !strings.Contains(txt, "║█║") {
		t.Error("barcode placeholder missing when enabled")
	}
	rd.ShowBarcode = false
	rd.ShowSKU = false
	txt = print.RenderText(sampleReceiptDoc("My Shop", rd))
	if strings.Contains(txt, "║█║") || strings.Contains(txt, "[SKU-001]") {
		t.Error("disabled barcode/SKU still rendered")
	}
}
