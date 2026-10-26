// Local backup & restore (docs: architecture/local-backup.md). Snapshot =
// VACUUM INTO (SQLite's safe online copy); restore = staged file applied by
// ApplyPendingRestore BEFORE the DB is opened — the live file is never
// swapped under a running connection.
package db

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

const (
	backupDirName      = "backups"
	restorePendingName = "restore-pending.db"
	backupPrefix       = "unitill-pos-"
	preRestorePrefix   = "pre-restore-"
	// backupTimeLayout is the UTC timestamp in snapshot and pre-restore
	// file names.
	backupTimeLayout = "20060102-150405"
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
	name := backupPrefix + time.Now().UTC().Format(backupTimeLayout) + ".db"
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); err == nil {
		// Same-second snapshot (tests, double-click): VACUUM INTO refuses to
		// overwrite, so reuse the existing file.
		return dest, nil
	}
	// Load-bearing for ut-docs#636: VACUUM INTO rebuilds the entire logical
	// database (every table in sqlite_schema), never a subset. Retained
	// fiscal data (reset-archive tables, 040_reset_archive.sql) depends on
	// that — a future rewrite of this copy toward a selective/per-table
	// export must not silently drop them.
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

// DefaultBackupKeep is how many unitill-pos-* snapshots the daily backup,
// the manual "Back up now" and device housekeeping keep (ut-docs#3092).
const DefaultBackupKeep = 14

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

// PrunePreRestore removes the pre-restore-<ts>.db copies ApplyPendingRestore
// sets aside, keeping at most the newest keep and none older than maxAge
// (whichever removes more), aged by the restore time in the file name. Each copy is a full database, and before
// ut-docs#3092 nothing ever removed them.
//
// Because each copy holds every sale, payment, invoice, Z report and audit
// row that was live at restore time, a copy no older than minRetain -- the
// shop's statutory archive floor (ADR-0040, ut-docs#3365) -- is always kept,
// whatever keep and maxAge say; the caller resolves the floor. It never
// touches real snapshots (unitill-pos-*) or any other file. Returns the
// files removed and the bytes freed; a file that can't be removed is skipped.
func PrunePreRestore(dbPath string, keep int, maxAge time.Duration, now time.Time, minRetain time.Duration) (int, int64, error) {
	dir, err := BackupDir(dbPath)
	if err != nil {
		return 0, 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	var list []BackupInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), preRestorePrefix) || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// os.Rename keeps the old database's mtime, so a till idle for a
		// month would make a copy set aside a minute ago look a month old.
		// Age it by the restore time in its name; fall back to the mtime.
		at := info.ModTime()
		ts := strings.TrimSuffix(strings.TrimPrefix(e.Name(), preRestorePrefix), ".db")
		if parsed, err := time.Parse(backupTimeLayout, ts); err == nil {
			at = parsed
		}
		list = append(list, BackupInfo{Name: e.Name(), Size: info.Size(), ModTime: at})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ModTime.After(list[j].ModTime) })
	removed, freed := 0, int64(0)
	for i, b := range list {
		if (i < keep && now.Sub(b.ModTime) <= maxAge) || now.Sub(b.ModTime) <= minRetain {
			continue
		}
		if os.Remove(filepath.Join(dir, b.Name)) == nil {
			removed++
			freed += b.Size
		}
	}
	return removed, freed, nil
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
	// Streamed, not read into memory: a snapshot now carries the shop's
	// photos too (ut-docs#2724) and can be hundreds of MB on a small till.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	clearRestoreRefused(dbPath)
	pending := filepath.Join(filepath.Dir(dbPath), restorePendingName)
	out, err := os.OpenFile(pending, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(pending)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(pending)
		return err
	}
	return nil
}

// PendingRestore reports whether a staged restore is waiting for a restart.
func PendingRestore(dbPath string) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(dbPath), restorePendingName))
	return err == nil
}

// ApplyPendingRestore swaps a staged restore into place. Call BEFORE Open.
// The replaced DB is kept as pre-restore-<ts>.db in the backup dir.
// Returns whether a restore was applied.
//
// A backup restore (no replica identity staged) keeps THIS till's cloud
// identity (ut-docs#5069, keepLiveCloudIdentity): a file taken on another
// device — a dead till being replaced — must never bring back that device's
// credential. A staged file that cannot be made safe that way is removed
// (it is a copy of a file still in the backups folder), the live DB stays,
// RestoreRefused turns true for Settings → Backups to say so, and boot goes
// on (false, nil) rather than swap in an unchecked file.
func ApplyPendingRestore(dbPath string) (bool, error) {
	pending := filepath.Join(filepath.Dir(dbPath), restorePendingName)
	if _, err := os.Stat(pending); err != nil {
		return false, nil
	}
	dir, err := BackupDir(dbPath)
	if err != nil {
		return false, err
	}
	// A LAN join stages the main till's snapshot through the same file;
	// ApplyReplicaIdentity gives that DB its own identity after Open, and
	// the shop's store link must travel with it, so it is left alone here.
	if _, err := os.Stat(ReplicaIdentityPath(dbPath)); err != nil {
		if kerr := keepLiveCloudIdentity(dbPath, pending); kerr != nil {
			if rerr := os.Remove(pending); rerr != nil && !os.IsNotExist(rerr) {
				return false, fmt.Errorf("withdraw a restore that could not be checked (%v): %w", kerr, rerr)
			}
			_ = os.Remove(pending + "-journal")
			_ = os.Remove(pending + "-wal")
			_ = os.Remove(pending + "-shm")
			markRestoreRefused(dbPath)
			logging.L().Errorf("restore refused, the current data is kept: %v", kerr)
			return false, nil
		}
	}
	if _, err := os.Stat(dbPath); err == nil {
		aside := filepath.Join(dir, preRestorePrefix+time.Now().UTC().Format(backupTimeLayout)+".db")
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

// restoreRefusedName is the marker ApplyPendingRestore leaves next to the DB
// when it refused a staged file; staging a new restore clears it.
const restoreRefusedName = "restore-refused"

func markRestoreRefused(dbPath string) {
	p := filepath.Join(filepath.Dir(dbPath), restoreRefusedName)
	if err := os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		logging.L().Warnf("restore: record the refused restore: %v", err)
	}
}

// RestoreRefused reports whether the last staged restore was refused at
// boot (the file was not a usable till database) and no restore has been
// staged since. Settings → Backups shows it so the manager is not left
// wondering why the data did not change.
func RestoreRefused(dbPath string) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(dbPath), restoreRefusedName))
	return err == nil
}

