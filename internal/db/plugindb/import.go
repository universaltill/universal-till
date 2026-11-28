package plugindb

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var sqliteHeader = []byte("SQLite format 3\x00")

// ImportDB is a read-only, immutable view of a staged SQLite file a user
// handed the plugin (db_open_import). There is no write path.
type ImportDB struct {
	db   *sql.DB
	conn *sql.Conn
	sem  chan struct{}
}

// OpenImport opens path read-only and immutable (mode=ro&immutable=1,
// query_only, trusted_schema=0, ATTACH limit 0, the same statement limits as
// a Store). path must come from the host's own import-handle registry, never
// from the guest. A file without the SQLite header is ErrNotSQLite.
func OpenImport(ctx context.Context, path string) (*ImportDB, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if ok, err := regularOrMissing(p); err != nil || !ok {
		if err == nil {
			err = os.ErrNotExist
		}
		return nil, fmt.Errorf("plugindb: import file: %w", err)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, len(sqliteHeader))
	_, rerr := io.ReadFull(f, hdr)
	_ = f.Close()
	if rerr != nil || !bytes.Equal(hdr, sqliteHeader) {
		return nil, ErrNotSQLite
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1&_pragma=query_only(1)&_pragma=trusted_schema(0)&_pragma=temp_store(2)&_pragma=busy_timeout(1000)", escapePath(p))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	d := &ImportDB{db: db, conn: conn, sem: make(chan struct{}, 1)}
	fail := func(err error) (*ImportDB, error) { _ = d.Close(); return nil, err }
	if err := setLimits(conn, limitSQLBytes, limitValueBytes); err != nil {
		return fail(err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA temp.max_page_count=%d`, 2048)); err != nil {
		return fail(err)
	}
	// Force the header/schema to be read now so a corrupt file fails here.
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrNotSQLite, err))
	}
	return d, nil
}

// Query runs one read statement; the result rules are Store.Query's.
func (d *ImportDB) Query(ctx context.Context, q string, args ...any) ([]byte, error) {
	if err := checkStatement(q); err != nil {
		return nil, err
	}
	t := time.NewTimer(DefaultBusyWait)
	defer t.Stop()
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, ErrBusy
	}
	defer func() { <-d.sem }()
	if d.conn == nil {
		return nil, ErrClosed
	}
	return queryRows(ctx, d.conn, q, args)
}

// Close releases the file. Idempotent.
func (d *ImportDB) Close() error {
	d.sem <- struct{}{}
	defer func() { <-d.sem }()
	if d.db == nil {
		return nil
	}
	if d.conn != nil {
		_ = d.conn.Close()
		d.conn = nil
	}
	err := d.db.Close()
	d.db = nil
	return err
}
