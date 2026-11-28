package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/db/plugindb"
	"github.com/universaltill/universal-till/internal/diskspace"
)

// Plugin data in backups (ut-docs#3157, ADR-0121). Every plugin's SQLite
// file and blobs live under <data>/plugin-data/<id>/ and nowhere in the main
// database, so a DB-only snapshot restored onto a fresh device lost them. A
// backup now carries them inside the same .db file, in a table that exists
// only in snapshots (like ut_backup_assets). Unlike photos there is NO size
// skip: plugin data is the plugin's only copy. Rows are 1 MiB chunks so a
// large blob is never read whole.
//
// Each plugin.db is copied with plugindb.SnapshotFile (VACUUM INTO), never a
// raw file copy: a live WAL database can have committed pages only in -wal.

const (
	backupPluginDataTable = "ut_backup_plugin_data"
	pluginDataChunk       = 1 << 20
	pluginDBName          = "plugin.db"
	pluginBlobsDir        = "blobs"
)

const (
	// pluginDataBackupDeadline bounds the whole plugin-data part of one
	// backup; pluginSnapshotDeadline bounds one plugin's VACUUM INTO. Both
	// also bound how long a snapshot reader pins the WAL, so a plugin writing
	// meanwhile cannot grow plugin.db-wal without limit (ut-docs#3157
	// performance MUST-FIX 4).
	pluginDataBackupDeadline = 20 * time.Minute
	pluginSnapshotDeadline   = 5 * time.Minute
)

// ErrBackupNoSpace: a plugin database was not snapshotted because the disk
// does not have room for the copy.
var ErrBackupNoSpace = errors.New("backup: not enough free disk space for the plugin database copy")

// pluginFreeBytes reports the free bytes on the disk holding dir; ok is false
// when unknown (then the guard does not refuse). A var so tests can pin it.
var pluginFreeBytes = func(dir string) (uint64, bool) {
	u, err := diskspace.Probe(dir)
	return u.Free, err == nil
}

// checkSnapshotRoom refuses a snapshot when free space is under twice the
// database size: VACUUM INTO writes one full copy to the temp dir, and
// embedding it writes a second into the backup file before the temp copy is
// removed. A full disk would take the till's own database down with it.
func checkSnapshotRoom(tmpDir string, dbSize int64) error {
	free, ok := pluginFreeBytes(tmpDir)
	if ok && dbSize > 0 && free < 2*uint64(dbSize) {
		return fmt.Errorf("%w (need about %d bytes, %d free)", ErrBackupNoSpace, 2*dbSize, free)
	}
	return nil
}

var (
	backupPluginIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	backupPluginBlobPattern = regexp.MustCompile(`^[a-z0-9._-]{1,128}$`)
)

// backupWinReserved mirrors the Windows device-name and trailing-dot rule
// the plugin id and blob name validators apply.
func backupWinReserved(seg string) bool {
	if strings.HasSuffix(seg, ".") {
		return true
	}
	base := seg
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "con", "prn", "aux", "nul":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '0' && base[3] <= '9'
}

func backupPluginIDOK(id string) bool {
	return backupPluginIDPattern.MatchString(id) && !strings.Contains(id, "..") && !backupWinReserved(id)
}

func backupPluginBlobOK(name string) bool {
	return backupPluginBlobPattern.MatchString(name) && !strings.Contains(name, "..") && !backupWinReserved(name)
}

// pluginDataRootFor derives <data>/plugin-data from the photo root
// (<data>/public/assets), so SnapshotWithAssets keeps its signature.
func pluginDataRootFor(assetsRoot string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(assetsRoot)), "plugin-data")
}

// backupPluginPath validates a stored entry path and returns the plugin id
// and the absolute destination. Only "<id>/plugin.db" and
// "<id>/blobs/<name>" are accepted.
func backupPluginPath(root, rel string) (id, dst string, ok bool) {
	if rel == "" || strings.ContainsAny(rel, "\\:") || strings.HasPrefix(rel, "/") {
		return "", "", false
	}
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return "", "", false
		}
	}
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 2 && parts[1] == pluginDBName:
	case len(parts) == 3 && parts[1] == pluginBlobsDir && backupPluginBlobOK(parts[2]):
	default:
		return "", "", false
	}
	if !backupPluginIDOK(parts[0]) {
		return "", "", false
	}
	return parts[0], filepath.Join(root, filepath.FromSlash(rel)), true
}

