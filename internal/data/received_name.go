package data

import "strings"

// checkReceivedName applies CheckSingleLineName to a name a directive from my.
// or the catalogue pull hands the till (ut-docs#5871). stored is the row's
// current name, or nil when the row (or option/value id) is new. A name that
// equals the stored one once both are trimmed is passed through unchecked
// (legacy tolerance: a row stored before the rule must stay editable when
// my. resends its name with another field changed; #5860 R8). Returns the
// trimmed name. Blank is the caller's own check and runs first.
func checkReceivedName(stored *string, received string) (string, error) {
	n := strings.TrimSpace(received)
	if stored != nil && n == strings.TrimSpace(*stored) {
		return n, nil
	}
	return CheckSingleLineName(n)
}
