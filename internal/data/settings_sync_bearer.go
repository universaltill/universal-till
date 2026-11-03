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
	"time"
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
		// Any row is a (re)pairing or an unjoin: a previous bearer from an
		// earlier pairing must not outlive it (ut-docs#5203).
		if err := removePrevSyncBearer(path); err != nil {
			return err
		}
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
// lands between GetOrCreate's read and its write (ut-docs#5172 review). Any
// write through it is a join, re-join, unjoin or promotion, not a rotation,
// so the previous bearer is dropped first (ut-docs#5203): a crash in between
// leaves no previous bearer, never a stale one beside a new pairing.
func storeSyncBearer(path, val string) error {
	syncBearerMu.Lock()
	defer syncBearerMu.Unlock()
	if err := removePrevSyncBearer(path); err != nil {
		return err
	}
	return writeSyncBearerFile(path, val)
}

// Previous sync bearer (ut-docs#5203). Since ut-docs#5198 the main till
// rotates a replica's bearer. A main till restored from a backup older than
// the last rotation knows only the bearer the replica rotated away from, so
// the replica keeps that one beside the current bearer (same secrets/ folder,
// so equally out of every backup) and falls back to it once, on a 401.

// prevSyncBearerPath is where the previous bearer of the bearer file at path lives.
func prevSyncBearerPath(path string) string { return path + "-prev" }

func removePrevSyncBearer(path string) error {
	return writeSyncBearerFile(prevSyncBearerPath(path), "")
}

// SyncBearerPrev returns the bearer this till rotated away from, "" when
// there is none or the database does not keep its bearer in a file.
func (r *SettingsRepo) SyncBearerPrev(ctx context.Context) (string, error) {
	path, ok := r.registeredBearerFile()
	if !ok {
		return "", nil
	}
	syncBearerMu.Lock()
	defer syncBearerMu.Unlock()
	v, _, err := readSyncBearerFile(prevSyncBearerPath(path))
	return v, err
}

// RotateSyncBearer replaces the bearer old by fresh, only if old is still the
// stored one (a re-join or another rotation while the caller's call was out
// wins; false, nothing changed). On a file-held bearer the previous value is
// written to the previous-bearer file first, so a crash between the two
// writes leaves both files holding old, never a lost bearer.
func (r *SettingsRepo) RotateSyncBearer(ctx context.Context, old, fresh string) (bool, error) {
	var err error
	done := settingsObs.trace("rotate_sync_bearer")
	defer func() { done(err) }()
	if old == "" || fresh == "" || old == fresh {
		return false, nil
	}
	path, ok := r.registeredBearerFile()
	if !ok {
		var res sql.Result
		res, err = r.db.ExecContext(ctx,
			`UPDATE settings SET value = ?, updated_at = ? WHERE key = ? AND value = ?`,
			fresh, time.Now().UTC(), SyncBearerSettingsKey, old)
		if err != nil {
			return false, settingsObs.wrapf("rotate_sync_bearer", "set setting %s", err, SyncBearerSettingsKey)
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	}
	syncBearerMu.Lock()
	defer syncBearerMu.Unlock()
	cur, _, rerr := readSyncBearerFile(path)
	if rerr != nil {
		err = rerr
		return false, err
	}
	if cur != old {
		return false, nil
	}
	if err = writeSyncBearerFile(prevSyncBearerPath(path), old); err != nil {
		return false, err
	}
	if err = writeSyncBearerFile(path, fresh); err != nil {
		return false, err
	}
	return true, nil
}

// AdoptPrevSyncBearer swaps the current and the previous bearer, only if
// failed is still the stored bearer and a different previous one is held:
// the main till refused failed (it was restored from a backup that predates
// the rotation, or answered a non-auth 401) and accepted the previous one.
// The refused bearer is KEPT as the new previous bearer rather than deleted:
// if the 401 was not a real refusal (a transient lookup error, plain HTTP to
// a pinned till) the main till may still know it, and the next 401 for the
// adopted bearer then falls back to it and heals the mistake.
//
// Crash ordering: the adopted bearer (the one the main till just accepted) is
// written as current first, the refused one as previous second. A crash in
// between leaves current == previous == adopted, so the bearer the main till
// is known to accept is never lost; the cost is only the lost way back to
// the refused bearer. (The other order would leave both files holding the
// refused bearer and lock out a restored-main replica.)
func (r *SettingsRepo) AdoptPrevSyncBearer(ctx context.Context, failed string) (adopted string, ok bool, err error) {
	done := settingsObs.trace("adopt_prev_sync_bearer")
	defer func() { done(err) }()
	path, reg := r.registeredBearerFile()
	if !reg || failed == "" {
		return "", false, nil
	}
	syncBearerMu.Lock()
	defer syncBearerMu.Unlock()
	cur, _, err := readSyncBearerFile(path)
	if err != nil || cur != failed {
		return "", false, err
	}
	prev, _, err := readSyncBearerFile(prevSyncBearerPath(path))
	if err != nil || prev == "" || prev == failed {
		return "", false, err
	}
	if err = writeSyncBearerFile(path, prev); err != nil {
		return "", false, err
	}
	// Failing here is harmless to this call (current == previous is refused
	// by the next adopt) but must not be silent.
	if err = writeSyncBearerFile(prevSyncBearerPath(path), failed); err != nil {
		return prev, true, err
	}
	return prev, true, nil
}
