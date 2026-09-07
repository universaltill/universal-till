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
func DefaultCharset(currency, locale string) string {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "EUR", "GBP":
	default:
		return "utf8"
	}
	if !cp858Language(locale) {
		return "utf8"
	}
	return "cp858"
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
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	return cp858Languages[l]
}