// EmbedBackupPluginData adds every plugin's database and blobs under
// pluginDataRoot to the snapshot file. A missing root is nothing. Plugins
// are independent: one failing is reported (joined) after the others are
// added, and the snapshot is kept.
func EmbedBackupPluginData(ctx context.Context, snapshot, pluginDataRoot string) error {
	sd, err := openBackupFile(snapshot)
	if err != nil {
		return err
	}
	defer sd.Close()
	sd.SetMaxOpenConns(1)
	if _, err := sd.Exec(`DROP TABLE IF EXISTS ` + backupPluginDataTable); err != nil {
		return err
	}
	if _, err := sd.Exec(`CREATE TABLE ` + backupPluginDataTable + ` (
		path  TEXT NOT NULL,
		chunk INTEGER NOT NULL,
		mod   INTEGER NOT NULL DEFAULT 0,
		data  BLOB NOT NULL,
		PRIMARY KEY (path, chunk)
	) WITHOUT ROWID`); err != nil {
		return err
	}
	entries, err := os.ReadDir(pluginDataRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(snapshot), ".plugin-data-snap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var errs []error
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("plugin data backup stopped: %w", err))
			break
		}
		id := e.Name()
		if !e.IsDir() || !backupPluginIDOK(id) { // symlinks, .removed, junk
			continue
		}
		if err := embedOnePlugin(ctx, sd, pluginDataRoot, tmp, id); err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func embedOnePlugin(ctx context.Context, sd *sql.DB, root, tmp, id string) error {
	dir := filepath.Join(root, id)
	tx, err := sd.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var errs []error
	if fi, err := os.Lstat(filepath.Join(dir, pluginDBName)); err == nil && fi.Mode().IsRegular() {
		snap := filepath.Join(tmp, id+".db")
		sctx, cancel := context.WithTimeout(ctx, pluginSnapshotDeadline)
		err := checkSnapshotRoom(tmp, fi.Size())
		if err == nil {
			err = plugindb.SnapshotFile(sctx, filepath.Join(dir, pluginDBName), snap)
		}
		cancel()
		if err != nil {
			errs = append(errs, err)
		} else {
			err := embedFile(tx, id+"/"+pluginDBName, snap, fi.ModTime())
			_ = os.Remove(snap)
			if err != nil {
				return err
			}
		}
	}
	blobs := filepath.Join(dir, pluginBlobsDir)
	if bi, err := os.Lstat(blobs); err == nil && bi.IsDir() {
		files, err := os.ReadDir(blobs)
		if err != nil {
			return err
		}
		for _, f := range files {
			if !backupPluginBlobOK(f.Name()) {
				continue // in-flight temp files, junk
			}
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue // symlinks and non-files are never followed
			}
			if err := embedFile(tx, id+"/"+pluginBlobsDir+"/"+f.Name(), filepath.Join(blobs, f.Name()), info.ModTime()); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue // removed while we walked
				}
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

func embedFile(tx *sql.Tx, rel, src string, mod time.Time) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, pluginDataChunk)
	for chunk := 0; ; chunk++ {
		n, err := io.ReadFull(f, buf)
		if n > 0 || chunk == 0 { // an empty file still gets one row
			if _, e := tx.Exec(`INSERT INTO `+backupPluginDataTable+` (path, chunk, mod, data) VALUES (?, ?, ?, ?)`,
				rel, chunk, mod.Unix(), buf[:n]); e != nil {
				return e
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// RestoreBackupPluginData writes the plugin databases and blobs a restored
// backup carries back under pluginDataRoot, then drops the table. Call at
// boot after ApplyPendingRestore and BEFORE plugins load (no plugin has a
// database open yet). A cheap no-op without the table.
//
// Only entries in the archive are written: another plugin's files are never
// touched. Entries must be "<id>/plugin.db" or "<id>/blobs/<name>" with a
// valid id and blob name; anything else, and any entry whose directory or
// target is a symlink or resolves outside the root, is skipped and reported.
// A restored plugin.db replaces the old file and its stale -wal/-shm. The
// table is dropped after one attempt (the error is returned for the log).
func RestoreBackupPluginData(dbPath, pluginDataRoot string) (int, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return 0, nil
	}
	sd, err := openBackupFile(dbPath)
	if err != nil {
		return 0, err
	}
	defer sd.Close()
	sd.SetMaxOpenConns(1)
	var n int
	if err := sd.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, backupPluginDataTable).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	var rels []string
	rows, err := sd.Query(`SELECT DISTINCT path FROM ` + backupPluginDataTable + ` ORDER BY path`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return 0, err
		}
		rels = append(rels, p)
	}
	readErr := rows.Err()
	_ = rows.Close()
	written := 0
	var errs []error
	for _, rel := range rels {
		id, dst, ok := backupPluginPath(pluginDataRoot, rel)
		if !ok {
			errs = append(errs, fmt.Errorf("refused backup entry %q", rel))
			continue
		}
		if err := restorePluginFile(sd, pluginDataRoot, id, dst, rel); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", rel, err))
			continue
		}
		written++
	}
	if _, err := sd.Exec(`DROP TABLE ` + backupPluginDataTable); err != nil {
		errs = append(errs, err)
	}
	if readErr != nil {
		errs = append(errs, readErr)
	}
	return written, errors.Join(errs...)
}

