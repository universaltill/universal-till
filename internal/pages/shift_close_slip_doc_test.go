package pages

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/print"
)

// xGoldenPath is resolved once at package init, while the working directory
// is still this package's directory (go test sets it; portable in CI): the
// page test deps chdir later, so a relative path would break.
var xGoldenPath = func() string { wd, _ := os.Getwd(); return filepath.Join(wd, "testdata", "x_report_doc.golden") }()

var updateXGolden = flag.Bool("update-x-golden", false, "rewrite testdata/x_report_doc.golden")

// ut-docs#5841 X1: the X report paper must stay byte-identical through the
// buildShiftSlipDoc refactor.
func TestBuildXReportDoc_GoldenBeforeRefactor(t *testing.T) {
	xdocSetup(t)
	got := fmt.Sprintf("%#v\n", buildXReportDoc(xdocReport(), "Corner Shop", "utf8"))
	path := xGoldenPath
	if *updateXGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("X report paper changed:\n%s\nwant\n%s", got, want)
	}
}

func shiftSlipDocFixture() data.ShiftCloseSlip {
	m := money.FromMinor
	x := xdocReport()
	x.Shift.Open = false
	x.Shift.ClosedAt = "2026-01-02T18:00:00Z"
	x.Cash.Adjustments = []data.XAdjustment{{Type: "payout", Reason: "Milk", Amount: m(-500)}}
	x.Cash.AdjustmentsTotal = m(-500)
	x.Cash.Expected = m(29500)
	return data.ShiftCloseSlip{X: x, Expected: m(29500), Counted: m(29300), Variance: m(-200), Skim: m(20000), NewFloat: m(9300)}
}

func shiftSlipText(t *testing.T, s data.ShiftCloseSlip) (print.Doc, string) {
	t.Helper()
	doc := buildShiftSlipDoc(s, "Corner Shop", "utf8")
	return doc, strings.Join(doc.Meta, "\n") + "\n" + strings.Join(doc.Footer, "\n")
}

func TestBuildShiftSlipDoc_ContentNoZOrXMarkers(t *testing.T) {
	xdocSetup(t)
	doc, txt := shiftSlipText(t, shiftSlipDocFixture())
	if doc.Meta[0] != "SHIFT CLOSE" {
		t.Fatalf("meta = %q", doc.Meta)
	}
	for _, w := range []string{"Front Till", "Alex", "Closed", "Generated", "Expected", "£295.00", "Counted", "£293.00",
		"Variance", "-£2.00", "Skim to safe", "-£200.00", "New float", "£93.00", "Payout (cash out): Milk", "Receipts R-0001 - R-0009"} {
		if !strings.Contains(txt, w) {
			t.Errorf("slip lacks %q:\n%s", w, txt)
		}
	}
	all := print.RenderText(doc)
	for _, bad := range []string{"X REPORT", "Not a day close", "END OF DAY", "Zeitraum"} {
		if strings.Contains(all, bad) {
			t.Errorf("slip must not contain %q", bad)
		}
	}
	if doc.KickDrawer || doc.Barcode != "" || doc.Logo != nil || doc.TSEQR != nil || doc.DrawerPin != 0 {
		t.Errorf("Z-only fields set: %+v", doc)
	}
}

func TestBuildShiftSlipDoc_SkimLineOnlyWhenPositive(t *testing.T) {
	xdocSetup(t)
	s := shiftSlipDocFixture()
	s.Skim = money.FromMinor(0)
	s.NewFloat = s.Counted
	_, txt := shiftSlipText(t, s)
	if strings.Contains(txt, "Skim to safe") {
		t.Errorf("skim line with zero skim:\n%s", txt)
	}
}

func TestBuildShiftSlipDoc_VarianceSigned(t *testing.T) {
	xdocSetup(t)
	s := shiftSlipDocFixture()
	s.Variance = money.FromMinor(150)
	if _, txt := shiftSlipText(t, s); !strings.Contains(txt, "+£1.50") {
		t.Errorf("positive variance not signed:\n%s", txt)
	}
	s.Variance = money.FromMinor(0)
	if _, txt := shiftSlipText(t, s); strings.Contains(txt, "+£0.00") {
		t.Errorf("zero variance must carry no sign:\n%s", txt)
	}
}

func TestBuildShiftSlipDoc_ZeroDecimalCurrency(t *testing.T) {
	newShiftsPageTestDeps(t)
	pinDefaultLocale(t, "en")
	pinCurrency(t, "JPY")
	s := shiftSlipDocFixture()
	s.Counted = money.FromMinor(500)
	_, txt := shiftSlipText(t, s)
	if !strings.Contains(txt, "500") || strings.Contains(txt, "5.00") {
		t.Fatalf("slip:\n%s", txt)
	}
}

// Free text on the slip (register, cashier, adjustment reason) must never
// reach the printer as a command or a forged line. The cleaning lives in
// internal/print (ut-docs#5847, #5857), so this checks the rendered bytes.
func TestBuildShiftSlipDoc_ControlRunesInFreeTextNeverReachPrinter(t *testing.T) {
	xdocSetup(t)
	const kick = "\x1bp\x00\x19\xfa"
	const forged = "\nExpected 0.00"
	slip := func(f func(string) string) data.ShiftCloseSlip {
		s := shiftSlipDocFixture()
		s.X.RegisterName = f("Front Till")
		s.X.CashierName = f("Alex")
		s.X.Cash.Adjustments = []data.XAdjustment{{Type: "payout", Reason: f("Milk"), Amount: money.FromMinor(-500)}}
		return s
	}
	dirty := func(s string) string { return s + kick + forged }
	clean := func(s string) string { return s + " x" }
	for _, cs := range []string{"", "utf8", "ascii", "cp858", "win1250"} {
		dd := buildShiftSlipDoc(slip(dirty), "Shop", cs)
		rd := buildShiftSlipDoc(slip(clean), "Shop", cs)
		out, ref := print.Render(dd), print.Render(rd)
		if n := bytes.Count(out, []byte("\x1bp")); n != 0 {
			t.Errorf("%q: drawer-kick bytes reached the printer x%d", cs, n)
		}
		if a, b := bytes.Count(out, []byte("\n")), bytes.Count(ref, []byte("\n")); a != b {
			t.Errorf("%q: %d newlines, clean slip has %d", cs, a, b)
		}
		txt, txtRef := print.RenderText(dd), print.RenderText(rd)
		if i := strings.IndexFunc(txt, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }); i >= 0 {
			t.Errorf("%q: control rune %q reached the plain-text slip", cs, []rune(txt[i:])[0])
		}
		if a, b := strings.Count(txt, "\n"), strings.Count(txtRef, "\n"); a != b {
			t.Errorf("%q: text has %d newlines, clean slip has %d", cs, a, b)
		}
	}
}
