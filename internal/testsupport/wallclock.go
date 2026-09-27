package testsupport

import (
	"strings"
	"time"
	"unsafe"
)

// WallStepped returns t with its wall clock shifted by d (whole seconds)
// but its monotonic reading untouched — exactly what time.Now() returns
// across an NTP step, or a Pi without an RTC setting its clock after boot
// (ut-docs#2853; shared with internal/pages for ut-docs#2915). Implemented
// via unsafe over time.Time's runtime layout (wall uint64; ext int64; loc
// *Location): with the hasMonotonic bit (1<<63) set, wall's bits 30..62
// hold whole seconds and ext holds the monotonic reading, so nudging the
// seconds field by whole seconds moves neither the nsec bits nor ext.
// Panics if t carries no monotonic reading, or the layout ever changes
// underneath this (see TestWallStepped).
func WallStepped(t time.Time, d time.Duration) time.Time {
	if d%time.Second != 0 {
		panic("WallStepped: d must be a whole number of seconds")
	}
	if !strings.Contains(t.String(), "m=") {
		panic("WallStepped: t carries no monotonic reading")
	}
	type timeLayout struct {
		wall uint64
		ext  int64
		loc  *time.Location
	}
	cp := t
	tl := (*timeLayout)(unsafe.Pointer(&cp))
	secs := uint64(d.Abs() / time.Second)
	if d >= 0 {
		tl.wall += secs << 30
	} else {
		tl.wall -= secs << 30
	}
	return cp
}
