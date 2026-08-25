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
	if strings.Contains(out, "BY VAT RATE") {
		t.Error("no TaxBands set — VAT footer section must be omitted")
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
