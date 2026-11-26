package pages

import (
	"strings"
)

const (
	externalCardErrAuthCode = "card_reader.external.err_auth_code"
	externalCardErrLast4    = "card_reader.external.err_last4"
	externalCardInvalidCode = "card_reader.external.invalid"
)

// parseExternalCardFields validates the cashier-typed fields of a card taken
// on the shop's own terminal (ADR-0205 §7, ADR-0127): auth code 0-12 ASCII
// [A-Za-z0-9]; last4 "" or exactly 4 ASCII digits. Returns the i18n key of the
// first problem. Values are stored as typed (after TrimSpace); never more of
// the card than its last four digits.
func parseExternalCardFields(auth, last4 string) (authCode, maskedPAN, errKey string) {
	auth, last4 = strings.TrimSpace(auth), strings.TrimSpace(last4)
	if len(auth) > 12 {
		return "", "", externalCardErrAuthCode
	}
	for i := 0; i < len(auth); i++ {
		c := auth[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "", "", externalCardErrAuthCode
		}
	}
	if last4 != "" {
		if len(last4) != 4 {
			return "", "", externalCardErrLast4
		}
		for i := 0; i < 4; i++ {
			if last4[i] < '0' || last4[i] > '9' {
				return "", "", externalCardErrLast4
			}
		}
		maskedPAN = "•••• " + last4
	}
	return auth, maskedPAN, ""
}
