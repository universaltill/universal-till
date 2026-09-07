package pages

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
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
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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

// ADR-0066 / ut-docs#1141: eodPeriodMeta's close-to-close "Zeitraum" line —
// the new "eod" path (Day=="") must NOT print the legacy "END OF DAY <day>"
// header, and must render the reference document's own "Zeitraum From - To"
// vocabulary, or "Zeitraum bis To" on the till's very first close (From
// unbounded/empty, ADR-0066 Decision 3).
func TestBuildEODDoc_CloseToCloseMetaLine(t *testing.T) {
	rep := data.EODReport{
		// Day intentionally empty: the new "eod" path.
		From: "2026-08-23T19:10:00+02:00", To: "2026-08-24T19:19:00+02:00",
		GeneratedAt: "2026-08-24T19:19:00Z",
		SalesCount:  1, Gross: 500, Net: 500,
	}
	// eodPeriodMeta's full value is asserted exactly by TestEodPeriodMeta
	// below; here the receipt is only 42 print.Width columns wide, so a
	// full RFC3339-to-RFC3339 line legitimately clips (same print-width
	// constraint TestBuildEODDoc_GeneratedByAndAnnotation's own comment
	// notes) — check the un-clippable prefix instead.
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	if strings.Contains(out, "END OF DAY") {
		t.Errorf("close-to-close report must not print the legacy END OF DAY header, got:\n%s", out)
	}
	if !strings.Contains(out, "Zeitraum 2026-08-23T19:10:00+02:00") {
		t.Errorf("expected the Zeitraum From - To line, got:\n%s", out)
	}

	// The till's first-ever close: From is empty (unbounded lower bound).
	rep.From = ""
	out = string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	if !strings.Contains(out, "Zeitraum bis 2026-08-24T19:19:00+02:00") {
		t.Errorf("expected the open-ended Zeitraum bis line for the first-ever close, got:\n%s", out)
	}
}

