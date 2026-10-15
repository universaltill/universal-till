package pages

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#4330: the printed Z-report's header, totals, payment methods,
// tips and VAT tables follow the shop's locale (httpx.DefaultLocale), the
// convention the sale receipt took in ut-docs#4199. An en shop's Z-report
// stays byte-for-byte what it was; a tr/fa shop's carries none of these
// English labels.

// labelsEODReport fills every section of the Z-report this card
// translates: header, totals, built-in and plugin payment methods, tips
// and both VAT tables.
func labelsEODReport() data.EODReport {
	return data.EODReport{
		Day:         "2026-10-14",
		GeneratedAt: "not-a-timestamp",
		SalesCount:  12, Gross: 123456,
		RefundCount: 2, RefundTotal: 1500,
		Net: 121956, TaxNet: 20326,
		Methods: []data.EODMethod{
			{Method: "cash", In: 50000, Out: 1500},
			{Method: "card", In: 70000},
			{Method: "voucher", In: 3456},
			{Method: "plugin_pay", In: 0},
		},
		Tips: []data.EODTip{
			{Method: "cash", Count: 3, Amount: 450},
			{Method: "card", Count: 1, Amount: 200},
		},
		TaxBands: []data.TaxBand{
			{RateBP: 2000, Net: 100000, Tax: 20000, Gross: 120000},
			{RateBP: 500, Net: 3290, Tax: 166, Gross: 3456},
		},
		MethodTaxBands: []data.MethodTaxBand{
			{Method: "card", RateBP: 2000, Net: 58333, Tax: 11667, Gross: 70000},
			{Method: "cash", RateBP: 2000, Net: 41667, Tax: 8333, Gross: 50000},
			{Method: "cash", RateBP: 500, Net: 3290, Tax: 166, Gross: 3456},
		},
	}
}

// eodPrintedLines flattens the Z-report's printed text in print order:
// meta lines, "label|amount" for totals and payments, then footer lines.
func eodPrintedLines(doc print.Doc) []string {
	out := append([]string{}, doc.Meta...)
	for _, kv := range doc.Totals {
		out = append(out, kv.Label+"|"+kv.Amount)
	}
	for _, kv := range doc.Payments {
		out = append(out, kv.Label+"|"+kv.Amount)
	}
	return append(out, doc.Footer...)
}

func buildLabelsEOD(t *testing.T, locale string) []string {
	t.Helper()
	newPrintAPITestDeps(t) // loads the translator
	pinDefaultLocale(t, locale)
	return eodPrintedLines(buildEODDoc(labelsEODReport(), "Shop", "", eodArticlePrintAll, 0))
}

// The en Z-report, pinned line for line to what it printed before
// ut-docs#4330.
func TestEODPrintLabels_EnglishUnchanged(t *testing.T) {
	got := buildLabelsEOD(t, "en")
	want := []string{
		"END OF DAY 2026-10-14",
		"Generated not-a-timestamp",
		"Sales (12)|£1,234.56",
		"Refunds (2)|-£15.00",
		"Tax (net)|£203.26",
		"NET|£1,219.56",
		"Cash|£485.00",
		"Card|£700.00",
		"Voucher|£34.56",
		"Plugin_pay|£0.00",
		"",
		"TIPS (held out of revenue)",
		"3x Cash              £4.50",
		"1x Card              £2.00",
		"TOTAL                £6.50",
		"",
		"BY VAT RATE",
		"               Net        Tax       Gross",
		"20%      £1,000.00    £200.00   £1,200.00",
		"5%          £32.90      £1.66      £34.56",
		"",
		"BY METHOD & VAT RATE",
		"Card:",
		"               Net        Tax       Gross",
		"20%        £583.33    £116.67     £700.00",
		"Cash:",
		"               Net        Tax       Gross",
		"20%        £416.67     £83.33     £500.00",
		"5%          £32.90      £1.66      £34.56",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("en Z-report changed:\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// englishEODLiterals are the en words each translated label used to print.
// None may survive on a tr/fa Z-report.
var englishEODLiterals = []string{
	"Generated", "Sales", "Refunds", "Tax", "NET", "Net", "Gross", "TIPS", "held out",
	"TOTAL", "BY VAT RATE", "BY METHOD", "Cash", "Card", "Voucher",
}

// sameInLocale are English literals that are also the right word in that
// language ("Net" is Turkish too), so their presence proves nothing.
var sameInLocale = map[string]map[string]bool{"tr": {"Net": true, "NET": true}}

func TestEODPrintLabels_TranslatedForTurkishAndPersian(t *testing.T) {
	for _, locale := range []string{"tr", "fa"} {
		lines := buildLabelsEOD(t, locale)
		for _, line := range lines[1:] { // [0] is the period line, out of scope
			for _, en := range englishEODLiterals {
				if strings.Contains(line, en) && !sameInLocale[locale][en] {
					t.Errorf("%s: line %q still carries the English %q", locale, line, en)
				}
			}
		}
	}
	joined := strings.Join(buildLabelsEOD(t, "tr"), "\n")
	for _, want := range []string{"Oluşturulma", "Satışlar (12)", "İadeler (2)", "Vergi (net)", "Nakit|", "Kart|", "TOPLAM", "KDV ORANINA GÖRE", "Brüt", "3x Nakit", "Plugin_pay|", "Kart:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tr: expected %q on the Z-report, got:\n%s", want, joined)
		}
	}
}

// Every translated footer line still fits the printer's width, so a long
// translation never pushes an amount off the paper (the card's %-20s
// column check), and a tr shop's lines encode in Windows-1254.
func TestEODPrintLabels_TranslatedLinesFitAndEncode(t *testing.T) {
	enc := charmap.Windows1254.NewEncoder()
	for _, locale := range []string{"en", "tr", "fa", "ar"} {
		newPrintAPITestDeps(t)
		pinDefaultLocale(t, locale)
		doc := buildEODDoc(labelsEODReport(), "Shop", "", eodArticlePrintAll, 0)
		for _, line := range doc.Footer {
			if n := utf8.RuneCountInString(line); n > print.Width {
				t.Errorf("%s: footer line is %d runes, over print.Width %d: %q", locale, n, print.Width, line)
			}
		}
		if locale != "tr" {
			continue
		}
		for _, line := range eodPrintedLines(doc) {
			if _, err := enc.String(line); err != nil {
				t.Errorf("tr line %q does not encode in Windows-1254: %v", line, err)
			}
		}
	}
}
