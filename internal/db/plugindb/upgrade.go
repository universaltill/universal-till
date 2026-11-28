package plugindb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const preUpgradeDir = ".pre-upgrade"

// versionName is a path-safe plugin version used as a file name.
var versionName = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

func validVersion(v string) error {
	if !versionName.MatchString(v) || strings.Contains(v, "..") {
		return fmt.Errorf("plugindb: invalid version %q", v)
	}
	return nil
}

// Upgrade is the handle of a finished MigrateForInstall/RestorePreUpgrade.
type Upgrade struct {
	// Undo puts the database back as it was before the call: the pre-upgrade
	// file is restored, or plugin.db is deleted when the call created it.
	Undo func() error
}

type preUpgradeMeta struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	At         time.Time `json:"at"`
	Migrations int       `json:"migrations"`
}

func noopUpgrade() *Upgrade { return &Upgrade{Undo: func() error { return nil }} }

func removeDBFiles(p string) {
	for _, s := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(p + s)
	}
}

// replaceDBFile puts src in place of the (closed, checkpointed) database p.
// Only the side files are removed first and the main file is swapped by one
// rename, so a crash at any point leaves either the old or the new database,
// never none: a missing plugin.db would be recreated empty by the next Open.
func replaceDBFile(src, p string) error {
	_ = os.Remove(p + "-wal")
	_ = os.Remove(p + "-shm")
	return copyFileAtomic(src, p)
}

// copyFileAtomic copies src to dst via a temp file in dst's directory and a
// rename, 0600.
func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// preDir returns <id>/.pre-upgrade, created when asked, refused if a link.
func preDir(idDir string, create bool) (string, error) {
	p := filepath.Join(idDir, preUpgradeDir)
	ok, err := realDir(p)
	if err != nil {
		return "", err
	}
	if !ok && create {
		if err := os.Mkdir(p, 0o700); err != nil && !os.IsExist(err) {
			return "", err
		}
		if ok, err = realDir(p); err != nil || !ok {
			if err == nil {
				err = ErrSymlink
			}
			return "", err
		}
	}
	return p, nil
}

// MigrateForInstall applies the artifact's migrations for an install or
// upgrade from fromVersion ("" for a fresh install) to toVersion. If the
// plugin already has a database it is first copied (VACUUM INTO) to
// .pre-upgrade/<fromVersion>.db; migrations then run in one transaction. On
// error the database is unchanged. Undo reverts a successful call (later
// install steps failed). No migrations: nothing is touched.
func (m *Manager) MigrateForInstall(ctx context.Context, id, fromVersion, toVersion string, o Options) (*Upgrade, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if len(o.Migrations) == 0 {
		return noopUpgrade(), nil
	}
	if err := validVersion(toVersion); err != nil {
		return nil, err
	}
	if fromVersion != "" {
		if err := validVersion(fromVersion); err != nil {
			return nil, err
		}
	}
	unlock := m.lockID(id)
	defer unlock()
	if err := m.Close(id); err != nil {
		return nil, err
	}
	dir, err := m.safeDir(id, true)
	if err != nil {
		return nil, err
	}
	dbp := filepath.Join(dir, DBFileName)
	exists, err := regularOrMissing(dbp)
	if err != nil {
		return nil, err
	}
	if fi, e := os.Stat(dbp); e == nil && fi.Size() == 0 {
		exists = false
	}
	var copyPath string
	if exists {
		pd, err := preDir(dir, true)
		if err != nil {
			return nil, err
		}
		name := fromVersion
		if name == "" {
			name = "unknown"
		}
		copyPath = filepath.Join(pd, name+".db")
		tmp := copyPath + ".tmp"
		_ = os.Remove(tmp)
		if err := SnapshotFile(ctx, dbp, tmp); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		if err := os.Rename(tmp, copyPath); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		meta, _ := json.Marshal(preUpgradeMeta{From: fromVersion, To: toVersion, At: m.now().UTC(), Migrations: len(o.Migrations)})
		if err := os.WriteFile(filepath.Join(pd, name+".json.tmp"), meta, 0o600); err == nil {
			_ = os.Rename(filepath.Join(pd, name+".json.tmp"), filepath.Join(pd, name+".json"))
		}
	}
	o.NoMigrate = false
	s, err := openStore(ctx, id, dir, o, m.now())
	if err != nil {
		if !exists {
			removeDBFiles(dbp)
		}
		return nil, err
	}
	_ = s.Close()
	var once sync.Once
	var undoErr error
	return &Upgrade{Undo: func() error {
		once.Do(func() {
			u := m.lockID(id)
			defer u()
			_ = m.Close(id)
			if exists {
				undoErr = replaceDBFile(copyPath, dbp)
			} else {
				removeDBFiles(dbp)
			}
		})
		return undoErr
	}}, nil
}

