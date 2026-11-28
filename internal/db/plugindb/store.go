package plugindb

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	// DefaultQuotaBytes is ADR-0121's limits.storage_mb default (50 MB).
	DefaultQuotaBytes int64 = 50 << 20
	// MaxResultBytes bounds one Query result (ADR-0121 §3: 1 MiB per call).
	MaxResultBytes = 1 << 20
	// journalSizeLimit keeps the WAL bounded (ADR-0121 §4: 4 MiB).
	journalSizeLimit = 4 << 20
	// tempQuotaBytes caps the in-memory TEMP schema (temp tables).
	tempQuotaBytes int64 = 8 << 20
	// DBFileName is the per-plugin database file name.
	DBFileName = "plugin.db"

	// Per-connection SQLite limits for plugin statements (design §3/§6.5).
	limitValueBytes = 1 << 20  // SQLITE_LIMIT_LENGTH: one string/BLOB/row
	limitSQLBytes   = 64 << 10 // SQLITE_LIMIT_SQL_LENGTH
	limitVariables  = 64       // SQLITE_LIMIT_VARIABLE_NUMBER
	limitCompound   = 20       // SQLITE_LIMIT_COMPOUND_SELECT
	// Migrations are signed artifact text, not guest input: they get a
	// wider window while they run.
	migrationSQLBytes   = maxMigrationBytes + 1<<10
	migrationValueBytes = 16 << 20

	// DefaultBusyWait is how long a caller waits for the single connection.
	// It is deliberately longer than the host's per-read cap (plugins
	// dbQueryCap, 3 s), so one long report query can never make a sale-path
	// write fail with ErrBusy: the write outwaits the read, which the host
	// interrupts first. (ut-docs#3157 performance MUST-FIX 3.)
	DefaultBusyWait = 5 * time.Second
	maxOpenStores   = 16
)

var (
	// ErrQuota: the write would grow the database past its storage quota.
	ErrQuota = errors.New("plugindb: storage quota exceeded")
	// ErrResultTooLarge: a Query result is over MaxResultBytes; page with LIMIT.
	ErrResultTooLarge = errors.New("plugindb: result larger than 1 MiB; page with LIMIT")
	// ErrClosed: the Store was closed (unload, uninstall).
	ErrClosed = errors.New("plugindb: store closed")
	// ErrBusy: the single connection stayed busy beyond Options.BusyWait.
	ErrBusy = errors.New("plugindb: database busy")
	// ErrSymlink: a path that must be a real file or directory is a link (or
	// resolves elsewhere); refused so a plugin cannot point the host at
	// another file.
	ErrSymlink = errors.New("plugindb: path is a symlink or not a regular file")
	// ErrNoPreUpgrade: no pre-upgrade copy exists for the requested version.
	ErrNoPreUpgrade = errors.New("plugindb: no pre-upgrade copy for that version")
	// ErrNotSQLite: the file does not start with the SQLite header.
	ErrNotSQLite = errors.New("plugindb: not a SQLite database file")
)

// Options configure Manager.Open.
type Options struct {
	// QuotaBytes is the database size cap; 0 means DefaultQuotaBytes.
	QuotaBytes int64
	// Migrations are the plugin's artifact migrations, applied on Open.
	Migrations []Migration
	// NoMigrate opens without touching _ut_migrations (the installer migrates;
	// the runtime only uses the database).
	NoMigrate bool
	// BusyWait is how long a caller waits for the single connection before
	// ErrBusy; 0 means 5 s.
	BusyWait time.Duration
}

// Store is one plugin's hardened database. Safe for concurrent use; calls are
// serialised on the single pinned connection through a semaphore, so a caller
// that cannot get the connection in BusyWait gets ErrBusy instead of hanging.
type Store struct {
	id       string
	path     string
	sem      chan struct{}
	busyWait atomic.Int64 // nanoseconds
	lastUsed atomic.Int64 // unix nanoseconds
	quota    atomic.Int64 // the quota last applied to the connection
	db       *sql.DB
	conn     *sql.Conn
}

func (s *Store) touch() { s.lastUsed.Store(time.Now().UnixNano()) }

