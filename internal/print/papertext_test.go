package print

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// Shared fixtures for the ut-docs#5847 control-rune tests.
var allCharsets = []string{"utf8", "", "ascii", "cp858", "win1250", "win1253", "win1254", "win1257"}

// kick is the literal cmdKickDrawer bytes; kickTwin is what paperText makes of
// it (0xFA alone is invalid UTF-8, so it becomes '?').
const (
	kick       = "\x1bp\x00\x19\xfa"
	kickTwin   = " p  ?"
	forged     = "\nTOTAL 0.00"
	forgedTwin = " TOTAL 0.00"
)

// twinOf maps a dirty fixture string to its sanitised twin without using
// paperText, so the byte-equality tests are not circular.
var twinOf = strings.NewReplacer(kick, kickTwin, forged, forgedTwin).Replace

func TestPaperText_ReplacesEachControlRuneWithOneSpace(t *testing.T) {
	for _, r := range []rune{0, '\t', '\n', '\r', '\f', 0x10, 0x1b, 0x1c, 0x1d, 0x7f, 0x85, 0x9b, 0x2028, 0x2029, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e} {
		in := "A" + string(r) + "B"
		got := paperText(in)
		if got != "A B" {
			t.Errorf("paperText(%U) = %q, want %q", r, got, "A B")
		}
		if displayWidth(got) != displayWidth(in) {
			t.Errorf("%U: width %d -> %d", r, displayWidth(in), displayWidth(got))
		}
	}
}

func TestPaperText_InvalidUTF8ByteBecomesQuestionMark(t *testing.T) {
	for in, want := range map[string]string{"a\xfab": "a?b", "\xff\xfe": "??"} {
		if got := paperText(in); got != want {
			t.Errorf("paperText(%q) = %q, want %q", in, got, want)
		}
		if displayWidth(paperText(in)) != displayWidth(in) {
			t.Errorf("%q: width changed", in)
		}
	}
	if got := paperText("a�b"); got != "a�b" {
		t.Errorf("a valid U+FFFD must be kept, got %q", got)
	}
}

func TestPaperText_KeepsBidiMarksIsolatesAndJoiners(t *testing.T) {
	keep := "x‌‍‎‏؜⁦⁧⁨⁩y"
	if got := paperText(keep); got != keep {
		t.Fatalf("got %q, want unchanged %q", got, keep)
	}
	if n := testing.AllocsPerRun(10, func() { _ = paperText(keep) }); n != 0 {
		t.Errorf("clean input allocated %v times, want 0", n)
	}
	// Real locale labels carry U+200E / isolates and must survive untouched.
	for _, f := range []string{"ar", "fa"} {
		raw, err := os.ReadFile("../../web/locales/" + f + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		seen := 0
		for k, v := range m {
			if !strings.ContainsAny(v, "‎⁦⁨⁩") {
				continue
			}
			seen++
			if got := paperText(v); got != v {
				t.Errorf("%s %s: label changed: %q -> %q", f, k, v, got)
			}
			if got := encodeText(v, "utf8"); !bytes.Equal(got, []byte(v)) {
				t.Errorf("%s %s: utf8 bytes changed", f, k)
			}
		}
		if seen == 0 {
			t.Errorf("%s: expected labels carrying bidi marks", f)
		}
	}
	if !utf8.ValidString(keep) {
		t.Fatal("fixture invalid")
	}
}
