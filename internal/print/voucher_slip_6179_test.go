package print

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderVoucherSlip_DateLine(t *testing.T) {
	for name, base := range slip6179Variants() {
		s := base
		s.Date = "Issued 01/03/2025"
		out := RenderVoucherSlip(s)
		date := encodeText(s.Date, s.Charset)
		if bytes.Count(out, date) != 1 {
			t.Fatalf("%s: date line count = %d", name, bytes.Count(out, date))
		}
		i := bytes.Index(out, date)
		if code := bytes.Index(out, []byte(s.Voucher.Code)); code < 0 || code > i {
			t.Errorf("%s: date must follow the code", name)
		}
		if n := bytes.Index(out, encodeText(s.Note, s.Charset)); s.Note != "" && n < i {
			t.Errorf("%s: date must precede the Note", name)
		}
		if i > bytes.LastIndex(out, cmdFeedCut) {
			t.Errorf("%s: date after feed+cut", name)
		}
		if !bytes.Equal(out[i-len(cmdAlignMid):i], cmdAlignMid) {
			t.Errorf("%s: date not centred", name)
		}
		// Only the date line is added versus the pre-change output.
		want := RenderVoucherSlip(base)
		extra := len(cmdAlignMid) + len(date) + 1 + len(cmdAlignLeft)
		if len(out) != len(want)+extra {
			t.Errorf("%s: added %d bytes, want %d", name, len(out)-len(want), extra)
		}
	}
	s := sampleSlip("GV-1", "")
	s.Date = "Issued 01/03/2025"
	if got := RenderVoucherSlipText(s); !strings.Contains(got, "Issued 01/03/2025") {
		t.Errorf("text slip lacks the date:\n%s", got)
	}
}

func TestRenderVoucherSlip_Text(t *testing.T) {
	s := sampleSlip("GV-1", "")
	s.Note = "Reprinted 06/11/2026"
	s.Text = []string{"Valid in store.", "", "No cash refund."}
	out := RenderVoucherSlip(s)
	note := bytes.Index(out, encodeText(s.Note, s.Charset))
	for _, l := range []string{"Valid in store.", "No cash refund."} {
		i := bytes.Index(out, encodeText(l, s.Charset))
		if i < 0 {
			t.Fatalf("lacks %q", l)
		}
		if i < note {
			t.Errorf("%q printed before the Note", l)
		}
	}
	// Order: Note, blank, text (centred), blank, rule, feed+cut.
	tail := string(out[note:])
	wantOrder := "\n" + string(cmdAlignLeft) + "\n" + string(cmdAlignMid) + "Valid in store.\n\nNo cash refund.\n" + string(cmdAlignLeft) + "\n" + strings.Repeat("-", Width) + "\n"
	if !strings.Contains(tail, wantOrder) {
		t.Errorf("layout wrong: %q", tail)
	}
	if !bytes.HasSuffix(out, cmdFeedCut) || bytes.Count(out, cmdFeedCut) != 1 {
		t.Error("want exactly one feed+cut at the end")
	}
	if bytes.Contains(out, cmdKickDrawer) || bytes.Contains(out, cmdKickDrawerPin5) {
		t.Error("slip must never kick the drawer")
	}
	txt := RenderVoucherSlipText(s)
	rows := strings.Split(txt, "\n")
	var iNote, iText int
	for i, r := range rows {
		if strings.Contains(r, "Reprinted") {
			iNote = i
		}
		if strings.Contains(r, "Valid in store.") {
			iText = i
		}
	}
	if iText != iNote+2 || strings.TrimSpace(rows[iNote+1]) != "" {
		t.Errorf("text slip: blank line then text expected:\n%s", txt)
	}
	if strings.TrimSpace(rows[iText+1]) != "" || !strings.Contains(rows[iText+2], "No cash refund.") {
		t.Errorf("inner blank line lost:\n%s", txt)
	}
}

func TestRenderVoucherSlipText_TextWraps(t *testing.T) {
	long := strings.Repeat("word ", 12) // 60 chars incl. trailing space trimmed by Fields
	long = strings.TrimSpace(long)
	cjk := strings.Repeat("漢", 30)
	unbroken := strings.Repeat("x", 50)
	combining := strings.Repeat("a", 41) + "é" + "bb"
	s := sampleSlip("GV-1", "")
	s.Text = []string{long, unbroken, cjk, combining}
	out := RenderVoucherSlipText(s)
	var textRows []string
	started := false
	for _, r := range strings.Split(out, "\n") {
		if strings.Contains(r, "word") {
			started = true
		}
		if started {
			textRows = append(textRows, r)
		}
		if d := displayWidth(r); d > Width {
			t.Errorf("row %q is %d columns", r, d)
		}
	}
	if strings.Count(out, "word") != 12 {
		t.Errorf("sentence words lost")
	}
	// 60-char sentence -> 2 rows
	n := 0
	for _, r := range strings.Split(out, "\n") {
		if strings.Contains(r, "word") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("60-char sentence wrapped to %d rows, want 2", n)
	}
	if strings.Count(out, "x") != 50 {
		t.Error("unbroken word lost characters")
	}
	if strings.Count(out, "漢") != 30 {
		t.Error("CJK lost characters")
	}
	if !strings.Contains(out, "é") {
		t.Error("combining mark separated from its base")
	}
}

