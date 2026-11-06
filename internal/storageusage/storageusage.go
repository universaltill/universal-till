// Package storageusage measures what uses space in the till's data
// directory for Settings → Storage (ut-docs#3122). Read-only: Lstat/WalkDir
// only, never follows a symlink, never opens the database.
package storageusage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/logging"
)

// Categories, in the order Report.Rows lists them.
const (
	CatDatabase   = "database"
	CatBackups    = "backups"
	CatLogs       = "logs"
	CatBugReports = "bug_reports"
	CatUpdates    = "updates"
	CatOther      = "other"
)

// Layout is where each row's files live; the caller builds it from the
// packages that own those paths.
type Layout struct {
	DBPath        string // live DB; DBPath+"-wal" and +"-shm" count with it
	DataDir       string // root of the Other row
	BackupsDir    string
	LogsDir       string
	BugReportsDir string
	UpdatesDir    string
}

// Row is one category and its size in bytes.
type Row struct {
	Category string
	Bytes    int64
}

// Report: Rows always holds all six categories in the order above.
// Skipped counts entries that could not be read (logged once per Measure).
type Report struct {
	Rows    []Row
	Skipped int
}

// Measure walks l. A missing directory counts 0. Only regular files count;
// symlinks (file or dir) are never followed or counted. The Other walk over
// DataDir skips the DB trio and any directory equal (after filepath.Clean)
// to one of the bucket dirs, so nothing is counted twice and Other never
// goes negative. Returns ctx.Err() if ctx is cancelled mid-walk.
func Measure(ctx context.Context, l Layout) (Report, error) {
	m := &measurer{ctx: ctx}

	var db int64
	dbFiles := map[string]bool{}
	for _, p := range []string{l.DBPath, l.DBPath + "-wal", l.DBPath + "-shm"} {
		dbFiles[filepath.Clean(p)] = true
		db += m.file(p)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}

	buckets := []struct {
		cat string
		dir string
	}{
		{CatBackups, l.BackupsDir},
		{CatLogs, l.LogsDir},
		{CatBugReports, l.BugReportsDir},
		{CatUpdates, l.UpdatesDir},
	}
	bucketDirs := map[string]bool{}
	sizes := map[string]int64{}
	for _, b := range buckets {
		bucketDirs[filepath.Clean(b.dir)] = true
	}
	for _, b := range buckets {
		n, err := m.walk(b.dir, nil, nil)
		if err != nil {
			return Report{}, err
		}
		sizes[b.cat] = n
	}
	other, err := m.walk(l.DataDir, dbFiles, bucketDirs)
	if err != nil {
		return Report{}, err
	}

	if m.skipped > 0 {
		logging.L().Warnf("[Storage] %d entries could not be read while measuring disk use; sizes may be low", m.skipped)
	}
	return Report{
		Rows: []Row{
			{CatDatabase, db},
			{CatBackups, sizes[CatBackups]},
			{CatLogs, sizes[CatLogs]},
			{CatBugReports, sizes[CatBugReports]},
			{CatUpdates, sizes[CatUpdates]},
			{CatOther, other},
		},
		Skipped: m.skipped,
	}, nil
}

type measurer struct {
	ctx     context.Context
	skipped int
}

// file is the size of one regular file via Lstat; 0 when missing, not a
// regular file or unreadable (counted in skipped unless simply absent).
func (m *measurer) file(p string) int64 {
	fi, err := os.Lstat(p)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			m.skipped++
		}
		return 0
	}
	if !fi.Mode().IsRegular() {
		return 0
	}
	return fi.Size()
}

// walk sums regular files under root. skipFiles are cleaned file paths left
// out; skipDirs are cleaned directory paths not descended into.
func (m *measurer) walk(root string, skipFiles, skipDirs map[string]bool) (int64, error) {
	if root == "" {
		return 0, nil
	}
	var total int64
	cleanRoot := filepath.Clean(root)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if cerr := m.ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				m.skipped++
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			if p == root {
				return fs.SkipAll
			}
			return nil
		}
		cp := filepath.Clean(p)
		if d.IsDir() {
			if cp != cleanRoot && skipDirs[cp] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skipFiles[cp] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				m.skipped++
			}
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