func errSymlinkEntry(p string) error { return fmt.Errorf("%s is a symlink or not a plain file/dir", p) }

// plainDirOrMissing: Lstat says real directory, or nothing there yet.
func plainDirOrMissing(p string) (bool, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.IsDir() { // Lstat reports a symlink as non-dir
		return false, errSymlinkEntry(p)
	}
	return true, nil
}

func restorePluginFile(sd *sql.DB, root, id, dst, rel string) error {
	// The root itself may be a symlink to another drive (resolve once); every
	// directory below it must be a real directory.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	dir := filepath.Join(resolved, id)
	chain := []string{dir}
	if strings.Contains(rel, "/"+pluginBlobsDir+"/") {
		chain = append(chain, filepath.Join(dir, pluginBlobsDir))
	}
	for _, d := range chain {
		exists, err := plainDirOrMissing(d)
		if err != nil {
			return err
		}
		if !exists {
			if err := os.Mkdir(d, 0o700); err != nil {
				return err
			}
		}
	}
	target := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(rel, id+"/")))
	if !strings.HasPrefix(target, resolved+string(filepath.Separator)) {
		return errors.New("path escapes the plugin data root")
	}
	if fi, err := os.Lstat(target); err == nil {
		if !fi.Mode().IsRegular() {
			return errSymlinkEntry(target)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := target + ".restore-tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	var mod int64
	crows, err := sd.Query(`SELECT mod, data FROM `+backupPluginDataTable+` WHERE path = ? ORDER BY chunk`, rel)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	for crows.Next() {
		var b []byte
		if err := crows.Scan(&mod, &b); err != nil {
			err = errors.Join(err, crows.Close(), f.Close())
			_ = os.Remove(tmp)
			return err
		}
		if _, err := f.Write(b); err != nil {
			err = errors.Join(err, crows.Close(), f.Close())
			_ = os.Remove(tmp)
			return err
		}
	}
	err = errors.Join(crows.Err(), crows.Close(), f.Close())
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if strings.HasSuffix(rel, "/"+pluginDBName) {
		// Stale WAL/SHM of the replaced file must not be replayed onto it.
		_ = os.Remove(target + "-wal")
		_ = os.Remove(target + "-shm")
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if mod > 0 {
		t := time.Unix(mod, 0)
		_ = os.Chtimes(target, t, t)
	}
	return nil
}
