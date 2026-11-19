package pages

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#4397: the rest of the printed Z-report — departments, tills, the
// ÖKC block, article groups/articles/operators, order types, vouchers,
// voids, cash reconciliation, the receipt range and the legacy period
// line — follows the shop's locale too, the #4330 convention. The fixed
// fiscal vocabulary (Zeitraum, GUTSCHEINE, STORNOS, Erstellt von,
// Anmerkung and the ÖKC slip names Mali fis / Iade fisi / Bilgi fisi)
// stays as it is.

// sectionsEODReport fills every section ut-docs#4397 translates, with the
// fallbacks (unnamed department/till/group/operator), a capped article
// list, a multi-Z ÖKC window with a mismatch, all four voucher lines and a
// cash variance.
func sectionsEODReport() data.EODReport {
	return data.EODReport{
		Day:         "2026-10-14",
		GeneratedAt: "not-a-timestamp",
		Departments: []data.DeptSales{{Department: "Drinks", Revenue: 4200}, {Revenue: 100}},
		Tills:       []data.TillSales{{Name: "Till 2", Revenue: 3000}, {Revenue: 1300}},
		FiscalDevice: &data.FiscalDeviceWindow{
			Serial: "AB123", Maker: "Acme", ZNos: []int64{41, 42},
			MaliFis: 5, IadeFisi: 1, BilgiFisi: 2, Other: 1, Total: 9, TillOKCTenders: 8,
		},
		ArticleGroups: []data.ArticleGroupSales{{Group: "Coffee", Gross: money.FromMinor(3000)}, {Gross: money.FromMinor(50)}},
		Articles: []data.ArticleSales{
			{Name: "Latte", Gross: money.FromMinor(2000)},
			{Name: "Tea", Gross: money.FromMinor(1000)},
			{Name: "Water", Gross: money.FromMinor(50)},
		},
		Operators: []data.OperatorSales{{DisplayName: "Alex", Gross: money.FromMinor(3000)}, {Gross: money.FromMinor(50)}},
		OrderTypes: []data.OrderTypeSales{
			{OrderType: "dine_in", Gross: money.FromMinor(2000)},
			{OrderType: pos.OrderTypeTakeaway, Gross: money.FromMinor(1050)},
		},
		VouchersIssuedCount: 2, VouchersIssued: 5000,
		VouchersRedeemedCount: 1, VouchersRedeemed: 1000,
		VouchersImportedCount: 3, VouchersImported: 7500,
		VouchersAdjustedCount: 1, VouchersAdjusted: -250,
		CancelCount: 1, CancelTotal: 899,
		CashReconciliation: &data.CashReconciliation{
			OpeningFloat: 10000, CashSales: 48500, TipsHeldOut: -450,
			PayIns: 500, PayOuts: -1200, Calculated: 57350, Counted: 57000,
			Variance: -350, Skim: -40000, NewFloat: 17000,
		},
		FirstReceipt: "R-0001",
		LastReceipt:  "R-0012",
		GeneratedBy:  "Alex",
		Annotation:   "late close",
	}
}

func buildSectionsEOD(t *testing.T, locale string, rep data.EODReport) []string {
	t.Helper()
	newPrintAPITestDeps(t) // loads the translator
	pinDefaultLocale(t, locale)
	doc := buildEODDoc(rep, "Shop", "", eodArticlePrintCapped, 2)
	return append(append([]string{}, doc.Meta[0]), doc.Footer...)
}