func TestRenderVoucherSlip_TextControlRunesNeverReachPrinter(t *testing.T) {
	s := sampleSlip("GV-1", "")
	s.Text = []string{"\x1b@x", "a‮b"}
	out := RenderVoucherSlip(s)
	start := bytes.Index(out, []byte("GV-1"))
	body := out[start : len(out)-len(cmdFeedCut)]
	// cmdAlignMid/Left legitimately carry ESC; strip them before checking.
	body = bytes.ReplaceAll(body, cmdAlignMid, nil)
	body = bytes.ReplaceAll(body, cmdAlignLeft, nil)
	if bytes.IndexByte(body, 0x1b) >= 0 {
		t.Errorf("raw ESC reached the printer: %q", body)
	}
	if strings.Contains(RenderVoucherSlipText(s), "‮") || strings.Contains(RenderVoucherSlipText(s), "\x1b") {
		t.Error("text slip kept a control rune")
	}
}

func TestRender_SlipDateAndTextDoNotChangeReceipt(t *testing.T) {
	doc := Doc{StoreName: "Shop", Charset: "utf8", Footer: []string{"Thanks"},
		Vouchers:       []VoucherBlock{{Label: "Gift voucher", Amount: "5.00", Code: "GV-1", Slip: true}},
		VoucherHeading: "Vouchers"}
	with := doc
	with.VoucherSlipText = []string{"Valid in store."}
	with.Vouchers = []VoucherBlock{doc.Vouchers[0]}
	with.Vouchers[0].SlipDate = "Issued 01/03/2025"
	if !bytes.Equal(Render(doc), Render(with)) {
		t.Error("ESC/POS receipt changed")
	}
	if RenderText(doc) != RenderText(with) {
		t.Error("text receipt changed")
	}
}

// TestRenderVoucherSlip_TextWrapsESC (review, ut-docs#6179): the ESC/POS slip
// word-wraps the shop's text to Width like the plain-text slip does — a
// 60-column sentence prints as two rows, none wider than the paper, every
// word kept, all inside the centred text block.
func TestRenderVoucherSlip_TextWrapsESC(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("word ", 12)) // 59 columns
	s := sampleSlip("GV-1", "")
	s.Text = []string{long, "No cash refund."}
	out := RenderVoucherSlip(s)
	start := bytes.LastIndex(out, cmdAlignMid)
	end := bytes.LastIndex(out, cmdAlignLeft)
	if start < 0 || end < start {
		t.Fatalf("no centred text block: %q", out)
	}
	block := strings.TrimSuffix(string(out[start+len(cmdAlignMid):end]), "\n")
	rows := strings.Split(block, "\n")
	want := []string{
		strings.TrimSpace(strings.Repeat("word ", 8)),
		strings.TrimSpace(strings.Repeat("word ", 4)),
		"No cash refund.",
	}
	if len(rows) != len(want) {
		t.Fatalf("text block rows = %q, want %q", rows, want)
	}
	for i, r := range rows {
		if displayWidth(r) > Width {
			t.Errorf("row %q is %d columns, over %d", r, displayWidth(r), Width)
		}
		if r != want[i] {
			t.Errorf("row %d = %q, want %q", i, r, want[i])
		}
	}
}

// TestRenderVoucherSlipText_DateBeforeNote (review, ut-docs#6179): on the
// plain-text slip the "Issued" date follows the code and precedes the
// "Reprinted" note, matching the ESC/POS order.
func TestRenderVoucherSlipText_DateBeforeNote(t *testing.T) {
	s := sampleSlip("GV-1", "")
	s.Date = "Issued 01/03/2025"
	s.Note = "Reprinted 02/03/2025"
	s.Text = []string{"Valid in store."}
	rows := strings.Split(RenderVoucherSlipText(s), "\n")
	idx := func(sub string) int {
		for i, r := range rows {
			if strings.Contains(r, sub) {
				return i
			}
		}
		t.Fatalf("no row contains %q:\n%s", sub, strings.Join(rows, "\n"))
		return -1
	}
	code, date, note, text := idx("GV-1"), idx("Issued"), idx("Reprinted"), idx("Valid in store.")
	if !(code < date && date+1 == note && note < text) {
		t.Errorf("order code=%d date=%d note=%d text=%d, want code < date, note right after date, text last", code, date, note, text)
	}
}
