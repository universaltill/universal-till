// Package phonenumber normalises phone numbers to E.164 ("+<country
// code><national number>") for caller-ID lookup (ADR-0131 §3,
// ut-docs#3200). It is pure: no SQL, no I/O, no carrier or number-length
// metadata — only each region's calling code and national trunk prefix
// (table.go). A number it cannot normalise is matched by its trailing
// digits instead (TrailingKey).
package phonenumber

import "strings"

// MinDigits / MaxDigits bound the digits after '+' in a normalised number.
// E.164 caps a number at 15 digits; 8 is the shortest real-world full
// international number this lookup accepts (shorter strings are almost
// always extensions or service codes, which would match too broadly).
const (
	MinDigits = 8
	MaxDigits = 15
)

// trailingKeyLen is how many trailing digits a fallback match compares;
// trailingKeyMin is the fewest digits that still make a usable key.
const (
	trailingKeyLen = 9
	trailingKeyMin = 6
)

// Normalise returns raw as E.164 ("+" and 8–15 digits) and true, or "" and
// false when it cannot. raw may contain only digits, '+', '*', '#', space,
// '-', '(', ')' and '.' — anything else (letters, '/', non-ASCII digits)
// fails. '+' is allowed once, before the first digit.
//
//   - A leading '+' means the number is already international.
//   - A leading "00" is the international prefix and becomes '+'.
//   - Otherwise the number is national: region (an ISO 3166-1 alpha-2
//     code, any case) must be in the table; its trunk prefix is stripped
//     once if present (a region with none, e.g. Italy, keeps its leading
//     0) and its calling code is prepended.
//   - In an international number, a literal "(0)" (the trunk prefix
//     written in brackets, "+44 (0)20 …") is dropped.
func Normalise(raw, region string) (string, bool) {
	// "+44 (0)20 …" / "0049 (0)30 …": the bracketed national trunk 0
	// written after the country code is not part of the number.
	if t := strings.TrimSpace(raw); strings.HasPrefix(t, "+") || strings.HasPrefix(t, "00") {
		raw = strings.Replace(raw, "(0)", "", 1)
	}
	var b strings.Builder
	plus := false
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+':
			if plus || b.Len() > 0 {
				return "", false
			}
			plus = true
		case r == '*' || r == '#' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", false
		}
	}
	d := b.String()
	var intl string
	switch {
	case plus:
		intl = d
	case strings.HasPrefix(d, "00"):
		intl = d[2:]
	default:
		e, ok := Lookup(region)
		if !ok || d == "" {
			return "", false
		}
		if e.Trunk != "" {
			d = strings.TrimPrefix(d, e.Trunk)
		}
		intl = e.CallingCode + d
	}
	// No ITU country code starts with 0.
	if len(intl) < MinDigits || len(intl) > MaxDigits || intl[0] == '0' {
		return "", false
	}
	return "+" + intl, true
}

// Digits returns the ASCII digits of raw, in order, dropping everything
// else.
func Digits(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TrailingKey is the fallback match key for raw: its last 9 digits, all
// of its digits when it has 6–9, or "" (no fallback) when it has fewer
// than 6 — too few to tell customers apart.
func TrailingKey(raw string) string {
	d := Digits(raw)
	if len(d) < trailingKeyMin {
		return ""
	}
	if len(d) > trailingKeyLen {
		return d[len(d)-trailingKeyLen:]
	}
	return d
}

// Lookup returns the table entry for an ISO 3166-1 alpha-2 code, any case.
func Lookup(iso string) (Entry, bool) {
	e, ok := byISO[strings.ToUpper(strings.TrimSpace(iso))]
	return e, ok
}
