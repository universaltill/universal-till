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
		// possibly-fine receipt with one full of '?'.
		{"turkish store is not switched", "TRY", "tr-TR", "utf8"},
		{"turkish store on euro is still not switched", "EUR", "tr-TR", "utf8"},
		// Greece is EUR but Greek is not in CP858 at all.
		{"greek euro store is not switched", "EUR", "el-GR", "utf8"},
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
