package plugindb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Migration is one plugin-owned schema step (ADR-0121 §4). Versions are
// contiguous from 1; the checksum is the SHA-256 of the exact SQL text and is
// pinned in the plugin's _ut_migrations table the first time it is applied.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Checksum is the hex SHA-256 of the migration's SQL text.
func (m Migration) Checksum() string {
	sum := sha256.Sum256([]byte(m.SQL))
	return hex.EncodeToString(sum[:])
}

var (
	// ErrMigrationTampered: an already-applied migration's text no longer
	// matches the checksum recorded when it ran. Fails closed.
	ErrMigrationTampered = errors.New("plugindb: an applied migration was modified")
	// ErrMigrationBehind: the database has applied versions the supplied
	// artifact does not carry (a downgrade). Refused.
	ErrMigrationBehind = errors.New("plugindb: database is newer than the plugin's migrations")
	// ErrMigrationSet: the supplied set is not contiguous from 1.
	ErrMigrationSet = errors.New("plugindb: migrations must be numbered 1..N with no gap or duplicate")
)

var migrationFile = regexp.MustCompile(`^([0-9]{4})_([a-z0-9][a-z0-9_-]{0,63})\.sql$`)

// maxMigrationBytes bounds one migration file read from an artifact.
const maxMigrationBytes = 256 << 10

// LoadMigrations reads NNNN_name.sql files from dir in fsys (the signed
// artifact's db/migrations). Anything that is not a matching regular file is
// an error rather than silently skipped, so a typo cannot drop a migration.
func LoadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("plugindb: read migrations dir: %w", err)
	}
	var out []Migration
	for _, e := range entries {
		m := migrationFile.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			return nil, fmt.Errorf("plugindb: %q is not a NNNN_name.sql migration file", e.Name())
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("plugindb: read %s: %w", e.Name(), err)
		}
		if len(b) > maxMigrationBytes {
			return nil, fmt.Errorf("plugindb: migration %s exceeds %d bytes", e.Name(), maxMigrationBytes)
		}
		v, _ := strconv.Atoi(m[1])
		out = append(out, Migration{Version: v, Name: m[2], SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, validateSet(out)
}

func validateSet(ms []Migration) error {
	for i, m := range ms {
		if m.Version != i+1 {
			return fmt.Errorf("%w (found version %d at position %d)", ErrMigrationSet, m.Version, i+1)
		}
		if strings.TrimSpace(m.SQL) == "" {
			return fmt.Errorf("%w: migration %d is empty", ErrMigrationSet, m.Version)
		}
	}
	return nil
}

// migrate applies every pending migration in ONE immediate transaction, so a
// failure at any step leaves the database exactly as it was (SQLite DDL is
// transactional). Re-running with nothing pending is a no-op.
func migrate(ctx context.Context, c *sql.Conn, ms []Migration, now time.Time) (err error) {
	if err := validateSet(ms); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _ut_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("plugindb: create _ut_migrations: %w", err)
	}
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	rows, err := tx.QueryContext(ctx, `SELECT version, checksum FROM _ut_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	applied := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err = rows.Scan(&v, &sum); err != nil {
			_ = rows.Close()
			return err
		}
		applied[v] = sum
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for v, sum := range applied {
		if v > len(ms) {
			return fmt.Errorf("%w: applied version %d, artifact carries %d", ErrMigrationBehind, v, len(ms))
		}
		if ms[v-1].Checksum() != sum {
			return fmt.Errorf("%w: version %d (%s)", ErrMigrationTampered, v, ms[v-1].Name)
		}
	}
	for _, m := range ms {
		if _, done := applied[m.Version]; done {
			continue
		}
		// Statement by statement, each one checked: a migration never runs
		// PRAGMA/ATTACH/VACUUM or its own COMMIT (guard.go).
		var stmts []string
		if stmts, err = splitMigration(m.SQL); err != nil || len(stmts) == 0 {
			if err == nil {
				err = ErrMigrationSet
			}
			return fmt.Errorf("plugindb: migration %d (%s): %w", m.Version, m.Name, err)
		}
		for _, st := range stmts {
			if _, err = tx.ExecContext(ctx, st); err != nil {
				return fmt.Errorf("plugindb: migration %d (%s): %w", m.Version, m.Name, mapErr(err))
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO _ut_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			m.Version, m.Name, m.Checksum(), now.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
