package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#4689: report text measures printer columns (print.DisplayWidth), so
// a ZWNJ or combining mark in a translated label takes no column.
const faRemaining = "باقی\u200cمانده" // has a ZWNJ

func TestFooterRowDisplayWidth4689(t *testing.T) {
	// ZWNJ inside the kept part: Clip must not count it as a column.
	long := strings.Repeat("ی\u200c", 60)
	for _, tt := range []struct{ name, label, amount string }{
		{"ascii", "Remaining", "12.50"},
		{"zwnj", faRemaining, "12.50"},
		{"combining", "éé", "12.50"},
		{"zwnj overlong clipped", long, "12.50"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := footerRow(tt.label, tt.amount)
			if got := print.DisplayWidth(row); got != print.Width {
				t.Errorf("DisplayWidth(%q) = %d, want %d", row, got, print.Width)
			}
			if !strings.HasSuffix(row, tt.amount) {
				t.Errorf("row %q lost its amount", row)
			}
		})
	}
}

func TestEodRowDisplayWidth4689(t *testing.T) {
	// A ZWNJ label pads to the same 20 columns + space + amount as ASCII.
	plain := eodRow("Remaini", "12.50") // 7 visible columns
	zw := eodRow("Rem\u200caini", "12.50")
	if print.DisplayWidth(zw) != print.DisplayWidth(plain) {
		t.Errorf("zwnj row %q width %d != plain row %q width %d", zw, print.DisplayWidth(zw), plain, print.DisplayWidth(plain))
	}
	if want := 20 + 1 + 5; print.DisplayWidth(zw) != want {
		t.Errorf("DisplayWidth(%q) = %d, want %d", zw, print.DisplayWidth(zw), want)
	}
	// Overlong fa label falls back to footerRow: amount ends at Width.
	row := eodRow(strings.Repeat(faRemaining+" ", 4), "1,234.50")
	if got := print.DisplayWidth(row); got != print.Width {
		t.Errorf("DisplayWidth(%q) = %d, want %d", row, got, print.Width)
	}
}

func TestRateTableRowsDisplayWidth4689(t *testing.T) {
	money := func(m int64) string { return "1.00" }
	for _, hdr := range []rateTableHeader{
		{Net: "Net", Tax: "Tax", Gross: "Gross"},
		{Net: "خالص\u200cخالص", Tax: "مالیات", Gross: "کل"},
		{Net: "Nét", Tax: "Táx", Gross: "Gróss"},
	} {
		lines := rateTableRows([]int{700, 1900}, []int64{1, 1}, []int64{1, 1}, []int64{1, 1}, money, hdr)
		w := print.DisplayWidth(lines[0])
		for _, l := range lines {
			if print.DisplayWidth(l) != w {
				t.Errorf("hdr %+v: line %q width %d != header width %d", hdr, l, print.DisplayWidth(l), w)
			}
		}
	}
}

func TestPrintReportColsDisplayWidth4689(t *testing.T) {
	plain := printReportCols([]string{"abcdef"})
	zw := printReportCols([]string{"abc\u200cdef"})
	if print.DisplayWidth(zw) != print.DisplayWidth(plain) {
		t.Errorf("zwnj col %q width %d != plain %q width %d", zw, print.DisplayWidth(zw), plain, print.DisplayWidth(plain))
	}
}

// Amounts too wide for the historical layout force the tight one (w = tw):
// columns still line up by display width and nothing passes print.Width.
func TestRateTableRowsTightLayoutDisplayWidth4689(t *testing.T) {
	money := func(m int64) string { return "£" + strings.Repeat("9", int(m)) + ".99" }
	hdr := rateTableHeader{Net: "خالص\u200cخالص", Tax: "مالیات", Gross: "کل\u200cکل"}
	lines := rateTableRows([]int{700, 1950}, []int64{7, 6}, []int64{6, 5}, []int64{9, 7}, money, hdr)
	w := print.DisplayWidth(lines[0])
	for _, l := range lines {
		if n := print.DisplayWidth(l); n != w || n > print.Width {
			t.Errorf("line %q width %d, want %d (<= %d)", l, n, w, print.Width)
		}
	}
}

// A tips row whose label overflows keeps its amount (it goes through eodRow
// now; "%-20s %s" let the renderer's clip cut the pence off).
func TestTipsRowOverflowKeepsAmount4689(t *testing.T) {
	row := eodRow("12x Some_plugin_gift_card_provider", "£1,234.56")
	if print.DisplayWidth(row) != print.Width || !strings.HasSuffix(row, " £1,234.56") {
		t.Errorf("tips row %q: want %d columns ending in the amount", row, print.Width)
	}
}
