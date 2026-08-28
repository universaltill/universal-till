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
	"path/filepath"
	"time"
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
	// Load-bearing for ut-docs#636: a raw byte copy of the whole snapshot
	// file, never a per-table export — a joining replica must inherit
	// retained fiscal data (reset-archive tables) the same way it inherits
	// everything else. Redaction below stays limited to one column on one
	// table for the same reason: anything more selective here risks quietly
	// dropping data a future migration adds.
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
	// temp_store(2) keeps this handle inside db.go's temp-dir-free
	// invariant (ut-docs#1239) — its current statements need no temp
	// b-tree, but Android has no writable temp dir to fall back on if
	// that ever changes.
	cdb, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=secure_delete(1)&_pragma=temp_store(2)", copyPath))
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

// joinSnapshotOrphanAge is how long a join-snapshot-*.db file may sit in the
// backup directory before SweepOrphanedJoinSnapshots treats it as orphaned by
// a crash between os.CreateTemp and cleanup() running in RedactedJoinSnapshot,
// rather than one still being served to a joining replica mid-request (which
// should complete in well under a second) — ut-docs#436.
const joinSnapshotOrphanAge = 5 * time.Minute

// SweepOrphanedJoinSnapshots removes join-snapshot-*.db files (and their
// -wal/-shm sidecars) left behind in the backup directory by a
// RedactedJoinSnapshot call whose cleanup() never ran, older than
// joinSnapshotOrphanAge. It never touches real backup snapshots
// (unitill-pos-*.db, backupPrefix) — a disjoint file namespace, matched by
// ListBackups/PruneBackups/ValidBackupName, never by this function. Intended
// to run once at startup, before the DB is opened (join-snapshot copies don't
// need a live connection to remove). Per-file removal errors are swallowed —
// same best-effort spirit as RedactedJoinSnapshot's own cleanup() — so one
// unremovable file can't stop the sweep or fail startup; only a failure to
// list the backup directory itself is returned. Returns the number of
// join-snapshot files removed.
func SweepOrphanedJoinSnapshots(dbPath string) (int, error) {
	dir, err := BackupDir(dbPath)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read backup dir: %w", err)
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if ok, _ := filepath.Match("join-snapshot-*.db", name); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < joinSnapshotOrphanAge {
			continue
		}
		path := filepath.Join(dir, name)
		// Sidecar removal errors stay swallowed (they're only ever present if
		// a pragma changed the copy's journal mode, same as cleanup() above),
		// but the primary .db removal must actually succeed before counting
		// this file as swept — otherwise a permission error/read-only volume
		// would still report a false "removed" count via the caller's log.
		if err := os.Remove(path); err != nil {
			continue
		}
		_ = os.Remove(path + "-wal")
		_ = os.Remove(path + "-shm")
		removed++
	}
	return removed, nil
}
