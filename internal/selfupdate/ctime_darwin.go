package selfupdate

import (
	"os"
	"syscall"
	"time"
)

// fileChangedAt is the later of a file's mtime and ctime (see ctime_linux.go).
func fileChangedAt(info os.FileInfo) time.Time {
	t := info.ModTime()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if c := time.Unix(st.Ctimespec.Sec, st.Ctimespec.Nsec); c.After(t) {
			t = c
		}
	}
	return t
}
