package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SyncBearerSettingsKey is the settings key of a joined (replica) till's LAN
// credential to its main till.
const SyncBearerSettingsKey = "sync.bearer"

// syncBearerFiles maps a *sql.DB to the file that holds its sync bearer
// (ut-docs#5172). SettingsRepo is constructed in dozens of places, so the
// redirect is registered per database, not per repo. A database that is not
// registered (tests, seed tooling) keeps the plain settings-row behaviour.
var syncBearerFiles sync.Map // *sql.DB -> string

// syncBearerMu serialises the read-modify-write of the bearer file
// (GetOrCreate) and the migration against concurrent repo calls.
var syncBearerMu sync.Mutex

// KeepSyncBearerOutOfDB moves the sync bearer out of the database (ut-docs#5172).
// Every DB backup (VACUUM INTO snapshot, an iCloud copy of the data folder, a
// copied .db) used to carry the live LAN credential as a settings row; from
// here on every SettingsRepo over db reads and writes it at path instead,
// which lives under the data root's secrets/ folder (excluded from backup).
//
// A non-empty sync.bearer row is written to the file first -- the row wins
// over an existing file, because it is either a pre-upgrade install, a
// bearer db.ApplyReplicaIdentity just wrote for a fresh join, or a restored
// pre-upgrade backup -- and only then deleted. An empty row is just deleted;
// no row leaves the file as it is.
//
// It fails closed: on any error the row is kept and db is NOT registered, so
// the caller logs and carries on with the old row behaviour and sync never
// breaks. Calling it again for the same db is a no-op.
func KeepSyncBearerOutOfDB(ctx context.Context, db *sql.DB, path string) error {
	if path == "" {
		return errors.New("sync bearer file path is empty")
	}
	syncBearerMu.Lock()
	defer syncBearerMu.Unlock()
	if _, ok := syncBearerFiles.Load(db); ok {
		return nil
	}
	var val string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, SyncBearerSettingsKey).Scan(&val)
	switch {
	case err == sql.ErrNoRows:
		// Nothing to migrate.
	case err != nil:
		return fmt.Errorf("read %s: %w", SyncBearerSettingsKey, err)
	default:
		if val != "" {
			if err := writeSyncBearerFile(path, val); err != nil {
				return err
			}
		}
		if err := secureDeleteBearerRow(ctx, db); err != nil {
			return err
		}
	}
	syncBearerFiles.Store(db, path)
	return nil
}

// secureDeleteBearerRow deletes the sync.bearer row with secure_delete on, so
// its bytes are zeroed instead of left in the page's free space, then
// checkpoints and truncates the WAL so no frame of the old page survives: the
// live .db and -wal are what an iCloud backup of the data folder, or a copied
// .db, carries. The pragma is per connection, so it runs on one pinned
// connection and is switched back off before that returns to the pool (left
// on, it would only cost speed). A checkpoint that cannot finish (a reader
// holds the WAL) is not an error: the row is gone and a later checkpoint
// copies the zeroed page.
func secureDeleteBearerRow(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("delete %s row: %w", SyncBearerSettingsKey, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA secure_delete = ON`); err != nil {
		return fmt.Errorf("delete %s row: secure_delete: %w", SyncBearerSettingsKey, err)
	}
	_, derr := conn.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, SyncBearerSettingsKey)
	_, _ = conn.ExecContext(ctx, `PRAGMA secure_delete = OFF`)
	if derr != nil {
		return fmt.Errorf("delete %s row: %w", SyncBearerSettingsKey, derr)
	}
	_, _ = conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return nil
}

// bearerFile returns the bearer file path when key is the sync bearer and r's
// database is registered; otherwise the key is an ordinary settings row.
func (r *SettingsRepo) bearerFile(key string) (string, bool) {
	if key != SyncBearerSettingsKey {
		return "", false
	}
	return r.registeredBearerFile()
}

// registeredBearerFile is bearerFile without the key test, for the methods
// that span many keys (GetByPrefix, All, ClearReplicaIdentity).
func (r *SettingsRepo) registeredBearerFile() (string, bool) {
	p, ok := syncBearerFiles.Load(r.db)
	if !ok {
		return "", false
	}
	return p.(string), true
}

// readSyncBearerFile returns the stored bearer; a missing file is ("", false, nil).
func readSyncBearerFile(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read sync bearer file: %w", err)
	}
	v := strings.TrimSuffix(string(b), "\n")
	return v, v != "", nil
}

// writeSyncBearerFile stores val atomically (temp file in the same dir +
// rename, 0600, dir 0700). An empty val removes the file: a missing file is
// "no bearer".
func writeSyncBearerFile(path, val string) error {
	if val == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove sync bearer file: %w", err)
		}
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".sync-bearer-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(val)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return errors.Join(werr, cerr)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// storeSyncBearer is writeSyncBearerFile under syncBearerMu, so a write never
// lands between GetOrCreate's read and its write (ut-docs#5172 review).
func storeSyncBearer(path, val string) error {
	syncBearerMu.Lock()
	defer syncBearerMu.Unlock()
	return writeSyncBearerFile(path, val)
}
