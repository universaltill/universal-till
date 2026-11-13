package pages

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeVoucherSlipText(t *testing.T) {
	ok := []struct{ name, in, want string }{
		{"crlf", "a\r\nb\rc d", "a\nb\nc\nd"},
		{"tab", "a\tb", "a b"},
		{"controls", "a\x1b@b\x00c\x7fd\u0085e‮f", "a@bcdef"},
		{"lrm kept", "a‎b", "a‎b"},
		{"edge blanks", "\n\n  a  \n\n b \n\n", "a\n\nb"},
		{"inner blank", "a\n\nb", "a\n\nb"},
		{"empty", "", ""},
		{"spaces", "  \n\t \n ", ""},
		{"4 lines", "a\nb\nc\nd", "a\nb\nc\nd"},
		{"160 a-umlaut", strings.Repeat("ä", 160), strings.Repeat("ä", 160)},
		{"4 lines 160", strings.Repeat("x", 40) + "\n" + strings.Repeat("x", 40) + "\n" + strings.Repeat("x", 40) + "\n" + strings.Repeat("x", 40),
			strings.Repeat("x", 40) + "\n" + strings.Repeat("x", 40) + "\n" + strings.Repeat("x", 40) + "\n" + strings.Repeat("x", 40)},
		{"invalid utf8", "a\xffb", "ab"},
	}
	for _, c := range ok {
		got, err := normalizeVoucherSlipText(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
			continue
		}
		again, err := normalizeVoucherSlipText(got)
		if err != nil || again != got {
			t.Errorf("%s: not idempotent: %q -> %q", c.name, got, again)
		}
	}
	for name, in := range map[string]string{
		"5 lines":                      "a\nb\nc\nd\ne",
		"161":                          strings.Repeat("ä", 161),
		"inner blank counts as a line": "a\n\n\n\nb",
	} {
		if got, err := normalizeVoucherSlipText(in); err != errVoucherSlipTextTooLong || got != "" {
			t.Errorf("%s: got %q, %v; want too long", name, got, err)
		}
	}
}

func TestLenientVoucherSlipText_NeverRefuses(t *testing.T) {
	got := lenientVoucherSlipText("a\r\nb\nc\nd\ne\x1b")
	if got != "a\nb\nc\nd\ne" {
		t.Errorf("got %q", got)
	}
}

func TestVoucherSlipLines(t *testing.T) {
	if voucherSlipLines("") != nil {
		t.Error("empty must be nil")
	}
	if got := voucherSlipLines("a\n\nb"); len(got) != 3 || got[1] != "" {
		t.Errorf("got %q", got)
	}
}

func TestSlipDateLineIn(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata")
	}
	const k = "receipt.vouchers.issued_on"
	for _, bad := range []string{"", "garbage", "0001-01-01T00:00:00Z"} {
		if got := slipDateLineIn("en-GB", k, bad, berlin); got != "" {
			t.Errorf("%q gave %q", bad, got)
		}
	}
	cases := []struct{ loc, stored, want string }{
		{"de", "2025-03-01T23:30:00Z", "Issued 02.03.2025"},
		{"en-GB", "2025-03-01T23:30:00Z", "Issued 02/03/2025"},
		{"en-US", "2025-03-01T23:30:00Z", "Issued 03/02/2025"},
		{"en-GB", "2025-03-01 09:00:00", "Issued 01/03/2025"},
		{"ar", "2025-03-01T10:00:00Z", "Issued 01/03/2025"},
	}
	for _, c := range cases {
		got := slipDateLineIn(c.loc, k, c.stored, berlin)
		// The label follows the locale pack; compare the date part only.
		wantDate := c.want[strings.LastIndex(c.want, " ")+1:]
		if !strings.HasSuffix(got, wantDate) {
			t.Errorf("%s %s: got %q, want date %q", c.loc, c.stored, got, wantDate)
		}
	}
}
