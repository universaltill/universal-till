package print

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestDefaultCharset(t *testing.T) {
	cases := []struct {
		name             string
		currency, locale string
		want             string
	}{
		// The reported case (ut-docs#1728): a German EUR store printing €.
		{"german euro store", "EUR", "de-DE", "cp858"},
		{"uk sterling store", "GBP", "en-GB", "cp858"},
		{"french euro store", "EUR", "fr-FR", "cp858"},
		{"region not enumerated still resolves on language", "EUR", "de-AT", "cp858"},
		{"lowercase and underscore forms", "eur", "nl_NL", "cp858"},
		// A '$' is plain ASCII — nothing is broken, so nothing changes.
		{"us dollar store keeps the pass-through", "USD", "en-US", "utf8"},
		// CP858 has neither 'ı' nor 'ş': switching Turkish would replace a
		// possibly-fine receipt with one full of '?'. Turkish is deliberately
		// left out of ut-docs#1733's fix (own market:tr track) even though
		// Windows-1254 would cover it — not this card's scope.
		{"turkish store is not switched", "TRY", "tr-TR", "utf8"},
		{"turkish store on euro is still not switched", "EUR", "tr-TR", "utf8"},
		// ut-docs#1733: eight eurozone locales CP858 cannot reach without
		// losing their own alphabet, resolved via the Windows-125x pages the
		// same printers already support.
		{"greek euro store gets win1253", "EUR", "el-GR", "win1253"},
		{"croatian euro store gets win1250", "EUR", "hr-HR", "win1250"},
		{"slovenian euro store gets win1250", "EUR", "sl-SI", "win1250"},
		{"slovak euro store gets win1250", "EUR", "sk-SK", "win1250"},
		{"estonian euro store gets win1257", "EUR", "et-EE", "win1257"},
		{"latvian euro store gets win1257", "EUR", "lv-LV", "win1257"},
		{"lithuanian euro store gets win1257", "EUR", "lt-LT", "win1257"},
		{"region not enumerated still resolves win1250", "EUR", "sk-CZ", "win1250"},
		// Maltese is NOT covered by any page here (verified: neither CP852
		// nor Windows-1250 has ċ/ġ/ħ) — stays on the pass-through pending a
		// follow-up card, same as Turkish.
		{"maltese euro store is not switched", "EUR", "mt-MT", "utf8"},
		// Windows-1250 has no '£' (independent review finding, ut-docs#1733)
		// — a GBP Croatian/Slovenian/Slovak store must NOT be switched to it,
		// or its currency symbol trades one mojibake for a folded '?', which
		// is worse than the utf8 pass-through it started from. GBP still
		// switches on win1257/win1253, which do carry '£'.
		{"GBP croatian store keeps the pass-through (win1250 has no £)", "GBP", "hr-HR", "utf8"},
		{"GBP slovenian store keeps the pass-through (win1250 has no £)", "GBP", "sl-SI", "utf8"},
		{"GBP slovak store keeps the pass-through (win1250 has no £)", "GBP", "sk-SK", "utf8"},
		{"GBP estonian store gets win1257 (has £)", "GBP", "et-EE", "win1257"},
		{"GBP greek store gets win1253 (has £)", "GBP", "el-GR", "win1253"},
		{"unknown language falls back to today's behaviour", "EUR", "zz-ZZ", "utf8"},
		{"blank config falls back to today's behaviour", "", "", "utf8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultCharset(tc.currency, tc.locale); got != tc.want {
				t.Fatalf("DefaultCharset(%q, %q) = %q, want %q", tc.currency, tc.locale, got, tc.want)
			}
		})
	}
}

