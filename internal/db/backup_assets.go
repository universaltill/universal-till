package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Uploaded photos in backups (ut-docs#2724). The shop's uploaded item and
// category photos and its receipt logo live only under the stable data dir
// (paths.Data("public","assets",…)), never in the database, so a DB-only
// snapshot restored onto a fresh device brought every row back without its
// photo. A backup now carries them INSIDE the same .db file, in a table that
// exists only in snapshots: the one file the operator downloads is the whole
// backup. RestoreBackupAssets writes them back at boot and drops the table,
// so a live database never keeps it.

const (
	backupAssetsTable = "ut_backup_assets"
	// backupAssetMaxBytes matches the 10MB cap the item and category photo
	// upload handlers apply (and syncAssetMaxBytes, ut-docs#2566), so no
	// legitimate upload is left out.
	backupAssetMaxBytes = 10 << 20
)

// backupAssetScopes are the shop-uploaded trees under the assets root. The
// rest of public/assets (built-in icons and logos) ships with each release
// and must keep coming from it (see paths.migrateLegacyUploadedAssets).
var backupAssetScopes = []string{"items", "categories", "logo"}

// tmpAssetSuffixes are in-flight files a backup leaves out: a replica's
// photo download (internal/pages syncTmpSuffix) and this file's own restore.
var tmpAssetSuffixes = []string{".sync-tmp", ".restore-tmp"}

// SnapshotWithAssets writes a Snapshot and adds the uploaded photos under
// assetsRoot to it. A non-empty path with a non-nil error means the
// database snapshot was written but its photos could not all be added: the
// snapshot is kept (a DB backup without photos still beats none) and the
// caller reports the error.
func SnapshotWithAssets(db *sql.DB, dbPath, assetsRoot string) (string, error) {
	dest, err := Snapshot(db, dbPath)
	if err != nil {
		return "", err
	}
	if err := embedBackupAssets(dest, assetsRoot); err != nil {
		return dest, fmt.Errorf("add photos to backup: %w", err)
	}
	return dest, nil
}

func embedBackupAssets(snapshot, assetsRoot string) error {
	sd, err := openBackupFile(snapshot)
	if err != nil {
		return err
	}
	defer sd.Close()
	tx, err := sd.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Rebuilt from scratch every time: a same-second Snapshot reuses an
	// existing file, and rows for photos deleted since must never survive.
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + backupAssetsTable); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE ` + backupAssetsTable + ` (
		path TEXT PRIMARY KEY,
		mod  INTEGER NOT NULL DEFAULT 0,
		data BLOB NOT NULL
	)`); err != nil {
		return err
	}
	for _, scope := range backupAssetScopes {
		root := filepath.Join(assetsRoot, scope)
		// WalkDir doesn't follow a symlinked root (a shop that keeps its
		// photos on another drive); it is our own data dir, so resolve it.
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root && errors.Is(err, fs.ErrNotExist) {
					return nil // nothing uploaded in this scope yet
				}
				return err
			}
			if !d.Type().IsRegular() || hasTmpAssetSuffix(path) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() > backupAssetMaxBytes {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.Join(scope, rel)
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`INSERT OR REPLACE INTO `+backupAssetsTable+` (path, mod, data) VALUES (?, ?, ?)`,
				filepath.ToSlash(rel), info.ModTime().Unix(), body)
			return err
		})
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RestoreBackupAssets writes the photos a restored backup carries back under
// assetsRoot, then drops the table and compacts the file. Call at boot,
// after ApplyPendingRestore and BEFORE Open; it is a cheap no-op for a
// database without the table (every boot but the first after a restore, and
// any backup made before ut-docs#2724), and running it every boot finishes
// a restore that was interrupted between the DB swap and this step.
//
// A photo in the backup replaces the file at the same path; files the
// backup doesn't have are left alone. Paths outside the uploaded-photo
// trees are refused — a backup can be dropped into the backups folder by
// hand. The table is dropped after one attempt even if a file could not be
// written (the error is returned for the log): kept, it would re-write every
// photo on each boot over newer uploads and ride into every later snapshot.
// The backup file itself stays in the backups folder for another restore.
// No VACUUM: it would stall boot on a large database, and the freed pages
// are reused (and every VACUUM INTO snapshot is compact anyway). Returns how
// many files were written.
func RestoreBackupAssets(dbPath, assetsRoot string) (int, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return 0, nil
	}
	sd, err := openBackupFile(dbPath)
	if err != nil {
		return 0, err
	}
	defer sd.Close()
	var n int
	if err := sd.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, backupAssetsTable).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	rows, err := sd.Query(`SELECT path, mod, data FROM ` + backupAssetsTable)
	if err != nil {
		return 0, err
	}
	written := 0
	var writeErr error
	for rows.Next() {
		var rel string
		var mod int64
		var body []byte
		if err := rows.Scan(&rel, &mod, &body); err != nil {
			_ = rows.Close()
			return written, err
		}
		dst, ok := backupAssetPath(assetsRoot, rel)
		if !ok || !underRoot(assetsRoot, dst) {
			continue
		}
		if err := writeRestoredAsset(dst, body, mod); err != nil {
			if writeErr == nil {
				writeErr = fmt.Errorf("restore photo %s: %w", rel, err)
			}
			continue
		}
		written++
	}
	readErr := rows.Err()
	if err := rows.Close(); err != nil && readErr == nil {
		readErr = err
	}
	if _, err := sd.Exec(`DROP TABLE ` + backupAssetsTable); err != nil {
		return written, err
	}
	if readErr != nil {
		return written, readErr
	}
	return written, writeErr
}

// openBackupFile opens a snapshot or the not-yet-open live DB file the same
// way db.go does (escaped URI, busy timeout), so a second same-second
// snapshot caller waits instead of failing with "database is locked".
func openBackupFile(path string) (*sql.DB, error) {
	return sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", escapeSQLiteURIPath(path)))
}

// underRoot reports whether dst's parent directory, with symlinks resolved,
// is still inside assetsRoot — a symlinked directory planted in the photo
// tree must not carry a restored file elsewhere. Directories that don't
// exist yet are created by writeRestoredAsset as real directories.
func underRoot(assetsRoot, dst string) bool {
	root, err := filepath.EvalSymlinks(assetsRoot)
	if err != nil {
		return true // no assets tree yet: nothing in it can be a symlink
	}
	dir := filepath.Dir(dst)
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent // walk up to the deepest directory that exists
	}
}

// backupAssetPath resolves a stored path under assetsRoot, accepting only a
// file inside one of backupAssetScopes: no traversal, no absolute, drive or
// backslash paths (same rules as the photo sync's safePath, ut-docs#2566).
func backupAssetPath(assetsRoot, rel string) (string, bool) {
	if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\:") {
		return "", false
	}
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	scope, rest, ok := strings.Cut(rel, "/")
	if !ok || rest == "" || hasTmpAssetSuffix(rel) {
		return "", false
	}
	known := false
	for _, s := range backupAssetScopes {
		if scope == s {
			known = true
			break
		}
	}
	if !known {
		return "", false
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", false
	}
	return filepath.Join(assetsRoot, clean), true
}

// writeRestoredAsset writes body via a temp file and rename, so a crash
// never leaves a half-written photo in place of a good one.
func writeRestoredAsset(dst string, body []byte, mod int64) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".restore-tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if mod > 0 {
		t := time.Unix(mod, 0)
		_ = os.Chtimes(dst, t, t)
	}
	return nil
}

func hasTmpAssetSuffix(path string) bool {
	for _, s := range tmpAssetSuffixes {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}
