package pages

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5776: the printed Z-report's "recorded late from other tills"
// section (ADR-0066 amendment, Decision 3).

func lateBaseReport() data.EODReport {
	return data.EODReport{Day: "2026-01-14", GeneratedAt: "not-a-timestamp", SalesCount: 2, Gross: 1000, Net: 900,
		Tills: []data.TillSales{{Name: "Main", Revenue: 900}}}
}

func buildLateDoc(t *testing.T, locale string, rep data.EODReport) print.Doc {
	t.Helper()
	newPrintAPITestDeps(t)
	pinDefaultLocale(t, locale)
	return buildEODDoc(rep, "Shop", "", eodArticlePrintCapped, 2)
}

func i64(v int64) *int64 { return &v }

const lateOriginA = "2026-01-05T09:30:00Z"
const lateOriginB = "2026-01-06T10:45:00Z"

func lateWhen(s string, locale string) string {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return httpx.FormatDateTimeLatin(ts.Local(), locale)
}

func TestEODPrint_LateNoteSection(t *testing.T) {
	base := buildLateDoc(t, "en", lateBaseReport())
	rep := lateBaseReport()
	rep.LateEntries = &data.EODLateEntries{
		EarliestOrigin: lateOriginA,
		Tills: []data.EODLateTill{
			{TillID: "till-a", Name: "Bar till", SalesCount: 2, Gross: 750, RefundCount: 1, RefundTotal: 40,
				NoSaleCount: 1, ClearedBasketCount: 1, ClearedBasketTotal: i64(250), EarliestOrigin: lateOriginA},
			{TillID: "till-b", Name: "Patio", SalesCount: 1, Gross: 100, EarliestOrigin: lateOriginB},
		},
	}
	doc := buildLateDoc(t, "en", rep)
	money := func(m int64) string { return httpx.FormatMoneyPrint(m, "en", "") }
	want := append(append([]string{}, base.Footer...),
		"", "RECORDED LATE FROM OTHER TILLS", "Made before the previous close", "Already included in the totals above",
		"Bar till",
		footerRow("  Entries", "5"),
		footerRow("  Sales (2)", money(750)),
		footerRow("  Refunds (1)", "-"+money(40)),
		footerRow("  Cleared baskets (1)", money(250)),
		footerRow("  Drawer opens", "1"),
		footerRow("  Earliest made", lateWhen(lateOriginA, "en")),
		"Patio",
		footerRow("  Entries", "1"),
		footerRow("  Sales (1)", money(100)),
		footerRow("  Earliest made", lateWhen(lateOriginB, "en")),
	)
	if !reflect.DeepEqual(doc.Footer, want) {
		t.Fatalf("footer:\n got %q\nwant %q", doc.Footer, want)
	}
	if !reflect.DeepEqual(doc.Totals, base.Totals) {
		t.Fatalf("late note must never touch Totals:\n got %v\nwant %v", doc.Totals, base.Totals)
	}
}

func TestEODPrint_LateNoteOmittedWhenNilOrEmpty(t *testing.T) {
	base := buildLateDoc(t, "en", lateBaseReport())
	for name, le := range map[string]*data.EODLateEntries{"nil": nil, "empty": {}, "empty-origin": {EarliestOrigin: lateOriginA}} {
		rep := lateBaseReport()
		rep.LateEntries = le
		doc := buildLateDoc(t, "en", rep)
		if !reflect.DeepEqual(doc.Footer, base.Footer) {
			t.Fatalf("%s: footer must be identical to the base:\n got %q\nwant %q", name, doc.Footer, base.Footer)
		}
	}
}