// acquire takes the connection. The returned func releases it.
func (s *Store) acquire(ctx context.Context) (func(), error) {
	wait := time.Duration(s.busyWait.Load())
	if wait <= 0 {
		wait = DefaultBusyWait
	}
	select {
	case s.sem <- struct{}{}:
	default:
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			return nil, ErrBusy
		}
	}
	if s.conn == nil {
		<-s.sem
		return nil, ErrClosed
	}
	s.touch()
	return func() { s.touch(); <-s.sem }, nil
}

func (s *Store) closed() bool {
	select {
	case s.sem <- struct{}{}:
		c := s.conn == nil
		<-s.sem
		return c
	default:
		return false // in use, so open
	}
}

// pageSize reads the database page size.
func pageSize(ctx context.Context, c *sql.Conn) (int64, error) {
	var ps int64
	if err := c.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&ps); err != nil || ps <= 0 {
		return 0, fmt.Errorf("plugindb: page size: %v", err)
	}
	return ps, nil
}

func effectiveQuota(q int64) int64 {
	if q <= 0 {
		return DefaultQuotaBytes
	}
	return q
}

// applyQuota (re)sets the size caps for the connection.
func applyQuota(ctx context.Context, c *sql.Conn, quota int64) error {
	quota = effectiveQuota(quota)
	ps, err := pageSize(ctx, c)
	if err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, fmt.Sprintf(`PRAGMA max_page_count=%d`, max(quota/ps, 1))); err != nil {
		return err
	}
	// The TEMP schema is a second database held in memory (temp_store=2) that
	// max_page_count on main does not cover; without its own cap a plugin
	// could fill host memory with a temp table.
	_, err = c.ExecContext(ctx, fmt.Sprintf(`PRAGMA temp.max_page_count=%d`, max(min(quota, tempQuotaBytes)/ps, 1)))
	return err
}

// setLimits applies the per-connection statement limits.
func setLimits(c *sql.Conn, sqlBytes, valueBytes int) error {
	for _, l := range []struct{ id, v int }{
		{sqlite3.SQLITE_LIMIT_ATTACHED, 0},
		{sqlite3.SQLITE_LIMIT_LENGTH, valueBytes},
		{sqlite3.SQLITE_LIMIT_SQL_LENGTH, sqlBytes},
		{sqlite3.SQLITE_LIMIT_VARIABLE_NUMBER, limitVariables},
		{sqlite3.SQLITE_LIMIT_COMPOUND_SELECT, limitCompound},
	} {
		if _, err := sqlite.Limit(c, l.id, l.v); err != nil {
			return fmt.Errorf("plugindb: set limit %d: %w", l.id, err)
		}
	}
	return nil
}

func escapePath(p string) string {
	return strings.NewReplacer("%", "%25", "#", "%23", "?", "%3f").Replace(p)
}

// openFileChecked creates/opens the database file and proves, after opening,
// that the path is the very regular file that was opened (defeats a symlink
// swapped in between the check and the open).
func openFileChecked(p string) error {
	if _, err := regularOrMissing(p); err != nil {
		return err
	}
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := regularOrMissing(p + side); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("plugindb: create database: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	li, err := os.Lstat(p)
	if err != nil || !li.Mode().IsRegular() || !os.SameFile(fi, li) {
		return ErrSymlink
	}
	return nil
}

// openStore opens (and optionally migrates) dir/plugin.db. dir must already be
// a vetted real directory (Manager.safeDir).
func openStore(ctx context.Context, id, dir string, o Options, now time.Time) (*Store, error) {
	p := filepath.Join(dir, DBFileName)
	// synchronous=NORMAL (1): in WAL mode this can never corrupt the database
	// (the WAL is still fsynced at each checkpoint); the only cost is that the
	// last few commits may be lost on a power cut, and the database is never
	// left inconsistent. FULL would fsync the WAL on every autocommit
	// db_exec, 5-30 ms each on an SD card, on the sale path. temp_store=file
	// (0): sorter/temp b-trees for ORDER BY / GROUP BY spill to disk rather
	// than growing host memory without bound on a big table (the TEMP
	// schema is still capped by temp.max_page_count).
	//
	// The one pinned connection is kept (a second read connection would need
	// the per-connection hardening hook on every pooled connection; the
	// guarantee is worth more than the concurrency). The write-starvation
	// risk is closed by timing instead: reads are capped below BusyWait.
	// Create the file ourselves and open mode=rw so the pool can never
	// conjure a database elsewhere (same as core, ut-docs#5049).
	if err := openFileChecked(p); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?mode=rw&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
		"&_pragma=synchronous(1)&_pragma=temp_store(0)&_pragma=secure_delete(1)&_pragma=trusted_schema(0)&_pragma=journal_size_limit(%d)&_txlock=immediate",
		escapePath(p), journalSizeLimit)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection, pinned: the hardening below is per connection, so a
	// second pooled connection would silently lack it.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{id: id, path: p, db: db, conn: conn, sem: make(chan struct{}, 1)}
	s.busyWait.Store(int64(o.BusyWait))
	s.touch()
	fail := func(err error) (*Store, error) { _ = s.Close(); return nil, err }

	if err := applyQuota(ctx, conn, o.QuotaBytes); err != nil {
		return fail(err)
	}
	s.quota.Store(effectiveQuota(o.QuotaBytes))
	if !o.NoMigrate {
		if err := s.migrate(ctx, o.Migrations, now); err != nil {
			return fail(err)
		}
	}
	if err := setLimits(conn, limitSQLBytes, limitValueBytes); err != nil {
		return fail(err)
	}
	return s, nil
}