// eodPeriodMeta directly, independent of print-width truncation concerns —
// pins the exact three cases (legacy day, bounded range, unbounded first
// close) as pure string values.
func TestEodPeriodMeta(t *testing.T) {
	cases := []struct {
		name string
		rep  data.EODReport
		want string
	}{
		{"legacy calendar day", data.EODReport{Day: "2026-07-14"}, "END OF DAY 2026-07-14"},
		{"close-to-close bounded", data.EODReport{From: "2026-08-23T19:10:00+02:00", To: "2026-08-24T19:19:00+02:00"},
			"Zeitraum 2026-08-23T19:10:00+02:00 - 2026-08-24T19:19:00+02:00"},
		{"till's first-ever close (unbounded)", data.EODReport{To: "2026-08-24T19:19:00+02:00"},
			"Zeitraum bis 2026-08-24T19:19:00+02:00"},
	}
	for _, c := range cases {
		if got := eodPeriodMeta(c.rep); got != c.want {
			t.Errorf("%s: eodPeriodMeta = %q, want %q", c.name, got, c.want)
		}
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
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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
	out = string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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

// TestBuildEODDoc_ArticleGroupArticleOperatorSections is the ut-docs#1010
// printed-receipt requirement: BY ARTICLE GROUP / BY ARTICLE / BY OPERATOR
// print as their own footer sections, same "%-20s %s" row shape and same
// "empty slice -> section omitted entirely" convention as BY DEPARTMENT /
// BY TILL above.
func TestBuildEODDoc_ArticleGroupArticleOperatorSections(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 2, Gross: 15000, Net: 15000, TaxNet: 2000,
		ArticleGroups: []data.ArticleGroupSales{
			{Group: "Phones", Qty: 1, Net: money.FromMinor(10000), Gross: money.FromMinor(10000)},
			{Group: "", Qty: 1, Net: money.FromMinor(5000), Gross: money.FromMinor(5000)},
		},
		Articles: []data.ArticleSales{
			{Name: "Phone Case", Qty: 1, Net: money.FromMinor(10000), Gross: money.FromMinor(10000)},
		},
		Operators: []data.OperatorSales{
			{CashierID: "cashier-a", DisplayName: "Cashier A", Count: 2, Net: money.FromMinor(15000), Gross: money.FromMinor(15000)},
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	for _, want := range []string{
		"BY ARTICLE GROUP", "Phones", "£100.00", "Uncategorized", "£50.00",
		"BY ARTICLE", "Phone Case",
		"BY OPERATOR", "Cashier A", "£150.00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Z-report missing %q\n%s", want, out)
		}
	}
}

// TestBuildEODDoc_ArticleGroupArticleOperatorSections_OmittedWhenEmpty: a
// range report (where ut-docs#1010's breakdowns are never populated, see
// EODReport's own doc comment) must print none of the three new sections —
// same "no line at all beats an empty section" convention as GUTSCHEINE/
// STORNOS.
func TestBuildEODDoc_ArticleGroupArticleOperatorSections_OmittedWhenEmpty(t *testing.T) {
	rep := data.EODReport{
		From: "2026-07-01", To: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 2, Gross: 15000, Net: 15000, TaxNet: 2000,
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	for _, unwanted := range []string{"BY ARTICLE GROUP", "BY ARTICLE", "BY OPERATOR"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("range report must omit %q entirely, got:\n%s", unwanted, out)
		}
	}
}

// articlesForCap builds n synthetic ArticleSales rows, GROSS-DESCENDING (the
// same ORDER BY gross DESC ArticleSalesForDay/ArticleSalesForInstantWindow
// return), each with a distinct amount so a truncation test can assert
// exactly which ones survived a cap by name.
func articlesForCap(n int) []data.ArticleSales {
	out := make([]data.ArticleSales, n)
	for i := 0; i < n; i++ {
		out[i] = data.ArticleSales{
			Name:  fmt.Sprintf("Article %02d", i+1),
			Qty:   1,
			Net:   money.FromMinor(int64(1000 * (n - i))),
			Gross: money.FromMinor(int64(1000 * (n - i))),
		}
	}
	return out
}

// TestBuildEODDoc_ArticlePrintMode_DefaultCappedLeavesLowSKUUnaffected is the
// ut-docs#1650 acceptance criterion "a low-SKU shop's printed output is
// unchanged in practice (fewer than 30 articles prints identically to
// 'all')": buildEODDoc, called with the shipped default values themselves
// (eodArticlePrintCapped/eodArticlePrintCapDefault — what
// resolveEODArticlePrintSettings resolves an untouched store setting TO;
// that resolution itself is pinned separately by
// TestResolveEODArticlePrintSettings_DefaultsUnsetOrInvalid in
// eod_api_test.go, not re-derived here), must print every one of 25
// articles with no "+N more" line.
func TestBuildEODDoc_ArticlePrintMode_DefaultCappedLeavesLowSKUUnaffected(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-09-06", GeneratedAt: "2026-09-06T21:30:00Z",
		SalesCount: 25, Gross: 15000, Net: 15000, TaxNet: 2000,
		Articles: articlesForCap(25),
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintCapped, eodArticlePrintCapDefault)))
	for i := 1; i <= 25; i++ {
		want := fmt.Sprintf("Article %02d", i)
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in an under-cap report, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "more (see on-screen report)") {
		t.Errorf("a 25-article day must not be truncated by the default cap (30), got:\n%s", out)
	}
}

// TestBuildEODDoc_ArticlePrintMode_CappedTruncatesTopNByRevenue is the
// high-SKU half of the same acceptance criterion: a day with MORE articles
// than the cap prints only the top-cap by revenue (rep.Articles already
// arrives gross-DESC) plus a "+N more" line, and omits the rest.
func TestBuildEODDoc_ArticlePrintMode_CappedTruncatesTopNByRevenue(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-09-06", GeneratedAt: "2026-09-06T21:30:00Z",
		SalesCount: 5, Gross: 15000, Net: 15000, TaxNet: 2000,
		Articles: articlesForCap(5),
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintCapped, 3)))
	for _, want := range []string{"Article 01", "Article 02", "Article 03", "+2 more (see on-screen report)"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in a capped report, got:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Article 04", "Article 05"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("article beyond the cap must not print, got %q in:\n%s", unwanted, out)
		}
	}
}

