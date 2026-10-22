package print

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// faChange is the fa value of receipt.change: it carries a ZWNJ (U+200C)
// between "باقی" and "مانده", a character with no column of its own
// (ut-docs#4685).
const faChange = "باقی\u200cمانده"

func TestDisplayWidth(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "Total", 5},
		{"multi-byte letters count one each", "£2.80 Café", 10},
		{"ZWNJ takes no column", faChange, 9},
		{"ZWJ takes no column", "a\u200db", 2},
		{"combining mark (Mn) takes no column", "e\u0301", 1},
		{"enclosing mark (Me) takes no column", "1\u20dd", 1},
		{"Arabic harakat take no column", "ب\u064e", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayWidth(tt.s); got != tt.want {
				t.Errorf("displayWidth(%q) = %d, want %d", tt.s, got, tt.want)
			}
		})
	}
}

// The card's AC: a row labelled باقی\u200cمانده right-aligns its amount with the
// other rows, in both the preview (RenderText) and the printed text (Render).
func TestZeroWidthLabelRowAlignsWithOtherRows(t *testing.T) {
	d := sampleDoc()
	d.Totals = []KV{{Label: "جمع", Amount: "£5.00"}}
	d.Payments = []KV{{Label: "نقد", Amount: "£10.00"}, {Label: faChange, Amount: "£5.00"}}

	for name, out := range map[string]string{
		"RenderText": RenderText(d),
		"Render":     string(Render(d)),
	} {
		t.Run(name, func(t *testing.T) {
			var change, cash string
			for _, row := range strings.Split(out, "\n") {
				switch {
				case strings.Contains(row, faChange):
					change = row
				case strings.Contains(row, "نقد"):
					cash = row
				}
			}
			if change == "" || cash == "" {
				t.Fatalf("rows not found in output:\n%s", out)
			}
			// Independent of displayWidth: the row ends in its amount, and the
			// padding before it is exactly what a 9-column label leaves.
			if want := strings.Repeat(" ", Width-9-5) + "£5.00"; !strings.HasSuffix(change, faChange+want) {
				t.Errorf("change row = %q, want label + %d spaces + amount", change, Width-9-5)
			}
			if got := displayWidth(change); got != Width {
				t.Errorf("change row is %d columns wide, want %d: %q", got, Width, change)
			}
			if got := displayWidth(cash); got != Width {
				t.Errorf("cash row is %d columns wide, want %d: %q", got, Width, cash)
			}
		})
	}
}

func TestKvRowZeroWidthLabelPadsToFullWidth(t *testing.T) {
	en := kvRow("Change", "£5.00")
	fa := kvRow(faChange, "£5.00")
	if got := displayWidth(fa); got != Width {
		t.Errorf("fa row is %d columns wide, want %d: %q", got, Width, fa)
	}
	// en output is unchanged: still padded to Width runes.
	if got := utf8.RuneCountInString(en); got != Width {
		t.Errorf("en row is %d runes wide, want %d: %q", got, Width, en)
	}
}

func TestLayoutLineZeroWidthNameFitsOneRow(t *testing.T) {
	// A name exactly filling the row in columns, but one rune over because of
	// its ZWNJ, still fits on one row.
	amount := "£1.00"
	name := faChange + strings.Repeat("ی", Width-1-utf8.RuneCountInString(amount)-displayWidth(faChange))
	rows := layoutLine(Line{Name: name, Qty: "1", Amount: amount})
	if len(rows) != 1 {
		t.Fatalf("want one row, got %d: %q", len(rows), rows)
	}
	if got := displayWidth(rows[0]); got != Width {
		t.Errorf("row is %d columns wide, want %d: %q", got, Width, rows[0])
	}
}

func TestClipKeepsZeroWidthRunesWithTheirBase(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"combining mark stays with its base", "ae\u0301b", 2, "ae\u0301"},
		{"ZWNJ does not use up a column", faChange, 9, faChange},
		{"cut after the ZWNJ's neighbour", faChange, 4, "باقی\u200c"},
		{"ascii unchanged", "hello world", 5, "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clip(tt.s, tt.max); got != tt.want {
				t.Errorf("clip(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
		})
	}
}

func TestRenderTextCentersByDisplayWidth(t *testing.T) {
	d := sampleDoc()
	// 10 columns, 11 runes: rune-count centring pads 15, display-width 16.
	name := faChange + "ی"
	d.StoreName = name
	out := RenderText(d)
	want := (Width - displayWidth(name)) / 2
	if got := leadingSpacesStr(t, out, name); got != want {
		t.Errorf("store name: want %d leading spaces, got %d", want, got)
	}
}

func TestWrapWordsMeasuresDisplayWidth(t *testing.T) {
	// Two words of 4 and 5 columns fit a 10-column row even though the ZWNJ
	// makes the rune count 11.
	s := "باقی\u200c مانده"
	if rows := wrapWords(s, 10); len(rows) != 1 {
		t.Errorf("want one row, got %q", rows)
	}
}

func TestWrapRunesMeasuresDisplayWidth(t *testing.T) {
	// 4 columns + ZWNJ + 5 columns: one 10-column row, not two.
	if rows := wrapRunes(faChange+"x", 10); len(rows) != 1 {
		t.Errorf("want one row, got %q", rows)
	}
	// A combining mark at the cut stays with its base.
	rows := wrapRunes("aébc", 2)
	if len(rows) != 2 || rows[0] != "aé" || rows[1] != "bc" {
		t.Errorf("got %q, want [\"ae\\u0301\" \"bc\"]", rows)
	}
}

// On a single-byte code page a zero-width rune must print nothing: kvRow
// padded it as zero columns, so a '?' would push the row past Width.
func TestCodePageRowsStayWidthWithZeroWidthRunes(t *testing.T) {
	for _, cs := range []string{"ascii", "cp858", "win1250", "win1257", "win1253", "win1254"} {
		t.Run(cs, func(t *testing.T) {
			for _, label := range []string{"Café", "Kaffee‌Bar"} {
				row := encodeText(kvRow(label, "5.00"), cs)
				if len(row) != Width {
					t.Errorf("%q: row is %d bytes on %s, want %d: %q", label, len(row), cs, Width, row)
				}
				if bytes.ContainsRune(row, '?') {
					t.Errorf("%q: row prints a '?' on %s: %q", label, cs, row)
				}
			}
		})
	}
}

func TestEncodableAcceptsZeroWidthJoiners(t *testing.T) {
	if !Encodable("Kaffee‌Bar", "cp858") {
		t.Error("a ZWNJ is dropped on cp858, not degraded: want Encodable")
	}
}

// The exported wrappers (ut-docs#4689) must agree with the internal helpers.
func TestExportedDisplayWidthAndClip(t *testing.T) {
	for _, s := range []string{"", "abc", "باقی‌مانده", "éa", "£1.00"} {
		if got, want := DisplayWidth(s), displayWidth(s); got != want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", s, got, want)
		}
		for _, max := range []int{-1, 0, 1, 3, 20} {
			if got, want := Clip(s, max), clip(s, max); got != want {
				t.Errorf("Clip(%q, %d) = %q, want %q", s, max, got, want)
			}
		}
	}
	if got := DisplayWidth("باقی‌مانده"); got != 9 {
		t.Errorf("DisplayWidth(fa ZWNJ label) = %d, want 9", got)
	}
}