// The whole point of ut-docs#1728: the bytes a receipt actually puts on the
// wire for a store that never touched the Characters setting. ut-docs#1243's
// tests all passed a charset in explicitly, so they proved the OPTION worked
// while the DEFAULT stayed broken — this asserts the default path instead.
func TestRender_DefaultCharsetForEuroStore_EmitsCP858EuroAndSelectsCodePage(t *testing.T) {
	doc := Doc{
		StoreName: "Test Cafe",
		Totals:    []KV{{Label: "TOTAL", Amount: "€2.50", Strong: true}},
		Charset:   DefaultCharset("EUR", "de-DE"),
	}
	out := Render(doc)

	euro, ok := charmap.CodePage858.EncodeRune('€')
	if !ok {
		t.Fatal("CP858 must be able to encode the euro sign")
	}
	if !bytes.Contains(out, []byte{euro}) {
		t.Fatalf("expected the single-byte CP858 euro (%#x) in the stream, got %q", euro, out)
	}
	if bytes.Contains(out, []byte("€")) {
		t.Fatalf("expected NO raw UTF-8 euro bytes (E2 82 AC) in the stream, got %q", out)
	}
	if !bytes.Contains(out, []byte{0x1b, 0x74, 0x13}) {
		t.Fatalf("expected the ESC t 19 code-page selection command in the stream, got %q", out)
	}
}

// ut-docs#1733: the Windows-125x pages must round-trip the same way CP858
// does — the currency symbol single-byte-encoded, and the ESC t selection
// command for that specific page in the stream.
func TestRender_DefaultCharsetForNewEurozoneLocales_EmitsCorrectPageAndCommand(t *testing.T) {
	cases := []struct {
		name           string
		locale         string
		wantCharmap    *charmap.Charmap
		wantSelectByte byte
	}{
		{"croatian (win1250)", "hr-HR", charmap.Windows1250, 45},
		{"estonian (win1257)", "et-EE", charmap.Windows1257, 51},
		{"greek (win1253)", "el-GR", charmap.Windows1253, 47},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			charset := DefaultCharset("EUR", tc.locale)
			doc := Doc{
				StoreName: "Test Cafe",
				Totals:    []KV{{Label: "TOTAL", Amount: "€2.50", Strong: true}},
				Charset:   charset,
			}
			out := Render(doc)

			euro, ok := tc.wantCharmap.EncodeRune('€')
			if !ok {
				t.Fatalf("%s must be able to encode the euro sign", charset)
			}
			if !bytes.Contains(out, []byte{euro}) {
				t.Fatalf("expected the single-byte %s euro (%#x) in the stream, got %q", charset, euro, out)
			}
			if bytes.Contains(out, []byte("€")) {
				t.Fatalf("expected NO raw UTF-8 euro bytes (E2 82 AC) in the stream, got %q", out)
			}
			if !bytes.Contains(out, []byte{0x1b, 0x74, tc.wantSelectByte}) {
				t.Fatalf("expected the ESC t %d code-page selection command in the stream, got %q", tc.wantSelectByte, out)
			}
		})
	}
}

// The counterpart: a Turkish store must keep sending UTF-8 rather than being
// silently downgraded to '?' by a blanket CP858 default.
func TestRender_DefaultCharsetForTurkishStore_KeepsPassThrough(t *testing.T) {
	doc := Doc{
		StoreName: "Sipariş",
		Charset:   DefaultCharset("TRY", "tr-TR"),
	}
	out := Render(doc)
	if !bytes.Contains(out, []byte("Sipariş")) {
		t.Fatalf("expected the Turkish store name to pass through unchanged, got %q", out)
	}
	if bytes.Contains(out, []byte{0x1b, 0x74}) {
		t.Fatalf("expected no code-page selection for the pass-through default, got %q", out)
	}
}

// ut-docs#1728 (independent review, finding 1). Making cp858 a DEFAULT means
// shops that were printing correctly over UTF-8 get transcoded whether they
// asked to or not. Before the fold, everything CP858 lacks became '?' — and
// what it lacks is not exotic: it is the typography Word and Excel put into
// every imported catalog and footer line. Trading a broken currency symbol
// for a receipt full of '?' would not have been a fix.
func TestEncodeText_CP858_FoldsEverydayTypographyRatherThanGuttingIt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Vielen Dank – bis bald", "Vielen Dank - bis bald"},
		{"Chef’s special", "Chef's special"},
		{"“Tageskarte”", `"Tageskarte"`},
		{"„Tagesessen“", `"Tagesessen"`},
		{"Suppe … Salat", "Suppe ... Salat"},
		{"• Kaffee", "* Kaffee"},
		{"Bœuf Bourguignon", "Boeuf Bourguignon"},
		{"Œufs", "OEufs"},                // uppercase Œ folds to OE, not Oe
		{"Menü N° 3", "Men\x81 N\xf8 3"}, // ü and ° are both native CP858
	}
	for _, tc := range cases {
		got := string(encodeText(tc.in, "cp858"))
		if got != tc.want {
			t.Errorf("encodeText(%q, cp858) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.Contains(got, "?") {
			t.Errorf("encodeText(%q, cp858) still degrades to '?': %q", tc.in, got)
		}
	}
}

