package print

import "strings"

// DefaultCharset resolves the code page a till should print in when the
// operator has never chosen one, from the store's configured currency and
// locale.
//
// ut-docs#1728. ut-docs#1243 added a working CP858 transcode (€ → 0xD5,
// £ → 0x9C) plus the ESC t 19 selection command, but wired it up as an
// opt-in dropdown option and left the resolved default on "utf8" — a raw
// pass-through that sends "€" as its three UTF-8 bytes (E2 82 AC) and never
// selects a code page at all. A single-byte-code-page thermal printer
// renders those three bytes as three CP437/CP850 glyphs ("âÎ¬2.50"), which
// is exactly the mojibake #1243 was raised for and which was still on real
// receipts afterwards, because fixing the capability changed nothing for a
// till nobody had reconfigured by hand.
//
// The default is deliberately NARROW rather than "cp858 everywhere":
//
//   - Only EUR and GBP stores switch. Those are the two currencies whose
//     symbol is unprintable without a code page and IS in CP858. A USD
//     store's "$" is plain ASCII and already prints correctly, so there is
//     no defect to fix and no reason to change what it sends.
//   - Only languages CP858 actually covers. CP858 is CP850 with the '€'
//     occupying what was 'ı', so it has neither 'ı' nor 'ş' — a Turkish
//     receipt would come out full of '?' where the utf8 pass-through may
//     well have printed it correctly. Greek and Cyrillic are not in CP858
//     at all, and Greece and Cyprus are EUR: gating on currency alone
//     would regress a Greek shop's own name into '?' to fix its currency
//     symbol. Hence the language gate below, which "no row" answers with
//     utf8 — today's behaviour — rather than a guess.
//
// Anything this returns is only ever a DEFAULT. An operator who picks a
// charset explicitly keeps it (see settings.AdoptDefaultPrinterCharset).
//
// ut-docs#1733. CP858 (above) only covers Western European alphabets — eight
// eurozone countries (Greek, Maltese, Croatian, Slovenian, Slovak, Estonian,
// Latvian, Lithuanian) still fell through to the utf8 pass-through and its
// mojibake. The obvious next DOS-era pages (CP852 Central European, CP869
// Greek) don't fix it: CP869 isn't in golang.org/x/text/encoding/charmap at
// all, and CP852 has no '€' slot whatsoever (verified against
// charmap.CodePage852.EncodeRune) — it predates the Euro entirely, so
// switching to it would fix the alphabet and leave the currency symbol
// exactly as broken as utf8 leaves it today. The printer's own ESC/POS
// code-table (`ESC t n`, Epson's published reference) also offers the
// *Windows* 125x pages, which got a later Euro-sign update the DOS pages
// never did — those are both selectable on the same hardware and present in
// charmap, so this resolves to those instead:
//
//   - Windows-1250 (Central European) covers Croatian/Slovenian/Slovak.
//   - Windows-1257 (Baltic Rim) covers Estonian/Latvian/Lithuanian — CP852
//     cannot: it lacks the Baltic long-vowel letters (ā/ē/ī/ū etc.).
//   - Windows-1253 (Greek) covers the Greek alphabet.
//
// Maltese (ċ/ġ/ħ) is NOT covered by any of the above (verified) and stays on
// the utf8 pass-through — split into a follow-up card rather than guessed.
//
// Windows-1250 has NO '£' (verified against charmap.Windows1250.EncodeRune —
// unlike Windows-1257/1253, which both carry it at 0xA3, same as CP858's
// 0x9C). A GBP store on hr/sl/sk/... would trade a working '£' for a folded
// '?', which is worse than the utf8 pass-through it started from — so unlike
// cp858/win1257/win1253, win1250 only ever activates for EUR, never GBP
// (independent review finding, ut-docs#1733).
func DefaultCharset(currency, locale string) string {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	switch cur {
	case "EUR", "GBP":
	default:
		return "utf8"
	}
	switch {
	case cp858Language(locale):
		return "cp858"
	case windows1250Language(locale) && cur == "EUR":
		return "win1250"
	case windows1257Language(locale):
		return "win1257"
	case windows1253Language(locale):
		return "win1253"
	default:
		return "utf8"
	}
}

// cp858Language reports whether CP858 can represent the everyday text of a
// locale's language — its shop names, item names and UI strings, not just
// its currency symbol. Keyed on the primary subtag only ("de-AT" and
// "de-DE" answer the same), so a region this list never enumerated still
// resolves correctly.
//
// The set is Western European by construction: CP858 is CP850 (Latin-1
// plus box drawing) with one slot traded for '€'. Adding a language here
// means asserting its alphabet survives charmap.CodePage858.EncodeRune —
// anything it can't encode degrades to '?' on a printed receipt, so absence
// is the safe answer and the list stays short on purpose.
var cp858Languages = map[string]bool{
	"en": true, "de": true, "fr": true, "es": true, "it": true,
	"nl": true, "pt": true, "da": true, "sv": true, "no": true,
	"nb": true, "nn": true, "fi": true, "is": true, "ga": true,
	"ca": true, "gl": true, "eu": true, "lb": true,
}

func cp858Language(locale string) bool {
	return cp858Languages[primaryLanguageSubtag(locale)]
}

// windows1250Languages covers Windows-1250's Central European repertoire —
// verified against charmap.Windows1250.EncodeRune for each language's
// diacritics (Croatian č/ć/đ/š/ž, Slovenian č/š/ž, Slovak ľ/ĺ/ŕ/ô) plus '€'.
var windows1250Languages = map[string]bool{
	"hr": true, "sl": true, "sk": true,
}

func windows1250Language(locale string) bool {
	return windows1250Languages[primaryLanguageSubtag(locale)]
}

// windows1257Languages covers Windows-1257's Baltic Rim repertoire — verified
// against charmap.Windows1257.EncodeRune for each language's diacritics
// (Estonian š/ž, Latvian ā/č/ē/ģ/ī/ķ/ļ/ņ/š/ū/ž, Lithuanian ą/č/ę/ė/į/š/ų/ū/ž)
// plus '€'. CP852 cannot substitute here: it lacks the Baltic long-vowel
// letters entirely (verified).
var windows1257Languages = map[string]bool{
	"et": true, "lv": true, "lt": true,
}

func windows1257Language(locale string) bool {
	return windows1257Languages[primaryLanguageSubtag(locale)]
}

// windows1253Languages covers Windows-1253's Greek repertoire — verified
// against charmap.Windows1253.EncodeRune for the full Greek alphabet plus
// '€'.
var windows1253Languages = map[string]bool{
	"el": true,
}

func windows1253Language(locale string) bool {
	return windows1253Languages[primaryLanguageSubtag(locale)]
}

// primaryLanguageSubtag normalizes a locale to its primary language subtag
// ("de-AT" and "de_DE" both answer "de"), shared by every *Language check
// above so a region this list never enumerated still resolves correctly.
func primaryLanguageSubtag(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	return l
}
