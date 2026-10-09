// Package stagedupload owns the temp files the till stages operator uploads
// in (ut-docs#3955): plugin view uploads (#3793) and data-import uploads
// (#599, #601). Each request handler creates its file with one of the
// patterns below and removes it when it is done. A crash or power cut
// mid-request — or, on Windows, a remove that races a reader still in
// flight — leaves the file behind, and nothing else ever cleans the system
// temp directory, so housekeeping calls PruneOlderThan.
//
// The sweep removes only regular files whose names match these patterns;
// every other file in the temp directory belongs to someone else.
package stagedupload

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Patterns for os.CreateTemp("", …). Changing one leaves files made under
// the old name to the OS's own temp clean-up, so keep them stable.
const (
	ViewUploadPattern  = "ut-view-upload-*.upload"  // plugin view uploads (#3793)
	ImportPattern      = "ut-import-*.upload"       // POST /api/data/import (#599)
	ImportStagePattern = "ut-import-stage-*.upload" // import preview staging (#601)
)

// MaxAge is how old a staged upload must be before the sweep removes it.
// A plugin view upload lives at most its job's 300 s ceiling, and an import
// upload only as long as its request. An import preview's staged copy
// expires an hour after it was last (re)staged, but the mtime is not that
// clock: restageCatalogUpload restarts the hour without touching the file,
// and the registry prune is lazy, so an expired copy stays takeable until
// the next preview. A day is far past every live use; the only owner that
// can outlive it is a preview already past its TTL, whose commit then fails
// cleanly with import.error.stage_expired and the operator re-uploads.
const MaxAge = 24 * time.Hour

// Patterns lists every pattern the sweep owns.
func Patterns() []string {
	return []string{ViewUploadPattern, ImportPattern, ImportStagePattern}
}

// Dir is the directory the handlers stage into: os.CreateTemp("") uses
// os.TempDir(). A variable so tests can point it at their own directory.
var Dir = os.TempDir

// Owns reports whether a file name is one of ours.
func Owns(name string) bool {
	for _, p := range Patterns() {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}

// PruneOlderThan removes every staged upload last modified before cutoff
// and reports how many files it removed and how many bytes that freed.
// It never descends into directories, never follows or removes a symlink,
// and treats a file that vanished meanwhile (its owner removed it) as
// nothing to do. The first other error is returned after the sweep ends.
func PruneOlderThan(cutoff time.Time) (int, int64, error) {
	dir := Dir()
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	var (
		removed  int
		freed    int64
		firstErr error
	)
	for _, e := range ents {
		if !e.Type().IsRegular() || !Owns(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			if !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
		freed += info.Size()
	}
	return removed, freed, firstErr
}
