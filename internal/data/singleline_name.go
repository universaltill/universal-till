package data

import "github.com/universaltill/universal-till/catimport/catalogrules"

// ErrNameInvalidCharacters is catalogrules.ErrNameInvalidCharacters: the
// same value, so errors.Is matches on either side.
var ErrNameInvalidCharacters = catalogrules.ErrNameInvalidCharacters

// CheckSingleLineName delegates to catalogrules.CheckSingleLineName, the one
// single-line name rule the till and ut-cloud share (ut-docs#5860).
func CheckSingleLineName(s string) (string, error) { return catalogrules.CheckSingleLineName(s) }
