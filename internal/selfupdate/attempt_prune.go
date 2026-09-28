package selfupdate

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PruneStaleAttempts removes Windows update attempt-* folders (the downloaded
// installer and helper script) last modified before cutoff — the
// device-housekeeping sweep (ut-docs#3092). applyWindowsInstaller clears
// them too, but only when the next update starts. updater.log and anything
// else in the folder stay, and nothing is touched while an update is being
// handed to the installer. On a platform that never wrote the folder this
// is a no-op. Returns the folders removed and the bytes freed.
func PruneStaleAttempts(cutoff time.Time) (int, int64, error) {
	if windowsHandover.Load() {
		return 0, 0, nil
	}
	root := windowsUpdateDir()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	removed, freed := 0, int64(0)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "attempt-") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		var size int64
		_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if fi, err := d.Info(); err == nil {
					size += fi.Size()
				}
			}
			return nil
		})
		if os.RemoveAll(dir) == nil {
			removed++
			freed += size
		}
	}
	return removed, freed, nil
}