func TestEODPrint_LateNoteWidth(t *testing.T) {
	for _, locale := range []string{"en", "tr", "ar"} {
		for _, name := range []string{strings.Repeat("N", 40), strings.Repeat("Ü", 60), ""} {
			rep := lateBaseReport()
			rep.LateEntries = &data.EODLateEntries{Tills: []data.EODLateTill{{
				TillID: "0123456789abcdef", Name: name, SalesCount: 12, Gross: 100_000_000, RefundCount: 3, RefundTotal: 100_000_000,
				ClearedBasketCount: 4, ClearedBasketTotal: i64(100_000_000), NoSaleCount: 5, EarliestOrigin: lateOriginA}}}
			base := buildLateDoc(t, locale, lateBaseReport())
			doc := buildLateDoc(t, locale, rep)
			section := doc.Footer[len(base.Footer):]
			if len(section) == 0 {
				t.Fatalf("%s: no section", locale)
			}
			m := httpx.FormatMoneyPrint(100_000_000, locale, "")
			amountLines := 0
			for _, l := range section {
				if n := utf8.RuneCountInString(l); n > print.Width {
					t.Fatalf("%s: line %q is %d runes, max %d", locale, l, n, print.Width)
				}
				if strings.Contains(l, strings.TrimLeft(m, "-")) {
					amountLines++
					if !strings.HasSuffix(l, m) && !strings.HasSuffix(l, "-"+m) {
						t.Fatalf("%s: amount clipped on %q", locale, l)
					}
				}
			}
			if amountLines != 3 {
				t.Fatalf("%s: want 3 amount lines (sales, refunds, cleared), got %d in %q", locale, amountLines, section)
			}
		}
	}
}

func TestEODPrint_LateNoteStreams(t *testing.T) {
	rep := lateBaseReport()
	rep.LateEntries = &data.EODLateEntries{Tills: []data.EODLateTill{
		{TillID: "t1", Name: "Only drawer", NoSaleCount: 2},
		{TillID: "t2", Name: "Old cleared", ClearedBasketCount: 3, SalesCount: 1, Gross: 5},
	}}
	doc := buildLateDoc(t, "en", rep)
	joined := strings.Join(doc.Footer, "\n")
	for _, want := range []string{footerRow("  Entries", "2"), footerRow("  Drawer opens", "2"), footerRow("  Entries", "4"), "  Cleared baskets (3)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, doc.Footer)
		}
	}
	for _, l := range doc.Footer {
		if strings.HasPrefix(l, "  Cleared baskets (3)") && strings.ContainsAny(l, "£0123456789.,") && l != "  Cleared baskets (3)" {
			t.Fatalf("a cleared count without a recorded total must print no amount, got %q", l)
		}
		if strings.HasPrefix(l, "  Refunds") || strings.HasPrefix(l, "  Sales (0)") {
			t.Fatalf("absent streams must print nothing, got %q", l)
		}
	}
}

func TestEODPrint_LateNoteUnnamedTills(t *testing.T) {
	newPrintAPITestDeps(t)
	rep := lateBaseReport()
	rep.LateEntries = &data.EODLateEntries{Tills: []data.EODLateTill{
		{TillID: "aaaaaaaa-1111", SalesCount: 1, Gross: 1},
		{TillID: "bbbbbbbb-2222", SalesCount: 1, Gross: 1},
		{TillID: "cccccccc-3333", Name: "Promoted name", SalesCount: 1, Gross: 1},
	}}
	doc := buildLateDoc(t, "en", rep)
	got := doc.Footer
	for _, want := range []string{"Other till aaaaaaaa", "Other till bbbbbbbb", "Promoted name"} {
		found := false
		for _, l := range got {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing line %q in %q", want, got)
		}
	}
	// Only the late section may not borrow this till's own label for an unnamed till.
	section := strings.Join(got[len(buildLateDoc(t, "en", lateBaseReport()).Footer):], "\n")
	if strings.Contains(section, "This till") || strings.Contains(section, "Main") {
		t.Fatalf("an unnamed till must never borrow this till's label: %q", section)
	}
}

