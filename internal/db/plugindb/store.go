package plugindb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
)

var (
	// ErrQuota: the write would grow the database past its storage quota.
	ErrQuota = errors.New("plugindb: storage quota exceeded")
	// ErrResultTooLarge: a Query result is over MaxResultBytes; page with LIMIT.
	ErrResultTooLarge = errors.New("plugindb: result larger than 1 MiB; page with LIMIT")
	// ErrClosed: the Store was closed (unload, uninstall).
	ErrClosed = errors.New("plugindb: store closed")
)

// Options configure Manager.Open.
type Options struct {
	// QuotaBytes is the database size cap; 0 means DefaultQuotaBytes.
	QuotaBytes int64
	// Migrations are the plugin's artifact migrations, applied on Open.
	Migrations []Migration
}

// Store is one plugin's hardened database. Safe for concurrent use; calls are
// serialised on the single pinned connection.
type Store struct {
	id   string
	path string
	mu   sync.Mutex
	db   *sql.DB
	conn *sql.Conn
}

func openStore(ctx context.Context, id, dir string, o Options, now time.Time) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("plugindb: create %s: %w", dir, err)
	}
	p := filepath.Join(dir, DBFileName)
	// Create the file ourselves and open mode=rw so the pool can never
	// conjure a database elsewhere (same as core, ut-docs#5049).
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("plugindb: create database: %w", err)
	}
	_ = f.Close()

	dsn := fmt.Sprintf("file:%s?mode=rw&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
		"&_pragma=temp_store(2)&_pragma=secure_delete(1)&_pragma=trusted_schema(0)&_pragma=journal_size_limit(%d)&_txlock=immediate",
		strings.NewReplacer("%", "%25", "#", "%23", "?", "%3f").Replace(p), journalSizeLimit)
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
	s := &Store{id: id, path: p, db: db, conn: conn}
	fail := func(err error) (*Store, error) { _ = s.Close(); return nil, err }

	if _, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_ATTACHED, 0); err != nil {
		return fail(fmt.Errorf("plugindb: disable ATTACH: %w", err))
	}
	quota := o.QuotaBytes
	if quota <= 0 {
		quota = DefaultQuotaBytes
	}
	var pageSize int64
	if err := conn.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil || pageSize <= 0 {
		return fail(fmt.Errorf("plugindb: page size: %v", err))
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA max_page_count=%d`, max(quota/pageSize, 1))); err != nil {
		return fail(err)
	}
	// The TEMP schema is a second database held in memory (temp_store=2) that
	// max_page_count on main does not cover; without its own cap a plugin
	// could fill host memory with a temp table.
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA temp.max_page_count=%d`, max(min(quota, tempQuotaBytes)/pageSize, 1))); err != nil {
		return fail(err)
	}
	if err := migrate(ctx, conn, o.Migrations, now); err != nil {
		return fail(err)
	}
	return s, nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_FULL {
		return fmt.Errorf("%w: %v", ErrQuota, err)
	}
	return err
}

// Exec runs one write statement and returns the rows affected.
func (s *Store) Exec(ctx context.Context, q string, args ...any) (int64, error) {
	if err := checkStatement(q); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return 0, ErrClosed
	}
	res, err := s.conn.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// Query runs one statement and returns the rows as a JSON array of objects,
// refusing a result over MaxResultBytes.
func (s *Store) Query(ctx context.Context, q string, args ...any) ([]byte, error) {
	if err := checkStatement(q); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil, ErrClosed
	}
	rows, err := s.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
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
			return nil, err
		}
		obj := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
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

// Path is the database file (for the host's own backup/export code).
func (s *Store) Path() string { return s.path }

// Close checkpoints the WAL and releases the connection. Idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