// '?' must remain reachable — it is the visible last resort for a rune with
// no fold at all, and losing it would silently drop characters instead.
func TestEncodeText_CP858_StillFallsBackToQuestionMark(t *testing.T) {
	if got := string(encodeText("日本", "cp858")); got != "??" {
		t.Fatalf("encodeText(日本, cp858) = %q, want %q", got, "??")
	}
}

// Encodable backs kitchenTicketText's printer-safe-fallback decision
// (internal/pages/kitchen_print.go, ut-docs#261/#1733) — it must say a
// restricted charset CAN render a script it natively supports (Greek under
// win1253) and CANNOT render one it doesn't (Japanese under any of them, or
// Arabic under a Latin-only page), matching what encodeText actually does
// rather than a coarse "any non-ASCII is unsafe" guess.
func TestEncodable(t *testing.T) {
	cases := []struct {
		s, charset string
		want       bool
	}{
		{"Käsespätzle", "cp858", true}, // German — natively in CP858
		{"Σουβλάκι", "win1253", true},  // Greek — natively in Windows-1253
		{"日本", "cp858", false},         // unrenderable under any single-byte page
		{"日本", "win1253", false},
		{"مرحبا", "win1250", false}, // Arabic — not in Windows-1250
		{"Café", "utf8", true},      // utf8 passes everything through
	}
	for _, tc := range cases {
		if got := Encodable(tc.s, tc.charset); got != tc.want {
			t.Errorf("Encodable(%q, %q) = %v, want %v", tc.s, tc.charset, got, tc.want)
		}
	}
}

// Independent review finding (ut-docs#1733): a literal '?' already present
// in s is an ordinary character, not a degraded rune — Encodable must not
// confuse the two by scanning encodeText's output for '?' (that first
// implementation would have said false here, since the rendered bytes
// contain a '?' either way).
func TestEncodable_LiteralQuestionMarkInSourceIsNotADegradedRune(t *testing.T) {
	cases := []struct{ s, charset string }{
		{"Menu?", "ascii"},       // plain ASCII plus a literal '?'
		{"Café?", "cp858"},       // é native to CP858, plus a literal '?'
		{"Čevapi?", "win1250"},   // Č native to Windows-1250, plus a literal '?'
		{"Šalica?", "win1257"},   // Š native to Windows-1257, plus a literal '?'
		{"Σουβλάκι?", "win1253"}, // Greek natively, plus a literal '?'
	}
	for _, tc := range cases {
		if !Encodable(tc.s, tc.charset) {
			t.Errorf("Encodable(%q, %s) = false, want true — every rune here (including the literal '?') is representable", tc.s, tc.charset)
		}
	}
}

// Independent review finding (ut-docs#1733): Windows-1250 has no '£' at all,
// unlike Windows-1257/1253 which both carry it (same as CP858). This pins
// the actual gap DefaultCharset's currency==EUR-only gate on win1250 exists
// to route around — if this test ever starts failing because win1250 grew a
// '£', the gate in DefaultCharset should be relaxed to match.
func TestPoundSign_MissingFromWin1250OnlyAmongTheNewPages(t *testing.T) {
	cases := []struct {
		charset string
		want    bool
	}{
		{"win1250", false},
		{"win1257", true},
		{"win1253", true},
	}
	for _, tc := range cases {
		if got := Encodable("£2.50", tc.charset); got != tc.want {
			t.Errorf("Encodable(£2.50, %q) = %v, want %v", tc.charset, got, tc.want)
		}
	}
}

