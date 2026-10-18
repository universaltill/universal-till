package data

import (
	"database/sql/driver"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"
)

// searchFoldSQLFunc is SearchFold registered as a deterministic SQLite
// scalar function, so a search can match folded names in SQL and keep its
// ORDER BY / LIMIT / OFFSET there: `instr(ut_search_fold(col), ?) > 0`,
// with the argument already folded by SearchFold in Go.
const searchFoldSQLFunc = "ut_search_fold"

// The driver adds registered functions to every connection it opens after
// this runs; package init runs before any caller of this package opens one.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction(searchFoldSQLFunc, 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			switch v := args[0].(type) {
			case string:
				return SearchFold(v), nil
			case []byte:
				return SearchFold(string(v)), nil
			case nil:
				return nil, nil
			default:
				return v, nil
			}
		})
}

// SearchFold is the one key the till's text searches match on (ut-docs#4650:
// gift vouchers, customers, the item search): trimmed, NFC-normalised (so an
// NFD paste meets stored NFC text), and case-folded rune by rune as
// lower(upper(r)) plus ß/ẞ→ss. That fold also sends final ς to σ and the
// Turkish I/ı/İ/i all to i. It is locale-independent on purpose -- every
// till treats those four as one letter in search, which only widens
// matches -- so no locale reaches the repository layer. Accents are kept:
// "Muller" does not find "Müller". Not for exact-match keys (FoldItemName).
// It runs on every row a search scans (searchFoldSQLFunc), so ASCII text
// and already-NFC text take fast paths.
func SearchFold(s string) string {
	s = strings.TrimSpace(s)
	if isASCII(s) {
		return strings.ToLower(s)
	}
	if !norm.NFC.IsNormalString(s) {
		s = norm.NFC.String(s)
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	var prev rune
	for _, r := range s {
		switch {
		case r == 'ß' || r == 'ẞ':
			b.WriteString("ss")
			r = 's'
		case r == '\u0307' && prev == 'i':
			// "i + combining dot above" (İ typed decomposed; NFC has no
			// precomposed lower form) folds to i, like İ itself.
		default:
			r = unicode.ToLower(unicode.ToUpper(r))
			b.WriteRune(r)
		}
		prev = r
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
