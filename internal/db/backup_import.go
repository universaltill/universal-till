package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Errors ImportBackupFile answers with; the handler maps each to a message
// the manager can act on (ut-docs#5070).
var (
	// ErrBackupTooLarge: the upload is over the cap the caller set.
	ErrBackupTooLarge = errors.New("backup file is larger than this till can take")
	// ErrNotTillBackup: not an SQLite file, damaged, or not a till database.
	ErrNotTillBackup = errors.New("not a Universal Till backup")
	// ErrBackupNewer: made by a newer Universal Till than this one.
	ErrBackupNewer = errors.New("backup was made by a newer version of Universal Till")
	// ErrBackupTooOld: made by a version from before the schema reset
	// (ADR-0074), which this build cannot open.
	ErrBackupTooOld = errors.New("backup was made by a version too old to restore")
	// ErrBackupOtherShop: the backup belongs to another my. shop than the
	// one this till is paired with.
	ErrBackupOtherShop = errors.New("backup belongs to another shop")
)

// sqliteMagic is the first 16 bytes of every SQLite 3 database file.
var sqliteMagic = []byte("SQLite format 3\x00")

// uploadPartPattern names an upload being checked in the backups folder; it
// never matches ValidBackupName, so a half-written or refused upload is never
// listed or restorable. SweepOrphanedUploads removes what a crash left.
const uploadPartPattern = "upload-*.part"

// ImportedBackup describes a backup file a manager uploaded, once it passed
// every check and sits in the backups folder as a normal snapshot.
type ImportedBackup struct {
	// Name is the file's name in the backups folder (ValidBackupName).
	Name string
	// ShopName is the backup's store.name ("" when it has none).
	ShopName string
	// Sales is how many sales the backup holds.
	Sales int
	// NewestSale is the newest sale's created_at; zero when there are none.
	// Sales after it are not in the backup: the cut-off the manager is told.
	NewestSale time.Time
}

// ImportBackupFile checks an uploaded backup and, when it is a usable till
// database, saves it in the backups folder as a restorable snapshot
// (ut-docs#5070). It never touches the live database; restoring it is the
// separate, step-up-guarded StageRestore.
//
// Checks, in order: at most maxBytes; the SQLite header; PRAGMA
// quick_check; a till schema no newer than this build; then Open on the
// upload's own copy, which runs exactly the migrations and ledger checks the
// next boot would, so a file that passes here also opens after the restore.
// When liveStoreID is set and the backup names a different
// marketplace.store_id, it is another shop's: ErrBackupOtherShop. The
// copy's cloud identity is removed (secure_delete) before it is kept, so
// another device's credential is never stored again (ApplyPendingRestore
// also keeps this till's own at restore time, ut-docs#5069).
func ImportBackupFile(dbPath string, src io.Reader, maxBytes int64, liveStoreID string) (ImportedBackup, error) {
	dir, err := BackupDir(dbPath)
	if err != nil {
		return ImportedBackup{}, err
	}
	tmp, err := os.CreateTemp(dir, uploadPartPattern)
	if err != nil {
		return ImportedBackup{}, fmt.Errorf("stage upload: %w", err)
	}
	part := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(part)
			_ = os.Remove(part + "-wal")
			_ = os.Remove(part + "-shm")
			_ = os.Remove(part + "-journal")
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(src, maxBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return ImportedBackup{}, fmt.Errorf("stage upload: %w", err)
	}
	if n > maxBytes {
		return ImportedBackup{}, ErrBackupTooLarge
	}
	if err := checkSQLiteHeader(part); err != nil {
		return ImportedBackup{}, err
	}
	if err := checkCandidateSchema(part); err != nil {
		return ImportedBackup{}, err
	}
	// The real boot path, on the upload's own copy.
	cand, err := Open(part)
	if errors.Is(err, ErrDatabasePredatesReset) {
		return ImportedBackup{}, fmt.Errorf("%w: %v", ErrBackupTooOld, err)
	}
	if err != nil {
		return ImportedBackup{}, fmt.Errorf("%w: %v", ErrNotTillBackup, err)
	}
	// Only this build's own triggers, views and tables (a crafted trigger
	// could put another till's credential back after the strip below).
	ierr := checkMigratedSchema(cand.DB, dir)
	var info ImportedBackup
	if ierr == nil {
		info, ierr = inspectBackup(cand.DB)
	}
	storeID := ""
	if ierr == nil {
		storeID, ierr = backupSetting(cand.DB, "marketplace.store_id")
	}
	if cerr := cand.Close(); ierr == nil {
		ierr = cerr
	}
	if ierr != nil {
		return ImportedBackup{}, fmt.Errorf("%w: %v", ErrNotTillBackup, ierr)
	}
	if live := strings.TrimSpace(liveStoreID); live != "" && strings.TrimSpace(storeID) != "" && strings.TrimSpace(storeID) != live {
		return ImportedBackup{}, ErrBackupOtherShop
	}
	if err := stripCloudIdentityFile(part); err != nil {
		return ImportedBackup{}, fmt.Errorf("remove the backup's cloud identity: %w", err)
	}
	name, err := freeImportName(dir, time.Now().UTC())
	if err != nil {
		return ImportedBackup{}, err
	}
	if err := os.Rename(part, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(filepath.Join(dir, name))
		return ImportedBackup{}, fmt.Errorf("keep upload: %w", err)
	}
	keep = true
	info.Name = name
	return info, nil
}

func checkSQLiteHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, sqliteMagic) {
		return ErrNotTillBackup
	}
	return nil
}

// checkCandidateSchema runs PRAGMA quick_check and compares the file's
// migration ledger with this build's newest migration, read-only.
func checkCandidateSchema(path string) error {
	ro, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", escapeSQLiteURIPath(path)))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotTillBackup, err)
	}
	defer ro.Close()
	var check string
	if err := ro.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil || check != "ok" {
		return fmt.Errorf("%w: integrity check: %q %v", ErrNotTillBackup, check, err)
	}
	var tables int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name IN ('schema_migrations', 'settings', 'sales')`).Scan(&tables); err != nil || tables != 3 {
		return fmt.Errorf("%w: missing till tables", ErrNotTillBackup)
	}
	var version int
	if err := ro.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("%w: %v", ErrNotTillBackup, err)
	}
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(migs) > 0 && version > migs[len(migs)-1].Version {
		return ErrBackupNewer
	}
	return nil
}

func inspectBackup(d *sql.DB) (ImportedBackup, error) {
	var out ImportedBackup
	name, err := backupSetting(d, "store.name")
	if err != nil {
		return out, err
	}
	out.ShopName = name
	var newest sql.NullString
	if err := d.QueryRow(`SELECT COUNT(*), MAX(created_at) FROM sales WHERE status IN ('completed', 'voided')`).Scan(&out.Sales, &newest); err != nil {
		return out, err
	}
	if newest.Valid {
		out.NewestSale = parseSQLiteTime(newest.String)
	}
	return out, nil
}

// parseSQLiteTime reads the two shapes sales.created_at holds: SQLite's
// datetime('now') and RFC 3339. Unparseable → zero.
func parseSQLiteTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func backupSetting(d *sql.DB, key string) (string, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func stripCloudIdentityFile(path string) error {
	cd, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=secure_delete(1)&_pragma=busy_timeout(5000)", escapeSQLiteURIPath(path)))
	if err != nil {
		return err
	}
	err = DeleteTillCloudIdentity(cd)
	if cerr := cd.Close(); err == nil {
		err = cerr
	}
	return err
}

// freeImportName reserves a snapshot name for an uploaded backup:
// unitill-pos-<UTC>-file.db, then -file2, -file3 … It creates the file
// (O_EXCL) so two uploads in the same second never get the same name; the
// caller renames the checked upload over its own placeholder.
func freeImportName(dir string, now time.Time) (string, error) {
	base := backupPrefix + now.Format(backupTimeLayout) + "-file"
	for i := 1; i < 100; i++ {
		name := base + ".db"
		if i > 1 {
			name = fmt.Sprintf("%s%d.db", base, i)
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_ = f.Close()
		return name, nil
	}
	return "", fmt.Errorf("no free name for the uploaded backup")
}

// uploadOrphanAge is how old an upload-*.part file must be before
// SweepOrphanedUploads treats it as left behind by a crash mid-upload.
const uploadOrphanAge = 24 * time.Hour

// SweepOrphanedUploads removes upload-*.part files (and their SQLite
// sidecars) older than uploadOrphanAge from the backups folder. Called at
// boot next to SweepOrphanedJoinSnapshots.
func SweepOrphanedUploads(dbPath string, now time.Time) (int, error) {
	dir, err := BackupDir(dbPath)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		base := e.Name()
		for _, side := range []string{"-wal", "-shm", "-journal"} {
			base = strings.TrimSuffix(base, side)
		}
		if ok, _ := filepath.Match(uploadPartPattern, base); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < uploadOrphanAge {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}

// IsUploadedBackupName reports whether a backup name is one ImportBackupFile
// kept (unitill-pos-<ts>-file[N].db). Such a file is not a snapshot of this
// till's data, so the daily backup does not count it as "a fresh backup".
func IsUploadedBackupName(name string) bool {
	if !ValidBackupName(name) {
		return false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, backupPrefix), ".db")
	i := strings.LastIndex(stem, "-file")
	if i < 0 {
		return false
	}
	rest := stem[i+len("-file"):]
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
