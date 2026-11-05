package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// DefaultFileWatchInterval is how often the running server re-checks that its
// database and lock files are still the ones it opened.
const DefaultFileWatchInterval = 30 * time.Second

// FileWatch detects the till's database file or data-dir lock file being
// deleted or replaced while the server runs (ut-docs#5049): an uninstaller or
// a person removing the data folder leaves the process holding an unlinked
// inode — the flock then guards nothing, so a second server could start on
// the same path. Identity is compared with os.SameFile (device+inode on Unix,
// file index on Windows).
type FileWatch struct {
	paths []string
	infos []os.FileInfo
	stat  func(string) (os.FileInfo, error)
}

// NewFileWatch records the identity of the database file and the data-dir
// lock file (a sibling of the database, see AcquireDataDirLock). Both must
// exist now.
func NewFileWatch(dbPath string) (*FileWatch, error) {
	w := &FileWatch{
		paths: []string{dbPath, filepath.Join(filepath.Dir(dbPath), dataDirLockName)},
		stat:  os.Stat,
	}
	for _, p := range w.paths {
		fi, err := w.stat(p)
		if err != nil {
			return nil, fmt.Errorf("watch %s: %w", p, err)
		}
		// On Windows os.Stat fills the file identity lazily, from the path,
		// at the first SameFile call — load it now, or the first Check would
		// compare the replacement with itself.
		_ = os.SameFile(fi, fi)
		w.infos = append(w.infos, fi)
	}
	return w, nil
}

// errStatFailed marks a Check failure that may be transient (EACCES, EIO, a
// stale network mount) rather than proof the file is gone.
var errStatFailed = errors.New("stat failed")

// fileWatchTransientLimit is how many checks in a row may fail with a
// transient stat error before Run gives up: one hiccup must not take a
// healthy till offline mid-trade.
const fileWatchTransientLimit = 3

// Check returns an error naming the first watched file that is gone or is no
// longer the file that was opened at startup. A stat error other than "does
// not exist" wraps errStatFailed.
func (w *FileWatch) Check() error {
	for i, p := range w.paths {
		fi, err := w.stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("the till's file %s was deleted while the server was running — stopping so it does not serve from a deleted database (ut-docs#5049)", p)
		}
		if err != nil {
			return fmt.Errorf("the till's file %s became unreadable while the server was running (%v) — stopping so it does not serve from a deleted database (ut-docs#5049): %w", p, err, errStatFailed)
		}
		if !os.SameFile(w.infos[i], fi) {
			return fmt.Errorf("the till's file %s was deleted or replaced while the server was running — stopping so it does not serve from a deleted database (ut-docs#5049)", p)
		}
	}
	return nil
}

// Run checks every interval until ctx is done. A deleted or replaced file
// calls onLost at once; a transient stat error only after
// fileWatchTransientLimit failed checks in a row. onLost is called once, then
// Run returns. A non-positive interval means the default.
func (w *FileWatch) Run(ctx context.Context, interval time.Duration, onLost func(error)) {
	if interval <= 0 {
		interval = DefaultFileWatchInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	transient := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := w.Check()
			if err == nil {
				transient = 0
				continue
			}
			if errors.Is(err, errStatFailed) {
				if transient++; transient < fileWatchTransientLimit {
					continue
				}
			}
			onLost(err)
			return
		}
	}
}
