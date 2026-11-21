package pages

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#6691: the late note's explanation lines and labels wrap, never clip.

func wrapStubT(over map[string]string) func(string) string {
	base := map[string]string{
		"reports.zpaper.late_title":       "RECORDED LATE FROM OTHER TILLS",
		"reports.zpaper.late_made_before": "Made before the previous close",
		"reports.zpaper.late_included":    "Already included in the totals above",
		"reports.zpaper.late_entries":     "Entries",
		"reports.zpaper.late_sales":       "Sales (%d)",
		"reports.zpaper.late_cleared":     "Cleared baskets (%d)",
		"reports.zpaper.late_earliest":    "Earliest made",
		"reports.eod.late_other_till":     "Other till %s",
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func wrapMoney(m int64) string { return "£" + strconv.FormatInt(m, 10) }
func wrapNoWhen(string) string { return "" }
func wrapFooter(le *data.EODLateEntries, over map[string]string) []string {
	return eodLateNoteFooter(le, wrapStubT(over), wrapMoney, wrapNoWhen)
}

func oneTill(name string) *data.EODLateEntries {
	return &data.EODLateEntries{Tills: []data.EODLateTill{{TillID: "till-a", Name: name, SalesCount: 2, Gross: 750}}}
}

func TestLateNoteFooter_EnglishUnchanged6691(t *testing.T) {
	le := &data.EODLateEntries{Tills: []data.EODLateTill{
		{TillID: "till-a", Name: "Bar till", SalesCount: 2, Gross: 750, ClearedBasketCount: 1},
		{TillID: "till-b", SalesCount: 0, NoSaleCount: 1},
	}}
	want := []string{"", "RECORDED LATE FROM OTHER TILLS", "Made before the previous close", "Already included in the totals above",
		"Bar till", footerRow("  Entries", "3"), footerRow("  Sales (2)", "£750"), "  Cleared baskets (1)",
		"Other till till-b", footerRow("  Entries", "1")}
	want = append(want, footerRow("  Drawer opens", "1"))
	got := eodLateNoteFooter(le, func(k string) string {
		if k == "reports.zpaper.late_no_sale" {
			return "Drawer opens"
		}
		return wrapStubT(nil)(k)
	}, wrapMoney, wrapNoWhen)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestLateNoteFooter_WrapsLongExplanation6691(t *testing.T) {
	text := "Already included in the totals above and never added a second time"
	got := wrapFooter(oneTill("A"), map[string]string{"reports.zpaper.late_included": text})
	rows := got[3:5]
	if print.DisplayWidth(text) <= print.Width {
		t.Fatal("fixture too short")
	}
	for _, r := range rows {
		if r == "" || print.DisplayWidth(r) > print.Width {
			t.Fatalf("bad row %q", r)
		}
	}
	if strings.Join(strings.Fields(strings.Join(rows, " ")), " ") != text {
		t.Fatalf("text lost: %q", rows)
	}
	if got[5] != "A" {
		t.Fatalf("a blank or extra row between wrap and till: %q", got[5])
	}
}

func TestLateNoteFooter_HardWrapsLongWord6691(t *testing.T) {
	w := strings.Repeat("w", 50)
	got := wrapFooter(oneTill("A"), map[string]string{"reports.zpaper.late_made_before": w})
	if got[2] != w[:42] || got[3] != w[42:] {
		t.Fatalf("rows %q %q", got[2], got[3])
	}
}

func TestLateNoteFooter_ZeroWidthByDisplayWidth6691(t *testing.T) {
	// 20 words of "ab‌cd" joined by spaces would be long; build one that is
	// over 42 runes but at most 42 columns: 15 ZWNJ-joined words of 4 letters.
	var ws []string
	for i := 0; i < 8; i++ {
		ws = append(ws, "باقی‌مانده") // 4+1+5 runes, 9 columns
	}
	fits := strings.Join(ws[:4], " ") // 4*10+3 = 43 runes, 39 columns
	if len([]rune(fits)) <= 42 || print.DisplayWidth(fits) > 42 {
		t.Fatalf("fixture: runes %d cols %d", len([]rune(fits)), print.DisplayWidth(fits))
	}
	got := wrapFooter(oneTill("A"), map[string]string{"reports.zpaper.late_title": fits})
	if got[1] != fits || got[2] != "Made before the previous close" {
		t.Fatalf("a fitting text was cut or wrapped: %q", got[:3])
	}
	long := strings.Join(ws, " ")
	got = wrapFooter(oneTill("A"), map[string]string{"reports.zpaper.late_title": long})
	n := 0
	for i := 1; got[i] != "Made before the previous close"; i++ {
		n++
		if print.DisplayWidth(got[i]) > print.Width {
			t.Fatalf("row too wide %q", got[i])
		}
	}
	if n < 2 {
		t.Fatalf("expected a wrap, got %d row(s)", n)
	}
	text := print.RenderText(print.Doc{Footer: got})
	for _, r := range strings.ReplaceAll(strings.ReplaceAll(long, " ", ""), "‌", "") {
		if !strings.ContainsRune(text, r) {
			t.Fatalf("rune %q lost in render", r)
		}
	}
}

func TestLateNoteFooter_LongTillNameWraps6691(t *testing.T) {
	name := strings.Repeat("Longname ", 6) + "end" // 57 chars
	got := wrapFooter(oneTill(name), nil)
	var rows []string
	i := 4
	for ; !strings.HasPrefix(got[i], "  Entries"); i++ {
		rows = append(rows, got[i])
	}
	if len(rows) != 2 {
		t.Fatalf("name rows = %q", rows)
	}
	if strings.Join(strings.Fields(strings.Join(rows, " ")), " ") != strings.Join(strings.Fields(name), " ") {
		t.Fatalf("name lost: %q", rows)
	}
	if !strings.HasSuffix(got[i], "2") || !strings.HasSuffix(got[i+1], "£750") {
		t.Fatalf("count/amount rows: %q %q", got[i], got[i+1])
	}
}

func TestLateNoteFooter_ClearedCountLabelKeepsIndent6691(t *testing.T) {
	le := &data.EODLateEntries{Tills: []data.EODLateTill{{TillID: "t", Name: "A", ClearedBasketCount: 3}}}
	got := wrapFooter(le, map[string]string{"reports.zpaper.late_cleared": "Cleared baskets that were abandoned before payment (%d)"})
	rows := got[6:] // after the name row and the Entries row
	if len(rows) < 2 {
		t.Fatalf("expected wrapped label, got %q", rows)
	}
	for _, r := range rows {
		if !strings.HasPrefix(r, "  ") || strings.HasPrefix(r, "   ") || print.DisplayWidth(r) > print.Width {
			t.Fatalf("bad indent/width %q", r)
		}
	}
}

func TestLateNoteFooter_FooterRowsKeepAmount6691(t *testing.T) {
	got := wrapFooter(oneTill("A"), map[string]string{"reports.zpaper.late_sales": strings.Repeat("Verkäufe ", 8) + "(%d)"})
	last := got[len(got)-1]
	if !strings.HasSuffix(last, "£750") || print.DisplayWidth(last) > print.Width {
		t.Fatalf("amount cut: %q", last)
	}
}

func TestLateNoteFooter_RTLWrapLogicalOrder6691(t *testing.T) {
	text := "تم إدخال هذه العمليات قبل الإغلاق السابق ثم وصلت متأخرة إلى هذا الجهاز"
	got := wrapFooter(oneTill("A"), map[string]string{"reports.zpaper.late_included": text})
	var rows []string
	for i := 3; got[i] != "A"; i++ {
		rows = append(rows, got[i])
		if print.DisplayWidth(got[i]) > print.Width {
			t.Fatalf("row too wide %q", got[i])
		}
	}
	if len(rows) < 2 || strings.Join(strings.Fields(strings.Join(rows, " ")), " ") != text {
		t.Fatalf("order/text: %q", rows)
	}
}