// RestorePreUpgrade rolls the database back to the pre-upgrade copy made when
// targetVersion was upgraded away from. The current database is itself kept
// as .pre-upgrade/rolled-back-<time>.db (never read by older code). Returns
// ErrNoPreUpgrade when no copy exists. Undo reinstates the current database.
func (m *Manager) RestorePreUpgrade(ctx context.Context, id, targetVersion string) (*Upgrade, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := validVersion(targetVersion); err != nil {
		return nil, err
	}
	unlock := m.lockID(id)
	defer unlock()
	dir, err := m.safeDir(id, false)
	if err != nil {
		return nil, err
	}
	if ok, err := realDir(dir); err != nil || !ok {
		if err == nil {
			err = ErrNoPreUpgrade
		}
		return nil, err
	}
	pd, err := preDir(dir, false)
	if err != nil {
		return nil, err
	}
	copyPath := filepath.Join(pd, targetVersion+".db")
	if ok, err := regularOrMissing(copyPath); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNoPreUpgrade
	}
	if err := m.Close(id); err != nil {
		return nil, err
	}
	dbp := filepath.Join(dir, DBFileName)
	keep := ""
	if ok, err := regularOrMissing(dbp); err != nil {
		return nil, err
	} else if ok {
		keep = filepath.Join(pd, fmt.Sprintf("rolled-back-%d.db", m.now().UnixNano()))
		if err := SnapshotFile(ctx, dbp, keep); err != nil {
			return nil, err
		}
	}
	if err := replaceDBFile(copyPath, dbp); err != nil {
		return nil, err // the current database is still in place
	}
	var once sync.Once
	var undoErr error
	return &Upgrade{Undo: func() error {
		once.Do(func() {
			u := m.lockID(id)
			defer u()
			_ = m.Close(id)
			if keep != "" {
				undoErr = replaceDBFile(keep, dbp)
			} else {
				removeDBFiles(dbp)
			}
		})
		return undoErr
	}}, nil
}

// CheckCompatible reads the plugin's migration ledger without changing
// anything and reports ErrMigrationBehind (database newer than ms) or
// ErrMigrationTampered. A plugin with no database is compatible.
func (m *Manager) CheckCompatible(ctx context.Context, id string, ms []Migration) error {
	if err := validID(id); err != nil {
		return err
	}
	unlock := m.lockID(id)
	defer unlock()
	if err := m.Close(id); err != nil {
		return err
	}
	dir, err := m.safeDir(id, false)
	if err != nil {
		return err
	}
	dbp := filepath.Join(dir, DBFileName)
	ok, err := regularOrMissing(dbp)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)&_pragma=trusted_schema(0)&_pragma=query_only(1)", escapePath(dbp)))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='_ut_migrations'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM _ut_migrations`)
	if err != nil {
		return err
	}
	defer rows.Close()
	applied := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return err
		}
		applied[v] = sum
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return checkApplied(applied, ms)
}

// SweepPreUpgrade clears stale upgrade state for every plugin: *.tmp leftovers
// (a crash between copy and activate), pre-upgrade files older than maxAge,
// and all but the newest keep version copies. rolled-back-* files are never
// swept: each is the only copy of the rows a newer version wrote before a
// rollback (possibly records under retention.keep_years), so it stays until
// the plugin's data is purged as a whole after uninstall.
func (m *Manager) SweepPreUpgrade(maxAge time.Duration, keep int) error {
	rr, err := m.resolvedRoot()
	if err != nil {
		return err
	}
	ents, err := os.ReadDir(rr)
	if err != nil {
		return err
	}
	var first error
	for _, e := range ents {
		id := e.Name()
		if !e.IsDir() || validID(id) != nil {
			continue
		}
		if err := m.sweepOne(id, maxAge, keep); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *Manager) sweepOne(id string, maxAge time.Duration, keep int) error {
	unlock := m.lockID(id)
	defer unlock()
	dir, err := m.safeDir(id, false)
	if err != nil {
		return nil // a linked dir is never touched
	}
	pd, err := preDir(dir, false)
	if err != nil {
		return nil
	}
	ents, err := os.ReadDir(pd)
	if err != nil {
		return nil
	}
	type cp struct {
		base string
		at   time.Time
	}
	var copies []cp
	cutoff := m.now().Add(-maxAge)
	rm := func(base string) {
		_ = os.Remove(filepath.Join(pd, base+".db"))
		_ = os.Remove(filepath.Join(pd, base+".json"))
	}
	for _, e := range ents {
		name := e.Name()
		if e.Type()&os.ModeSymlink != 0 || !e.Type().IsRegular() {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".tmp"):
			_ = os.Remove(filepath.Join(pd, name))
		case strings.HasSuffix(name, ".db"):
			base := strings.TrimSuffix(name, ".db")
			fi, err := e.Info()
			if err != nil {
				continue
			}
			at := fi.ModTime()
			if strings.HasPrefix(base, "rolled-back-") {
				continue // business data, never aged out (see SweepPreUpgrade)
			}
			if at.Before(cutoff) {
				rm(base)
				continue
			}
			copies = append(copies, cp{base, at})
		}
	}
	sort.Slice(copies, func(i, j int) bool { return copies[i].at.After(copies[j].at) })
	if keep >= 0 {
		for i := keep; i < len(copies); i++ {
			rm(copies[i].base)
		}
	}
	return nil
}
