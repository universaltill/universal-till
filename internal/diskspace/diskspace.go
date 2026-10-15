// Package diskspace reports how much room is left on the disk that holds the
// till's data directory, and whether that is below the low-disk floor
// (ut-docs#3121). Housekeeping prunes to its minimums below the floor, and
// the status bar shows a chip to a manager; nothing here ever blocks a sale.
package diskspace

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
)

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

// DocsShotsRoomyEnv, when set to a true boolean ("1", "true"), makes Probe
// report a fixed roomy disk instead of measuring the host. It is harness-only:
// set ONLY by the docs-shots Playwright webServer env
// (e2e/playwright.docs.config.ts), never by Go, run-till*.sh, production or
// the real e2e suite, so the host's free space can never reach a manual
// screenshot as a "Storage almost full" chip or a disk.low Problem
// (ut-docs#4280).
const DocsShotsRoomyEnv = "UT_DOCS_SHOTS_DISK_ROOMY"

// docsShotsRoomy is the pinned disk Probe reports under DocsShotsRoomyEnv:
// 32 GiB free of 64 GiB, comfortably above the floor.
var docsShotsRoomy = Usage{Free: 32 << 30, Total: 64 << 30}

var (
	roomyOnce sync.Once
	roomyOn   bool
)

// parseRoomy resolves the pin from a raw env value. Anything that is not a
// true boolean (empty, a typo, "yes") is false, so a typo never hides a real
// low disk. Pure, so it can be unit-tested without racing the sync.Once cache in Probe.
func parseRoomy(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

// Probe measures the disk that holds path (a directory that exists). Under
// UT_DOCS_SHOTS_DISK_ROOMY (docs-shots harness only) it returns a pinned
// roomy disk without touching the filesystem, so that till's housekeeping
// also skips the low-disk prune and caps backups against the pinned free
// space. The env is read once, on first use.
func Probe(path string) (Usage, error) {
	roomyOnce.Do(func() { roomyOn = parseRoomy(os.Getenv(DocsShotsRoomyEnv)) })
	return probeWith(roomyOn, path)
}

// probeWith is Probe with the pin passed in, so tests can exercise both
// branches without the sync.Once cache.
func probeWith(roomy bool, path string) (Usage, error) {
	if roomy {
		return docsShotsRoomy, nil
	}
	return probe(path)
}
