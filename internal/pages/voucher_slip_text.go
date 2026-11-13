package pages

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/httpx"
)

// The shop's own text on the gift voucher slips and the slips' date line
// (ut-docs#6179).

const (
	maxVoucherSlipTextLines = 4
	maxVoucherSlipTextRunes = 160 // runes over all lines, line feeds not counted
)

var errVoucherSlipTextTooLong = errors.New("voucher slip text over the line or character limit")

// cleanVoucherSlipText is steps 1-4 of the write-side rule: valid UTF-8, LF
// line breaks, no control runes, each line trimmed, edge blank lines dropped.
func cleanVoucherSlipText(raw string) []string {
	raw = strings.ToValidUTF8(raw, "")
	raw = strings.NewReplacer("\r\n", "\n", "\r", "\n", " ", "\n", " ", "\n").Replace(raw)
	raw = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r): // C0, DEL, C1 (mirrors print.paperControl)
			return -1
		case r >= 0x202A && r <= 0x202E: // bidi embeddings/overrides
			return -1
		}
		return r
	}, raw)
	lines := strings.Split(raw, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// normalizeVoucherSlipText is the one write-side rule for
// receipt.voucher_slip_text (designer save, the main till applying a
// forwarded save, the raw settings editor). It refuses over-long text and
// never truncates. Idempotent.
func normalizeVoucherSlipText(raw string) (string, error) {
	lines := cleanVoucherSlipText(raw)
	runes := 0
	for _, l := range lines {
		runes += utf8.RuneCountInString(l)
	}
	if len(lines) > maxVoucherSlipTextLines || runes > maxVoucherSlipTextRunes {
		return "", errVoucherSlipTextTooLong
	}
	return strings.Join(lines, "\n"), nil
}

// lenientVoucherSlipText is the same normalisation without the limit, so the
// designer's live preview never errors; only Save refuses.
func lenientVoucherSlipText(raw string) string {
	return strings.Join(cleanVoucherSlipText(raw), "\n")
}

// voucherSlipLines splits the stored value for printing; "" gives nil.
func voucherSlipLines(stored string) []string {
	if stored == "" {
		return nil
	}
	return strings.Split(stored, "\n")
}

// voucherSlipTextTooLongMsg is the localized refusal shown by the designer
// and the raw settings editor.
func voucherSlipTextTooLongMsg(locale string) string {
	return fmt.Sprintf(httpx.T(locale, "designer.receipt.voucher_slip_text_too_long"), maxVoucherSlipTextLines, maxVoucherSlipTextRunes)
}

// slipDateLine is "Issued <date>" / "Topped up <date>" for a stored
// timestamp, in the till's local time and the shop's date order with Latin
// digits (like "Reprinted"); "" when the timestamp does not parse — never
// today, never a zero date.
func slipDateLine(locale, key, stored string) string {
	return slipDateLineIn(locale, key, stored, time.Local)
}

func slipDateLineIn(locale, key, stored string, tz *time.Location) string {
	t, ok := httpx.ParseStoredTime(stored)
	if !ok || t.IsZero() {
		return ""
	}
	return fmt.Sprintf(httpx.T(locale, key), httpx.FormatDateLatin(t.In(tz), locale))
}