func TestEODPrint_LateNoteEarliest(t *testing.T) {
	mk := func(overall string, perTill string) data.EODReport {
		rep := lateBaseReport()
		rep.LateEntries = &data.EODLateEntries{EarliestOrigin: overall,
			Tills: []data.EODLateTill{{TillID: "t1", Name: "Bar", SalesCount: 1, Gross: 1, EarliestOrigin: perTill}}}
		return rep
	}
	count := func(d print.Doc) int {
		n := 0
		for _, l := range d.Footer {
			if strings.Contains(l, "Earliest made") {
				n++
			}
		}
		return n
	}
	// (i) slice-A shape: overall only, printed once, before the tills.
	d := buildLateDoc(t, "en", mk(lateOriginA, ""))
	if count(d) != 1 || !strings.Contains(strings.Join(d.Footer, "\n"), footerRow("Earliest made", lateWhen(lateOriginA, "en"))) {
		t.Fatalf("overall line: %q", d.Footer)
	}
	// (ii) nothing parseable: no line, the rest is printed.
	d = buildLateDoc(t, "en", mk("", ""))
	if count(d) != 0 || !strings.Contains(strings.Join(d.Footer, "\n"), "Bar") {
		t.Fatalf("empty origin: %q", d.Footer)
	}
	d = buildLateDoc(t, "en", mk("garbage", ""))
	if count(d) != 0 {
		t.Fatalf("garbage origin must print no line: %q", d.Footer)
	}
	// (iii) per-till present: its own line, never the overall one.
	d = buildLateDoc(t, "en", mk(lateOriginA, lateOriginB))
	if count(d) != 1 || !strings.Contains(strings.Join(d.Footer, "\n"), footerRow("  Earliest made", lateWhen(lateOriginB, "en"))) {
		t.Fatalf("per-till line: %q", d.Footer)
	}
	// (iv) ar: no panic, Latin digits on the date line.
	d = buildLateDoc(t, "ar", mk(lateOriginA, lateOriginB))
	for _, l := range d.Footer {
		if strings.Contains(l, lateWhen(lateOriginB, "ar")) {
			for _, r := range lateWhen(lateOriginB, "ar") {
				if r > 127 {
					t.Fatalf("non-ASCII in date %q", l)
				}
			}
			return
		}
	}
	t.Fatalf("ar: date line missing in %q", d.Footer)
}

func TestEODPrint_LateNoteFromArchivedJSON(t *testing.T) {
	// A slice-A archive: no cleared total, no per-till earliest.
	const sliceA = `{"day":"","from":"2026-01-01T08:00:00Z","to":"2026-01-02T08:00:00Z","sales_count":1,"gross":10,"net":9,
"late_entries":{"earliest_origin":"` + lateOriginA + `","tills":[{"till_id":"tx","name":"Bar","sales_count":1,"gross":10,"refund_count":0,"refund_total":0,"no_sale_count":0,"cleared_basket_count":2}]}}`
	var rep data.EODReport
	if err := json.Unmarshal([]byte(sliceA), &rep); err != nil {
		t.Fatal(err)
	}
	doc := buildLateDoc(t, "en", rep)
	joined := strings.Join(doc.Footer, "\n")
	for _, want := range []string{"RECORDED LATE FROM OTHER TILLS", "  Cleared baskets (2)", footerRow("Earliest made", lateWhen(lateOriginA, "en")), footerRow("  Entries", "3")} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, doc.Footer)
		}
	}
	// pre-#2421 and legacy calendar-day archives print as before.
	for _, js := range []string{`{"from":"2026-01-01T08:00:00Z","to":"2026-01-02T08:00:00Z","sales_count":1}`, `{"day":"2026-01-01","sales_count":1}`} {
		var r data.EODReport
		if err := json.Unmarshal([]byte(js), &r); err != nil {
			t.Fatal(err)
		}
		d := buildLateDoc(t, "en", r)
		for _, l := range d.Footer {
			if strings.Contains(l, "LATE") || strings.Contains(l, "Earliest") {
				t.Fatalf("no late section expected, got %q", l)
			}
		}
	}
}

func TestLateNoteHelpers(t *testing.T) {
	for in, want := range map[string]string{"": "", "abc": "abc", "0123456789": "01234567", "ÄÖÜäöüßß99": "ÄÖÜäöüßß"} {
		if got := lateTillShortID(in); got != want {
			t.Fatalf("lateTillShortID(%q) = %q, want %q", in, got, want)
		}
	}
	if n := lateTillEntries(data.EODLateTill{SalesCount: 1, RefundCount: 2, NoSaleCount: 3, ClearedBasketCount: 4}); n != 10 {
		t.Fatalf("entries = %d, want 10", n)
	}
	T := func(k string) string { return map[string]string{"reports.eod.late_other_till": "Other till %s"}[k] }
	if got := lateTillLabel(data.EODLateTill{TillID: ""}, T); got != "Other till" {
		t.Fatalf("empty id label = %q", got)
	}
	if got := lateTillLabel(data.EODLateTill{TillID: "abcdefghij", Name: "N"}, T); got != "N" {
		t.Fatalf("named label = %q", got)
	}
	if lateTillEntries(data.EODLateTill{}) != 0 || strconv.Itoa(0) != "0" {
		t.Fatal("zero")
	}
	if eodLateNoteView(nil) != nil || eodLateNoteView(&data.EODLateEntries{}) != nil {
		t.Fatal("view must be nil for nil/empty")
	}
}
