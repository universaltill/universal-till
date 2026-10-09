// Package diskspace reports how much room is left on the disk that holds the
// till's data directory, and whether that is below the low-disk floor
// (ut-docs#3121). Housekeeping prunes to its minimums below the floor, and
// the status bar shows a chip to a manager; nothing here ever blocks a sale.
package diskspace

import "errors"

// MinFreeBytes is the absolute floor: 500 MB.
const MinFreeBytes uint64 = 500 << 20

// ErrUnsupported is returned by Probe on a platform with no free-space call;
// callers treat it as "unknown" and never prune or warn on it.
var ErrUnsupported = errors.New("diskspace: no free-space probe on this platform")

// Usage is one probe of the disk holding a path, in bytes. Free is what an
// unprivileged process may still write (statfs f_bavail, Windows'
// FreeBytesAvailable), not the root-reserved blocks a till can't use.
type Usage struct {
	Free  uint64
	Total uint64
}

// Floor is the free space below which the disk counts as low: 500 MB or 10%
// of the disk, whichever is larger.
func Floor(total uint64) uint64 {
	return max(MinFreeBytes, total/10)
}

// Low reports whether u is below the floor.
func Low(u Usage) bool { return u.Free < Floor(u.Total) }

// Probe measures the disk that holds path (a directory that exists).
func Probe(path string) (Usage, error) { return probe(path) }
