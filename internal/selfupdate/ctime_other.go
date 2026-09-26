//go:build !linux && !darwin

package selfupdate

import (
	"os"
	"time"
)

// fileChangedAt falls back to mtime where ctime is not exposed (see
// ctime_linux.go).
func fileChangedAt(info os.FileInfo) time.Time { return info.ModTime() }