// TestBuildEODDoc_ArticlePrintMode_AllNeverTruncates: explicit "all" mode
// reproduces the pre-#1650 unconditional behavior even when the count would
// exceed a configured cap.
func TestBuildEODDoc_ArticlePrintMode_AllNeverTruncates(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-09-06", GeneratedAt: "2026-09-06T21:30:00Z",
		SalesCount: 5, Gross: 15000, Net: 15000, TaxNet: 2000,
		Articles: articlesForCap(5),
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 3)))
	for i := 1; i <= 5; i++ {
		want := fmt.Sprintf("Article %02d", i)
		if !strings.Contains(out, want) {
			t.Errorf("expected %q under 'all' mode regardless of cap, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "more (see on-screen report)") {
		t.Errorf("'all' mode must never truncate, got:\n%s", out)
	}
}

// TestBuildEODDoc_ArticlePrintMode_OffOmitsSectionEntirely: "off" omits the
// whole "BY ARTICLE" section, same "no line at all" convention the empty
// case already uses -- but here rep.Articles is non-empty, proving the
// setting (not just an empty slice) is what suppressed it.
func TestBuildEODDoc_ArticlePrintMode_OffOmitsSectionEntirely(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-09-06", GeneratedAt: "2026-09-06T21:30:00Z",
		SalesCount: 5, Gross: 15000, Net: 15000, TaxNet: 2000,
		Articles: articlesForCap(5),
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintOff, eodArticlePrintCapDefault)))
	if strings.Contains(out, "BY ARTICLE") {
		t.Errorf("'off' mode must omit the BY ARTICLE section entirely, got:\n%s", out)
	}
	for i := 1; i <= 5; i++ {
		if strings.Contains(out, fmt.Sprintf("Article %02d", i)) {
			t.Errorf("'off' mode must print no article lines, got:\n%s", out)
		}
	}
}

// TestBuildEODDoc_OrderTypeSection is the ut-docs#1015 printed-receipt
// requirement: BY ORDER TYPE prints as its own footer section, same
// footerRow/omit-when-empty convention as BY ARTICLE GROUP/BY OPERATOR
// above, with fixed English "Dine in"/"Takeaway" labels (this printed
// report is not localized — see GUTSCHEINE/STORNOS's own fixed-vocabulary
// precedent).
func TestBuildEODDoc_OrderTypeSection(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-09-06", GeneratedAt: "2026-09-06T21:30:00Z",
		SalesCount: 2, Gross: 15000, Net: 15000, TaxNet: 2000,
		OrderTypes: []data.OrderTypeSales{
			{OrderType: "", Qty: 1, Net: money.FromMinor(10000), Gross: money.FromMinor(10000)},
			{OrderType: "takeaway", Qty: 1, Net: money.FromMinor(5000), Gross: money.FromMinor(5000)},
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	for _, want := range []string{"BY ORDER TYPE", "Dine in", "£100.00", "Takeaway", "£50.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("Z-report missing %q\n%s", want, out)
		}
	}
}

