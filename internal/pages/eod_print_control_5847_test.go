package pages

import (
	"bytes"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5847: free text on the printed Z report (register, department,
// article, cashier names, store name) must never reach the printer as a
// command or a forged line.
func TestBuildEODDoc_ControlRunesInFreeTextNeverReachPrinter(t *testing.T) {
	newPrintAPITestDeps(t)
	pinDefaultLocale(t, "en")
	const kick = "\x1bp\x00\x19\xfa"
	const forged = "\nTOTAL 0.00"
	report := func(f func(string) string) data.EODReport {
		return data.EODReport{
			Day:             "2025-03-14", // fixed-date:ok outside the 60-day window, never read against the clock
			Departments:     []data.DeptSales{{Department: f("Drinks"), Revenue: 100}},
			Tills:           []data.TillSales{{Name: f("Till 2"), Revenue: 100}},
			ArticleGroups:   []data.ArticleGroupSales{{Group: f("Group"), Gross: money.FromMinor(100)}},
			Articles:        []data.ArticleSales{{Name: f("Article"), Gross: money.FromMinor(100)}},
			Operators:       []data.OperatorSales{{DisplayName: f("Cashier"), Gross: money.FromMinor(100)}},
			NoSaleCount:     1,
			NoSaleOperators: []data.NoSaleOperator{{DisplayName: f("Cashier"), Count: 1}},
		}
	}
	dirty := func(s string) string { return s + kick + forged }
	clean := func(s string) string { return s + " x" }
	for _, cs := range []string{"", "utf8", "ascii", "cp858", "win1250"} {
		out := print.Render(buildEODDoc(report(dirty), dirty("Shop"), cs, eodArticlePrintAll, 0))
		ref := print.Render(buildEODDoc(report(clean), clean("Shop"), cs, eodArticlePrintAll, 0))
		if n := bytes.Count(out, []byte(kick)); n != 0 {
			t.Errorf("%q: drawer-kick bytes reached the printer x%d", cs, n)
		}
		if a, b := bytes.Count(out, []byte("\n")), bytes.Count(ref, []byte("\n")); a != b {
			t.Errorf("%q: %d newlines, clean report has %d", cs, a, b)
		}
	}
}