// ut-docs#1733: the same fold-before-'?' guarantee, generalized to the three
// new pages. Unlike CP858, these already natively encode dashes/quotes/
// bullet/ellipsis, so only the œ/Œ decomposition step is actually exercised
// here — still worth asserting, since it is shared code (foldToCharmap) and
// a regression there would silently reintroduce '?' for everyday content.
func TestEncodeText_NewCharmaps_FoldAndFallBackToQuestionMark(t *testing.T) {
	for _, charset := range []string{"win1250", "win1257", "win1253"} {
		t.Run(charset, func(t *testing.T) {
			if got := string(encodeText("Bœuf", charset)); got != "Boeuf" {
				t.Errorf("encodeText(Bœuf, %s) = %q, want %q", charset, got, "Boeuf")
			}
			if got := string(encodeText("日本", charset)); got != "??" {
				t.Errorf("encodeText(日本, %s) = %q, want %q", charset, got, "??")
			}
		})
	}
}

// The comment on the new *Languages maps asserts each enumerated language's
// everyday shop text survives its page without degrading to '?' — the same
// check TestCP858Languages_... does for the original set, and for the same
// reason: nothing else would catch a wrong or incomplete map entry.
func TestNewEurozoneLanguages_EverydayTextSurvivesWithoutDegrading(t *testing.T) {
	cases := []struct {
		lang, charset, sample string
	}{
		{"hr", "win1250", "Ćevapi, Čorba, Đevđir, Šalica, Žlica"},
		{"sl", "win1250", "Čevapčiči, Šunka, Žlica"},
		{"sk", "win1250", "Ľudový, Kôň, Guláš, Vareník"},
		{"et", "win1257", "Šašlõkk, Žgood"},
		{"lv", "win1257", "Rasols, Šņabis, Zaķis, Ūdens, Ķīselis, Ņipris, Ēdiens, Āboliņš"},
		{"lt", "win1257", "Šaltibarščiai, Žąsis, Ėriena, Kūčiukai, Įdaras, Ąžuolas"},
		{"el", "win1253", "Σουβλάκι, Τζατζίκι, Μουσακάς"},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			got := string(encodeText(tc.sample, tc.charset))
			if strings.Contains(got, "?") {
				t.Errorf("%s: %q degrades to %q on %s", tc.lang, tc.sample, got, tc.charset)
			}
		})
	}
}

// The comment on cp858Languages asserts that each enumerated language's
// alphabet survives the cp858 path. The review's finding 6 was that nothing
// checked it — and that it was already wrong for French ('œ' is not in
// CP858). This is that check.
func TestCP858Languages_EverydayTextSurvivesWithoutDegrading(t *testing.T) {
	samples := map[string]string{
		"en": "Chef's Special — Today’s Soup",
		"de": "Käsespätzle, Weißbier, Grüße",
		"fr": "Bœuf, Crème brûlée, Œufs à la coque",
		"es": "Café con leche, Jamón, Niño",
		"it": "Caffè, Panino, Perché",
		"nl": "Broodje, Kaas, ĳsje",
		"pt": "Pão, Açúcar, Refeição",
		"da": "Smørrebrød, Øl, Kaffe",
		"sv": "Räksmörgås, Köttbullar",
		"no": "Kjøttkaker, Rømme",
		"nb": "Smørbrød, Løk",
		"nn": "Fløyte, Bær",
		"fi": "Pullakahvi, Säilyke",
		"is": "Skyr, Þorskur, Ærslabelgur",
		"ga": "Béile, Sláinte",
		"ca": "Cafè, Entrepà, ŀl",
		"gl": "Café, Empanada, Ñoño",
		"eu": "Kafea, Txistorra",
		"lb": "Kaffi, Gebäck",
	}
	for lang, sample := range samples {
		if !cp858Language(lang) {
			t.Errorf("%s is expected to be enumerated as CP858-safe", lang)
			continue
		}
		got := string(encodeText(sample, "cp858"))
		if strings.Contains(got, "?") {
			t.Errorf("%s: %q degrades to %q — either fix the fold or drop %s from cp858Languages",
				lang, sample, got, lang)
		}
	}
}