// clearRestoreRefused drops the marker; every staging path calls it.
func clearRestoreRefused(dbPath string) {
	_ = os.Remove(filepath.Join(filepath.Dir(dbPath), restoreRefusedName))
}

// RestoreKeepsLivePrefixes are the settings a backup restore takes from the
// running till instead of the backup (ut-docs#5069): the till's cloud
// identity (TillCloudIdentityPrefixes: device id, credential, enrolment and
// terms markers, unpaid check-in) plus what is only valid together with
// that credential — the store and merchant it authenticates for, the
// pinned marketplace key, and the entitlement and cloud-link tier the cloud
// answered it — and this device's own support session (diagnostics.*,
// dropped on a LAN join for the same reason, ut-docs#2950). Taken from a
// dead till's backup, these made a replacement till log in as the
// tombstoned till ("Replaced").
//
// The catalogue pull state (cloudsync.catalog.*) is NOT here: it describes
// the rows in the restored file (the generation each row is stamped with),
// so it stays the backup's.
var RestoreKeepsLivePrefixes = append(append([]string(nil), TillCloudIdentityPrefixes...),
	"marketplace.store_id", "marketplace.merchant_id", "marketplace.public_key", "entitlement.", "cloud.link_", "diagnostics.")

// RestoreDropKeys are the hashes of what a till last pushed to the cloud.
// A restore drops them (neither the backup's nor the live till's apply to
// the restored data), so the next sync pushes the restored state: with the
// backup's, a replacement whose cloud copy was overwritten since could
// skip the push as "unchanged".
var RestoreDropKeys = []string{"cloudsync.snapshot_hash", "cloudsync.order_tracking_hash"}

// keepLiveCloudIdentity rewrites the staged restore so its
// RestoreKeepsLivePrefixes rows are exactly the live DB's: the backup's are
// deleted (secure_delete, so the other till's token bytes are zeroed) and
// the live till's copied in. No live DB (or one without a settings table)
// leaves none: the till mints and pairs a fresh identity, never the
// backup's.
//
// A live DB that exists but cannot be read is the corrupt-database case a
// restore is the cure for, almost always with this till's own backup: the
// backup's identity is then kept as it is (the pre-#5069 behaviour), so the
// recovered till stays paired instead of having to pair again. An error
// means the staged file itself is not a usable till database.
func keepLiveCloudIdentity(dbPath, pending string) error {
	keep, readable, err := liveIdentityRows(dbPath)
	if err != nil {
		logging.L().Warnf("restore: the current database could not be read (%v); the backup's own cloud identity is kept", err)
	}
	pd, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=secure_delete(1)&_pragma=temp_store(2)&_pragma=busy_timeout(5000)", escapeSQLiteURIPath(pending)))
	if err != nil {
		return fmt.Errorf("open staged restore: %w", err)
	}
	werr := func() error {
		tx, err := pd.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if readable {
			if err := deleteSettingsPrefixes(tx, RestoreKeepsLivePrefixes); err != nil {
				return err
			}
			for _, kv := range keep {
				if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
					return fmt.Errorf("keep %s: %w", kv[0], err)
				}
			}
		}
		for _, k := range RestoreDropKeys {
			if _, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, k); err != nil {
				return fmt.Errorf("clear %s: %w", k, err)
			}
		}
		return tx.Commit()
	}()
	if cerr := pd.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("keep this till's cloud identity: %w", werr)
	}
	if readable {
		logging.L().Infof("restore: kept this till's own cloud identity (%d setting(s)); the backup's was dropped", len(keep))
	}
	return nil
}

// liveIdentityRows reads the live DB's RestoreKeepsLivePrefixes rows.
// readable is false only when a live DB exists and could not be read (err
// says why); no live DB, or one without a settings table, is readable with
// no rows.
func liveIdentityRows(dbPath string) (rows [][2]string, readable bool, err error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, true, nil
	}
	ld, err := openBackupFile(dbPath)
	if err != nil {
		return nil, false, err
	}
	defer ld.Close()
	var n int
	if err := ld.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'settings'`).Scan(&n); err != nil {
		return nil, false, err
	}
	if n == 0 {
		return nil, true, nil
	}
	q, err := ld.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, false, err
	}
	defer q.Close()
	for q.Next() {
		var k, v string
		if err := q.Scan(&k, &v); err != nil {
			return nil, false, err
		}
		for _, p := range RestoreKeepsLivePrefixes {
			if strings.HasPrefix(k, p) {
				rows = append(rows, [2]string{k, v})
				break
			}
		}
	}
	if err := q.Err(); err != nil {
		return nil, false, err
	}
	return rows, true, nil
}
