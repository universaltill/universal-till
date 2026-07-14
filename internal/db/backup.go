// Local backup & restore (docs: architecture/local-backup.md). Snapshot =
// VACUUM INTO (SQLite's safe online copy); restore = staged file applied by
// ApplyPendingRestore BEFORE the DB is opened — the live file is never
// swapped under a running connection.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	backupDirName      = "backups"
	restorePendingName = "restore-pending.db"
	backupPrefix       = "unitill-pos-"
)

// BackupDir returns (and creates) the backup directory next to the DB file.
func BackupDir(dbPath string) (string, error) {
	dir := filepath.Join(filepath.Dir(dbPath), backupDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("backup dir: %w", err)
	}
	return dir, nil
}

// Snapshot writes a compact online copy of the live DB and returns its path.
func Snapshot(db *sql.DB, dbPath string) (string, error) {
	dir, err := BackupDir(dbPath)
	if err != nil {
		return "", err
	}
	name := backupPrefix + time.Now().UTC().Format("20060102-150405") + ".db"
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); err == nil {
		// Same-second snapshot (tests, double-click): VACUUM INTO refuses to
		// overwrite, so reuse the existing file.
		return dest, nil
	}
	if _, err := db.Exec(`VACUUM INTO ?`, dest); err != nil {
		return "", fmt.Errorf("vacuum into: %w", err)
	}
	return dest, nil
}

// BackupInfo describes one snapshot for the settings UI.
type BackupInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// ListBackups returns snapshots newest-first.
func ListBackups(dbPath string) ([]BackupInfo, error) {
	dir, err := BackupDir(dbPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []BackupInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), backupPrefix) || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// ValidBackupName guards path traversal on user-supplied backup names.
func ValidBackupName(name string) bool {
	return name == filepath.Base(name) &&
		strings.HasPrefix(name, backupPrefix) && strings.HasSuffix(name, ".db")
}

// PruneBackups keeps the newest keep snapshots, removing the rest.
func PruneBackups(dbPath string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	list, err := ListBackups(dbPath)
	if err != nil {
		return err
	}
	dir, _ := BackupDir(dbPath)
	for _, b := range list[min(keep, len(list)):] {
		_ = os.Remove(filepath.Join(dir, b.Name))
	}
	return nil
}

// StageRestore marks a snapshot to become the live DB on next start.
func StageRestore(dbPath, backupName string) error {
	if !ValidBackupName(backupName) {
		return fmt.Errorf("invalid backup name")
	}
	dir, err := BackupDir(dbPath)
	if err != nil {
		return err
	}
	src := filepath.Join(dir, backupName)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("backup not found: %w", err)
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	pending := filepath.Join(filepath.Dir(dbPath), restorePendingName)
	return os.WriteFile(pending, raw, 0o644)
}

// PendingRestore reports whether a staged restore is waiting for a restart.
func PendingRestore(dbPath string) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(dbPath), restorePendingName))
	return err == nil
}

// ApplyPendingRestore swaps a staged restore into place. Call BEFORE Open.
// The replaced DB is kept as pre-restore-<ts>.db in the backup dir.
// Returns whether a restore was applied.
func ApplyPendingRestore(dbPath string) (bool, error) {
	pending := filepath.Join(filepath.Dir(dbPath), restorePendingName)
	if _, err := os.Stat(pending); err != nil {
		return false, nil
	}
	dir, err := BackupDir(dbPath)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(dbPath); err == nil {
		aside := filepath.Join(dir, "pre-restore-"+time.Now().UTC().Format("20060102-150405")+".db")
		if err := os.Rename(dbPath, aside); err != nil {
			return false, fmt.Errorf("set aside current db: %w", err)
		}
		// SQLite sidecar files from the old DB must not pollute the restored one.
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
	}
	if err := os.Rename(pending, dbPath); err != nil {
		return false, fmt.Errorf("apply restore: %w", err)
	}
	return true, nil
}
