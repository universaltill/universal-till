// Package restorenum holds the one definition of what counts as a believable
// receipt or Z number for the backup-restore number jump (ut-docs#6906,
// ut-docs#6924). It is a leaf package so both internal/data and internal/db
// can share it (db cannot import data).
package restorenum

import "strconv"

// Max bounds a counter the restore jump believes: a larger receipt or Z
// number is corrupt, not a counter, and is ignored.
const Max int64 = 1_000_000_000_000_000

// ParseFloor reads a stored floor; ok is false for anything that is not a
// base-10 integer in [0, Max] (such a value counts as absent).
func ParseFloor(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 || v > Max {
		return 0, false
	}
	return v, true
}
