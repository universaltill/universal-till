package pages

import (
	"bytes"
	"strings"
	"testing"
	"unicode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5857: free text on the printed X report (register name, cashier
// name, adjustment reason) must never reach the printer as a command or a
// forged line. The cleaning lives in internal/print (ut-docs#5847), not here.
func TestBuildXReportDoc_ControlRunesInFreeTextNeverReachPrinter(t *testing.T) {
	xdocSetup(t)
	const kick = "\x1bp\x00\x19\xfa"
	const forged = "\nTOTAL 0.00"
	report := func(f func(string) string) data.XReport {
		rep := xdocReport()
		rep.RegisterName = f("Front Till")
		rep.CashierName = f("Alex")
		rep.Cash.Adjustments = []data.XAdjustment{{Type: "payout", Reason: f("Milk"), Amount: money.FromMinor(-500)}}
		return rep
	}
	dirty := func(s string) string { return s + kick + forged }
	clean := func(s string) string { return s + " x" }
	for _, cs := range []string{"", "utf8", "ascii", "cp858", "win1250"} {
		dd := buildXReportDoc(report(dirty), "Shop", cs)
		rd := buildXReportDoc(report(clean), "Shop", cs)

		// ESC p prefix: the kick's trailing 0xFA folds to '?' on cp858/win125x,
		// so the full sequence could not fail.
		out, ref := print.Render(dd), print.Render(rd)
		if n := bytes.Count(out, []byte("\x1bp")); n != 0 {
			t.Errorf("%q: drawer-kick bytes reached the printer x%d", cs, n)
		}
		if a, b := bytes.Count(out, []byte("\n")), bytes.Count(ref, []byte("\n")); a != b {
			t.Errorf("%q: %d newlines, clean report has %d", cs, a, b)
		}

		// lp/CUPS path: PrintDoc sends print.RenderText.
		txt, txtRef := print.RenderText(dd), print.RenderText(rd)
		// Any control rune but the layout's own '\n': a \f would eject a
		// page on lp, a NUL/ESC reach the printer as a command.
		if i := strings.IndexFunc(txt, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }); i >= 0 {
			t.Errorf("%q: control rune %q reached the plain-text report", cs, []rune(txt[i:])[0])
		}
		if a, b := strings.Count(txt, "\n"), strings.Count(txtRef, "\n"); a != b {
			t.Errorf("%q: text has %d newlines, clean report has %d", cs, a, b)
		}
	}
}
