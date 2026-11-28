package plugindb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SnapshotFile writes a consistent, compact copy of the SQLite file srcDB to
// dest with VACUUM INTO on the host's own short-lived connection. It is the
// only supported way to copy a live WAL database: a raw file copy can miss
// the -wal file. dest must not exist; it is created 0600.
func SnapshotFile(ctx context.Context, srcDB, dest string) error {
	src, err := filepath.Abs(srcDB)
	if err != nil {
		return err
	}
	dst, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if ok, err := regularOrMissing(src); err != nil || !ok {
		if err == nil {
			err = os.ErrNotExist
		}
		return fmt.Errorf("plugindb: snapshot source: %w", err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("plugindb: snapshot destination exists: %s", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=rw&_pragma=busy_timeout(5000)&_pragma=trusted_schema(0)", escapePath(src)))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// Start from a short WAL: a checkpoint before the snapshot reader pins it
	// (best effort; a busy writer just leaves it as it is). While the read
	// transaction below runs, no checkpoint can pass it, so the run is bounded
	// by ctx (the caller's deadline) rather than left open-ended.
	if rows, err := db.QueryContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err == nil {
		_ = rows.Close()
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("plugindb: snapshot: %w", err)
	}
	return os.Chmod(dst, 0o600)
}