// migrate runs migrations with the wider artifact limits, then restores the
// guest limits. The caller must not hold the connection.
func (s *Store) migrate(ctx context.Context, ms []Migration, now time.Time) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := setLimits(s.conn, migrationSQLBytes, migrationValueBytes); err != nil {
		return err
	}
	defer func() { _ = setLimits(s.conn, limitSQLBytes, limitValueBytes) }()
	return migrate(ctx, s.conn, ms, now)
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_FULL:
			return fmt.Errorf("%w: %v", ErrQuota, err)
		case sqlite3.SQLITE_TOOBIG:
			return fmt.Errorf("%w: %v", ErrResultTooLarge, err)
		}
	}
	return err
}

// Exec runs one write statement and returns the rows affected.
func (s *Store) Exec(ctx context.Context, q string, args ...any) (int64, error) {
	if err := checkStatement(q); err != nil {
		return 0, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	res, err := s.conn.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// Query runs one statement and returns the rows as a JSON array of objects,
// refusing a result over MaxResultBytes. A BLOB value is {"b64":"..."}.
func (s *Store) Query(ctx context.Context, q string, args ...any) ([]byte, error) {
	if err := checkStatement(q); err != nil {
		return nil, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return queryRows(ctx, s.conn, q, args)
}

func queryRows(ctx context.Context, c *sql.Conn, q string, args []any) ([]byte, error) {
	rows, err := c.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, mapErr(err)
	}
	out := []byte{'['}
	first := true
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, mapErr(err)
		}
		obj := make(map[string]any, len(cols))
		size := 0
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				size += len(v)*4/3 + 4
				vals[i] = map[string]string{"b64": base64.StdEncoding.EncodeToString(v)}
			case string:
				size += len(v)
			}
			if size > MaxResultBytes {
				return nil, ErrResultTooLarge
			}
			obj[c] = vals[i]
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		if len(out)+len(b)+2 > MaxResultBytes {
			return nil, ErrResultTooLarge
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, b...)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return append(out, ']'), nil
}

// Checkpoint runs a PASSIVE WAL checkpoint (never blocks readers/writers);
// the host calls it once per event that used the database.
func (s *Store) Checkpoint(ctx context.Context) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	rows, err := s.conn.QueryContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`)
	if err != nil {
		return err
	}
	return rows.Close()
}

// Close checkpoints the WAL and releases the connection. Idempotent.
func (s *Store) Close() error {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	return s.closeHeld()
}

// claim takes the connection for closing without waiting. true means the
// caller now owns the semaphore and must call closeClaimed (which closes and
// keeps it held, so any late caller sees ErrClosed).
func (s *Store) claim() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// closeClaimed closes a claimed store and releases its semaphore.
func (s *Store) closeClaimed() error {
	defer func() { <-s.sem }()
	return s.closeHeld()
}

func (s *Store) closeHeld() error {
	if s.db == nil {
		return nil
	}
	if s.conn != nil {
		_, _ = s.conn.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
		_ = s.conn.Close()
		s.conn = nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