// The en Z-report's remaining sections, pinned line for line to what they
// printed before ut-docs#4397.
func TestEODPrintSections_EnglishUnchanged(t *testing.T) {
	got := buildSectionsEOD(t, "en", sectionsEODReport())
	want := []string{
		"END OF DAY 2026-10-14",
		"",
		"BY DEPARTMENT",
		"Drinks               £42.00",
		"Uncategorized        £1.00",
		"",
		"BY TILL",
		"Till 2               £30.00",
		"This till            £13.00",
		"",
		"FISCAL DEVICE (OKC)",
		"Serial                               AB123",
		"Maker                                 Acme",
		"Z no.           41, 42 (closed mid-period)",
		"Mali fis             5",
		"Iade fisi            1",
		"Bilgi fisi           2",
		"Other                1",
		"Till OKC tenders     8",
		"MISMATCH (device 9 vs till 8)",
		"",
		"BY ARTICLE GROUP",
		"Coffee                              £30.00",
		"Uncategorized                        £0.50",
		"",
		"BY ARTICLE",
		"Latte                               £20.00",
		"Tea                                 £10.00",
		"+1 more (see on-screen report)",
		"",
		"BY OPERATOR",
		"Alex                                £30.00",
		"Unattributed                         £0.50",
		"",
		"BY ORDER TYPE",
		"Dine in                             £20.00",
		"Takeaway                            £10.50",
		"",
		"GUTSCHEINE",
		"Issued (2)           £50.00",
		"Redeemed (1)         £10.00",
		"Imported (3)         £75.00",
		"Adjusted (1)         -£2.50",
		"",
		"STORNOS (voided receipts, not in revenue)",
		"Voided (1)           £8.99",
		"",
		"CASH RECONCILIATION",
		"Opening float        £100.00",
		"Cash sales           £485.00",
		"Tips held out        -£4.50",
		"Pay-ins              £5.00",
		"Pay-outs             -£12.00",
		"Calculated           £573.50",
		"Counted              £570.00",
		"Variance             -£3.50 !!",
		"Skim to safe         -£400.00",
		"New float            £170.00",
		"",
		"Receipts R-0001 - R-0012",
		"",
		"Erstellt von: Alex",
		"Anmerkung: late close",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("en Z-report changed:\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The ÖKC block's two other outcomes: no activity at all, and a match.
func TestEODPrintSections_FiscalDeviceOutcomesEnglishUnchanged(t *testing.T) {
	empty := data.EODReport{Day: "2026-10-14", FiscalDevice: &data.FiscalDeviceWindow{}}
	if got := strings.Join(buildSectionsEOD(t, "en", empty)[1:], "\n"); got != "\nFISCAL DEVICE (OKC)\nNO DEVICE ACTIVITY" {
		t.Errorf("en empty ÖKC block changed: %q", got)
	}
	match := data.EODReport{Day: "2026-10-14", FiscalDevice: &data.FiscalDeviceWindow{Serial: "AB123", ZNos: []int64{7}, MaliFis: 3, Total: 3, TillOKCTenders: 3}}
	got := buildSectionsEOD(t, "en", match)
	if last := got[len(got)-1]; last != "MATCH" {
		t.Errorf("en matching ÖKC block ends %q, want MATCH", last)
	}
	if !strings.Contains(strings.Join(got, "\n"), "Z no.                                    7") {
		t.Errorf("en single-Z line changed:\n%s", strings.Join(got, "\n"))
	}
}

// englishEODSectionLiterals are the en words each translated label used to
// print. None may survive on a tr/fa Z-report.
var englishEODSectionLiterals = []string{
	"END OF DAY", "BY DEPARTMENT", "Uncategorized", "BY TILL", "This till",
	"FISCAL DEVICE", "Serial", "Maker", "Z no.", "closed mid-period", "Other",
	"Till OKC tenders", "MATCH", "BY ARTICLE", "more (see", "BY OPERATOR",
	"Unattributed", "BY ORDER TYPE", "Dine in", "Takeaway", "Issued",
	"Redeemed", "Imported", "Adjusted", "voided receipts", "Voided",
	"CASH RECONCILIATION", "Opening float", "Cash sales", "Tips held out",
	"Pay-ins", "Pay-outs", "Calculated", "Counted", "Variance",
	"Skim to safe", "New float", "Receipts",
}

// fixedEODVocabulary is printed in every locale on purpose (ADR-0066
// Decision 6, ut-docs#1012; the ÖKC slip names, ut-docs#2410).
var fixedEODVocabulary = []string{"GUTSCHEINE", "STORNOS (", "Mali fis", "Iade fisi", "Bilgi fisi", "Erstellt von: ", "Anmerkung: "}

func TestEODPrintSections_TranslatedForTurkishAndPersian(t *testing.T) {
	for _, locale := range []string{"tr", "fa"} {
		lines := buildSectionsEOD(t, locale, sectionsEODReport())
		for _, line := range lines {
			for _, en := range englishEODSectionLiterals {
				if strings.Contains(line, en) {
					t.Errorf("%s: line %q still carries the English %q", locale, line, en)
				}
			}
		}
		joined := strings.Join(lines, "\n")
		for _, fixed := range fixedEODVocabulary {
			if !strings.Contains(joined, fixed) {
				t.Errorf("%s: fixed fiscal vocabulary %q is gone:\n%s", locale, fixed, joined)
			}
		}
		// The data itself survives translation.
		for _, v := range []string{"AB123", "41, 42", "R-0001", "R-0012", "2026-10-14"} {
			if !strings.Contains(joined, v) {
				t.Errorf("%s: value %q missing:\n%s", locale, v, joined)
			}
		}
	}
	for _, locale := range []string{"tr", "fa"} {
		for _, rep := range []data.EODReport{
			{FiscalDevice: &data.FiscalDeviceWindow{}},
			{FiscalDevice: &data.FiscalDeviceWindow{ZNos: []int64{7}, MaliFis: 3, Total: 3, TillOKCTenders: 3}},
		} {
			for _, line := range buildSectionsEOD(t, locale, rep)[1:] {
				for _, en := range []string{"FISCAL DEVICE", "NO DEVICE ACTIVITY", "MATCH"} {
					if strings.Contains(line, en) {
						t.Errorf("%s: ÖKC line %q still carries the English %q", locale, line, en)
					}
				}
			}
		}
	}
}

// Every footer line still fits the printer's width in every core locale —
// a long translated row label is clipped, never the amount — and a tr
// shop's lines encode in Windows-1254.
func TestEODPrintSections_TranslatedLinesFitAndEncode(t *testing.T) {
	enc := charmap.Windows1254.NewEncoder()
	rep := sectionsEODReport()
	rep.Annotation = "" // free text, out of scope
	for _, locale := range []string{"en", "tr", "fa", "ar"} {
		lines := buildSectionsEOD(t, locale, rep)
		for _, line := range lines[1:] {
			if n := print.DisplayWidth(line); n > print.Width {
				t.Errorf("%s: footer line is %d columns, over print.Width %d: %q", locale, n, print.Width, line)
			}
		}
		if locale != "tr" {
			continue
		}
		for _, line := range lines {
			if _, err := enc.String(line); err != nil {
				t.Errorf("tr line %q does not encode in Windows-1254: %v", line, err)
			}
		}
	}
}

// eodRow keeps the en "%-20s %s" layout byte-for-byte and clips only a
// label that would push the amount past print.Width.
func TestEODRow_PadsLikeSprintfAndClipsOnlyOverflow(t *testing.T) {
	if got, want := eodRow("Pay-ins", "£5.00"), "Pay-ins              £5.00"; got != want {
		t.Errorf("short label: got %q, want %q", got, want)
	}
	long := strings.Repeat("ş", 40)
	got := eodRow(long, "£1,234.56")
	if n := print.DisplayWidth(got); n != print.Width {
		t.Errorf("overflowing label: %d columns, want exactly print.Width %d: %q", n, print.Width, got)
	}
	if !strings.HasSuffix(got, " £1,234.56") {
		t.Errorf("overflowing label lost its amount: %q", got)
	}
}