// TestBuildEODDoc_OrderTypeSection_OmittedWhenEmpty mirrors
// TestBuildEODDoc_ArticleGroupArticleOperatorSections_OmittedWhenEmpty: a
// range report never populates OrderTypes, so BY ORDER TYPE must not print
// at all.
func TestBuildEODDoc_OrderTypeSection_OmittedWhenEmpty(t *testing.T) {
	rep := data.EODReport{
		From: "2026-07-01", To: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 2, Gross: 15000, Net: 15000, TaxNet: 2000,
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	if strings.Contains(out, "BY ORDER TYPE") {
		t.Errorf("range report must omit %q entirely, got:\n%s", "BY ORDER TYPE", out)
	}
}

// TestBuildEODDoc_ArticleSection_LongNameDoesNotSwallowAmount is the fix for
// ut-docs#1010 review finding 1: sale_lines.name_snapshot is free-text and
// routinely exceeds the fixed 20-column pad BY DEPARTMENT/BY TILL's shared
// "%-20s %s" convention was written for (those labels are short/controlled —
// a department name, a till name, "TOTAL"). A name that long, formatted the
// old way, pushes the amount past print.Width and the printer's own
// line-clip silently drops it — the actual revenue figure vanishes from a
// financial document with no error. footerRow (the fix) must clip the LABEL
// instead, so the amount always survives intact in the rendered output.
func TestBuildEODDoc_ArticleSection_LongNameDoesNotSwallowAmount(t *testing.T) {
	longName := "Coca-Cola Zero Sugar 500ml Bottle 6-Pack Multibuy Offer" // 56 runes
	// The article's own Gross (£777.77) is deliberately DIFFERENT from the
	// report's overall Sales/NET total (£1,500.00) — otherwise a check for
	// "does £777.77 appear anywhere in the output" could false-pass by
	// matching the unrelated Sales/NET lines instead of the BY ARTICLE row
	// actually under test (exactly the false-pass class this pipeline
	// watches for: an earlier draft of this test asserted on "£1,500.00"
	// while ALSO setting the report total to £1,500.00, so it kept passing
	// even with the pre-fix "%-20s %s" formatting reinstated, because the
	// substring matched the totals section instead).
	rep := data.EODReport{
		Day: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 1, Gross: 150000, Net: 150000,
		Articles: []data.ArticleSales{
			{Name: longName, Qty: 1, Net: money.FromMinor(77777), Gross: money.FromMinor(77777)},
		},
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	lines := strings.Split(out, "\n")
	var articleLine string
	for i, l := range lines {
		if strings.Contains(l, "BY ARTICLE") && i+1 < len(lines) {
			articleLine = lines[i+1]
			break
		}
	}
	if articleLine == "" {
		t.Fatalf("no BY ARTICLE section found in output:\n%s", out)
	}
	if !strings.Contains(articleLine, "£777.77") {
		t.Fatalf("BY ARTICLE row for a long article name must still carry its own amount intact, got row %q from output:\n%s", articleLine, out)
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
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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
	out = string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	if !strings.Contains(out, "!!") {
		t.Error("non-zero variance must be flagged with !!")
	}
	if !strings.Contains(out, "-£1.10") {
		t.Error("negative variance must render signed (-£1.10)")
	}

	// No reconciliation (no shift closed that day): the section is absent
	// and the report still renders — day-close is never blocked on it.
	rep.CashReconciliation = nil
	out = string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	if strings.Contains(out, "CASH RECONCILIATION") {
		t.Error("section must be omitted when no reconciliation exists")
	}
}

// A cash tip held out of CashSales (ut-docs#1046) prints its own "Tips held
// out" line inside CASH RECONCILIATION, positioned right after Cash sales.
func TestBuildEODDoc_CashReconciliation_TipsHeldOut(t *testing.T) {
	rc := data.CashReconciliation{
		OpeningFloat: 10000,
		CashSales:    40000,
		TipsHeldOut:  2000,
		Calculated:   52000,
		Counted:      52000,
		ShiftsClosed: 1,
	}
	// ut-docs#1124: guard the fixture itself, not just the render -- these
	// are exactly the line items buildEODDoc prints top-to-bottom above
	// "Calculated" (Skim excepted, printed separately below Variance -- true
	// for a close-time skim, since expected_cash is computed before that
	// audit row exists; NOT true for a skim recorded mid-shift, see
	// ut-docs#1146 -- this fixture has no skim at all, so it doesn't reach
	// that gap either way). If these didn't already sum to Calculated here,
	// the assertions below would be checking a scenario that can't occur
	// against real data.
	if sum := rc.OpeningFloat + rc.CashSales + rc.TipsHeldOut + rc.PayIns + rc.PayOuts; sum != rc.Calculated {
		t.Fatalf("test fixture doesn't satisfy the reconciliation identity: %d != Calculated %d", sum, rc.Calculated)
	}
	rep := data.EODReport{
		Day: "2026-07-14", GeneratedAt: "2026-07-14T21:30:00Z",
		SalesCount: 1, Gross: 40000, Net: 40000,
		Methods:            []data.EODMethod{{Method: "cash", In: 42000}},
		Tips:               []data.EODTip{{Method: "cash", Count: 1, Amount: 2000}},
		CashReconciliation: &rc,
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
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
