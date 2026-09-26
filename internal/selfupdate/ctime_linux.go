package selfupdate

import (
	"os"
	"syscall"
	"time"
)

// fileChangedAt is the later of a file's mtime and ctime. dpkg keeps the
// package's own (old) mtime on the files it installs, so only ctime shows
// that a .deb replaced the binary after an in-app update (ut-docs#2759).
func fileChangedAt(info os.FileInfo) time.Time {
	t := info.ModTime()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if c := time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)); c.After(t) {
			t = c
		}
	}
	return t
}
