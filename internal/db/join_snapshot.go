// Join snapshot (ut-docs#426): the full-DB copy served to a joining replica
// over GET /api/sync/snapshot must NOT carry other tills' bearer_hash — the
// sync-auth secret that must never leave the primary (same rule the D4
// admin-bundle redactCols in internal/data/sync_admin_repo.go enforces on
// every incremental pull). Real disaster-recovery backups (Snapshot) keep
// the real values: restoring one restores the shop's real till roster.
package db

import (
	"database/sql"
	"fmt"
	"io"
	"os"
)

// RedactedJoinSnapshot produces a throwaway, bearer_hash-redacted COPY of
// the live DB for serving to a joining replica over GET /api/sync/snapshot.
// It never touches the real backup snapshot Snapshot() produces (that one
// stays pristine — it's a real disaster-recovery artifact and appears in
// the settings-page backups list). Caller must call the returned cleanup
// func (defer it) once the copy has been served.
func RedactedJoinSnapshot(db *sql.DB, dbPath string) (string, func(), error) {
	snap, err := Snapshot(db, dbPath)
	if err != nil {
		return "", nil, err
	}
	dir, err := BackupDir(dbPath)
	if err != nil {
		return "", nil, err
	}
	// The name deliberately lacks the "unitill-pos-" backupPrefix, so the
	// copy is invisible to ListBackups()/ValidBackupName() — it is not a
	// backup and must never show up in (or be restored from) the settings
	// page. CreateTemp also keeps two same-second joins from colliding.
	tmp, err := os.CreateTemp(dir, "join-snapshot-*.db")
	if err != nil {
		return "", nil, fmt.Errorf("create join snapshot copy: %w", err)
	}
	copyPath := tmp.Name()
	cleanup := func() {
		_ = os.Remove(copyPath)
		// Defensive: sidecars would only exist if a pragma changed the
		// journal mode on the copy, but removing them is free.
		_ = os.Remove(copyPath + "-wal")
		_ = os.Remove(copyPath + "-shm")
	}
	src, err := os.Open(snap)
	if err != nil {
		tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("open snapshot for copy: %w", err)
	}
	_, copyErr := io.Copy(tmp, src)
	src.Close()
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("copy snapshot: %w", copyErr)
	}

	// Redact on the COPY only, over a dedicated connection — never the
	// original snapshot file, never the live DB. Zero rows is fine (a fresh
	// primary serving its first join): UPDATE simply affects nothing.
	//
	// secure_delete(1) is load-bearing, not defensive: a plain UPDATE only
	// changes what SQL reports — SQLite doesn't zero the bytes a shrunk
	// record frees, so the real bearer_hash strings stay physically present
	// in the file's page slack and are trivially recoverable with
	// strings/grep on the .db file the replica receives, defeating the
	// whole point of this redaction (ut-docs#426 review finding, confirmed
	// empirically: without this pragma, 3/4 seeded hashes survived in the
	// raw bytes despite every row reading back NULL over SQL). Setting it
	// via `_pragma=` in the DSN (not an Exec'd PRAGMA) applies it before
	// this connection's first write, per the same pooled-connection
	// reasoning documented in db.go — an Exec'd PRAGMA only binds the one
	// connection that ran it, which this single-use short-lived handle
	// happens to avoid, but the DSN form is the same guarantee openDB
	// already relies on elsewhere and costs nothing extra here.
	cdb, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=secure_delete(1)", copyPath))
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("open join snapshot copy: %w", err)
	}
	_, execErr := cdb.Exec(`UPDATE tills SET bearer_hash = NULL`)
	if closeErr := cdb.Close(); execErr == nil {
		execErr = closeErr
	}
	if execErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("redact tills in join snapshot: %w", execErr)
	}
	return copyPath, cleanup, nil
}
