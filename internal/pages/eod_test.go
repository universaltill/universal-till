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
		FirstReceipt: "000000001", LastReceipt: "000000013",
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8")))
	for _, want := range []string{
		"END OF DAY 2026-07-14", "Sales (12)", "Refunds (1)", "NET",
		"£145.00", "Cash", "£85.00", // 9000 in − 500 out
		"Receipts 000000001 - 000000013",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Z-report missing %q", want)
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
