package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5798: the pure X report paper layout.

func xdocReport() data.XReport {
	m := money.FromMinor
	return data.XReport{
		Shift:        data.ShiftSummary{ID: "sh1", RegisterID: "reg1", OpenedAt: "2026-01-02T08:00:00Z", Open: true},
		RegisterName: "Front Till", CashierName: "Alex", GeneratedAt: "2026-01-02T10:00:00Z",
		SalesCount: 3, SalesGross: m(30000), ReturnsCount: 1, ReturnsTotal: m(500), Net: m(29500), Tax: m(4900),
		VoidsCount: 2, VoidsTotal: m(700), ClearedBasketsCount: 1, ClearedBasketsTotal: m(300),
		Methods: []data.XMethodLine{
			{MethodID: "cash", Name: "Cash", Net: m(20000)},
			{MethodID: "m-9", Name: "House account", Net: m(9500)},
		},
		Cash: data.XCash{
			Opening: m(10000), CashSales: m(20000), TipsHeldOut: m(0), AdjustmentsTotal: m(-2000),
			Adjustments: []data.XAdjustment{{Type: "payout", Reason: "Milk", Amount: m(-500)}, {Type: "skim", Amount: m(-1500)}},
			Expected:    m(28000),
		},
		NoSale: 4, FirstReceipt: "R-0001", LastReceipt: "R-0009",
	}
}

func xdocSetup(t *testing.T) {
	t.Helper()
	newShiftsPageTestDeps(t)
	pinDefaultLocale(t, "en")
	pinCurrency(t, "GBP")
}

func TestBuildXReportDoc_ContentNoZMarkers(t *testing.T) {
	xdocSetup(t)
	doc := buildXReportDoc(xdocReport(), "Corner Shop", "utf8")
	if doc.Meta[0] != "X REPORT" || doc.Meta[1] != "Not a day close. Nothing is closed." {
		t.Fatalf("meta = %q", doc.Meta)
	}
	meta := strings.Join(doc.Meta, "\n")
	for _, w := range []string{"Front Till", "Alex", "Generated"} {
		if !strings.Contains(meta, w) {
			t.Errorf("meta lacks %q", w)
		}
	}
	wantTotals := []string{"Sales (3)", "Refunds (1)", "Tax (net)", "NET"}
	if len(doc.Totals) != 4 {
		t.Fatalf("totals = %+v", doc.Totals)
	}
	for i, w := range wantTotals {
		if doc.Totals[i].Label != w {
			t.Errorf("totals[%d] = %q, want %q", i, doc.Totals[i].Label, w)
		}
	}
	if !doc.Totals[3].Strong {
		t.Error("NET must be strong")
	}
	if len(doc.Payments) != 2 || doc.Payments[0].Label != "Cash" || doc.Payments[1].Label != "House account" {
		t.Fatalf("payments = %+v", doc.Payments)
	}
	foot := strings.Join(doc.Footer, "\n")
	for _, w := range []string{"Payout (cash out): Milk", "Skim to safe", "Expected", "£280.00", "Receipts R-0001 - R-0009", "Opening float", "Cash sales", "Tips"} {
		if !strings.Contains(foot, w) {
			t.Errorf("footer lacks %q:\n%s", w, foot)
		}
	}
	if doc.KickDrawer || doc.Barcode != "" || doc.Logo != nil || doc.TSEQR != nil || doc.DrawerPin != 0 {
		t.Errorf("Z-only fields set: %+v", doc)
	}
	txt := print.RenderText(doc)
	if strings.Contains(txt, "END OF DAY") || strings.Contains(txt, "Zeitraum") {
		t.Errorf("Z marker in:\n%s", txt)
	}
}

func TestBuildXReportDoc_NegativeAdjustmentSignFirst(t *testing.T) {
	xdocSetup(t)
	r := xdocReport()
	r.Cash.Adjustments = []data.XAdjustment{{Type: "skim", Amount: money.FromMinor(-2000)}}
	foot := strings.Join(buildXReportDoc(r, "S", "utf8").Footer, "\n")
	if !strings.Contains(foot, "-£20.00") {
		t.Fatalf("footer:\n%s", foot)
	}
}

func TestBuildXReportDoc_ZeroDecimalCurrency(t *testing.T) {
	newShiftsPageTestDeps(t)
	pinDefaultLocale(t, "en")
	pinCurrency(t, "JPY")
	r := xdocReport()
	r.Cash.Expected = money.FromMinor(500)
	foot := strings.Join(buildXReportDoc(r, "S", "utf8").Footer, "\n")
	if !strings.Contains(foot, "500") || strings.Contains(foot, "5.00") {
		t.Fatalf("footer:\n%s", foot)
	}
}

func TestBuildXReportDoc_NoReceiptsOmitsRange(t *testing.T) {
	xdocSetup(t)
	r := xdocReport()
	r.FirstReceipt, r.LastReceipt = "", ""
	if foot := strings.Join(buildXReportDoc(r, "S", "utf8").Footer, "\n"); strings.Contains(foot, "Receipts") {
		t.Fatalf("footer:\n%s", foot)
	}
}
