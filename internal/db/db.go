package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/universaltill/universal-till/internal/logging"
	_ "modernc.org/sqlite" // pure Go SQLite driver
)

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	// A fresh install extracts to a folder with no data/ directory, so the
	// default ./data/unitill-pos.db path can't be opened (SQLite CANTOPEN,
	// reported as "out of memory (14)"). Create the parent directory first.
	// Skip in-memory DBs used by tests (":memory:", "file::memory:...").
	if !strings.Contains(path, ":memory:") {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create data dir %q: %w", dir, err)
			}
		}
	}
	// _pragma= is how modernc.org/sqlite applies a pragma to EVERY pooled
	// connection — an Exec'd PRAGMA reaches only the one connection that
	// ran it (the old `_foreign_keys=on` was mattn syntax and silently
	// ignored, so FK enforcement was missing on pooled connections). The
	// busy timeout keeps a concurrent writer (e.g. the sync pull's apply
	// transaction) from surfacing SQLITE_BUSY to a sale mid-checkout.
	//
	// _txlock=immediate closes a lock-contention gap (ut-docs#311): in the
	// default rollback-journal mode with deferred BEGINs, a transaction
	// that reads first and then writes (the check-then-insert shape most
	// write paths here use) fails INSTANTLY with SQLITE_BUSY when another
	// connection already holds the write lock — SQLite's deadlock-avoidance
	// skips the busy handler on the SHARED→RESERVED promotion, so
	// busy_timeout never applies. _txlock=immediate makes every
	// database/sql Begin/BeginTx run BEGIN IMMEDIATE, taking the write lock
	// at BEGIN — a code path where the busy handler DOES run — so a
	// concurrent writer now waits up to busy_timeout instead of failing
	// instantly. This alone is the fix; it's not WAL-dependent (in WAL mode
	// the same read-then-write upgrade fails as SQLITE_BUSY_SNAPSHOT
	// instead, which the busy handler equally doesn't retry). (Every
	// BeginTx in internal/data is a write transaction, so applying
	// _txlock=immediate DSN-wide costs no read concurrency.)
	//
	// journal_mode(WAL) is a separate, complementary win added alongside
	// it: WAL's MVCC means readers no longer block on the writer at all.
	// Requesting WAL is safe for the ":memory:" DSNs tests use — SQLite
	// reports journal_mode=memory there instead of erroring.
	//
	// temp_store(2) = MEMORY (ut-docs#1239): CREATE TEMP TABLE is otherwise
	// backed by a file in the system temp directory, and an unrooted
	// Android app has none it can write (TMPDIR unset, /tmp absent) — the
	// first real-device boot died with SQLITE_IOERR_GETTEMPPATH (6410) in
	// migration 036, the first migration to use a temp table. In-memory
	// temp storage removes the dependency on a temp dir for every platform.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=temp_store(2)&_txlock=immediate", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Close the pool on every error path below (ut-docs#1094 review): every
	// prior return here handed back nil on failure without closing sqlDB,
	// leaking its pooled connections/file descriptors for the rest of the
	// process's life. Harmless-ish for a single failed Open, but
	// internal/app's openWithRetry (ut-docs#1094) now calls Open up to 8x in
	// a row while a fresh install's own migration race settles — each
	// failed attempt would otherwise leak its own pool of connections
	// against the very database the retry is waiting on, working against
	// the thing it's retrying for.
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = sqlDB.Close()
		}
	}()
	// Belt-and-braces for the first connection.
	if _, err := sqlDB.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return nil, fmt.Errorf("enable foreign_keys pragma: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	db := &DB{DB: sqlDB}

	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	closeOnError = false
	return db, nil
}

func (db *DB) migrate() error {
	// ensure schema_migrations table exists
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	// find current max version
	var current int
	row := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("read current schema version: %w", err)
	}

	migs, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migs {
		if m.Version <= current {
			continue
		}
		if err := db.applyMigration(m); err != nil {
			return err
		}
	}

	return nil
}

func (db *DB) applyMigration(m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", m.Version, err)
	}
	defer tx.Rollback()

	if err := execMigrationStatements(tx, m); err != nil {
		return err
	}

	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, m.Version); err != nil {
		return fmt.Errorf("record migration %d: %w", m.Version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.Version, err)
	}

	return nil
}

// addColumnStmt recognises one `ALTER TABLE [schema.]<t> ADD [COLUMN] <c> …`
// statement. It is matched against a literal-masked statement (see
// splitStatements), so text inside a quoted string can never match.
var addColumnStmt = regexp.MustCompile("(?is)^\\s*ALTER\\s+TABLE\\s+(?:[\"`]?\\w+[\"`]?\\.)?[\"`]?(\\w+)[\"`]?\\s+ADD\\s+(?:COLUMN\\s+)?[\"`]?(\\w+)[\"`]?\\b")

// onSkippedAddColumn is a test seam: called for every ADD COLUMN the runner
// skips, so a test can assert a fresh install skips nothing.
var onSkippedAddColumn func(version int, table, column string)

// execMigrationStatements runs one migration statement by statement, all
// inside the migration's transaction, and makes ADD COLUMN idempotent
// (ut-docs#1412). SQLite has CREATE TABLE/INDEX IF NOT EXISTS but no ADD
// COLUMN IF NOT EXISTS, so an additive migration that re-runs against a
// database which already carries its column dies with "duplicate column
// name" — which is what happened when a till had run a pre-merge build
// whose migration was later renumbered (078 on v0.9.0: the Android shell
// showed a white screen). 077's header states the convention that every
// migration must be re-runnable; this supplies the missing half.
//
// Each ADD COLUMN is checked against pragma_table_info immediately before
// it would execute — never up front — so a migration that rebuilds a table
// (CREATE x_new … DROP TABLE x … RENAME TO x, as 030 does) and then adds a
// column sees the rebuilt table, not the old one (independent review,
// finding 3). Everything that is not an ADD COLUMN whose column already
// exists runs unchanged; an ADD COLUMN against a table that does not exist
// still fails loudly. Statement splitting and matching are both blind to
// the contents of single-quoted literals, and `--` comments are removed
// before splitting, so neither a semicolon nor DDL-shaped text inside a
// string can confuse the runner (review findings 1, 2, 4, 5).
func execMigrationStatements(tx *sql.Tx, m migration) error {
	for _, st := range splitStatements(stripLineComments(m.SQL)) {
		if sm := addColumnStmt.FindStringSubmatch(st.masked); sm != nil {
			table, column := sm[1], sm[2]
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
				return fmt.Errorf("inspect migration %d (%s): check column %s.%s: %w", m.Version, m.Name, table, column, err)
			}
			if n > 0 {
				logging.L().Warnf("migration %d (%s): column %s.%s already exists, skipping its ADD COLUMN (ut-docs#1412)", m.Version, m.Name, table, column)
				if onSkippedAddColumn != nil {
					onSkippedAddColumn(m.Version, table, column)
				}
				continue
			}
		}
		if _, err := tx.Exec(st.text); err != nil {
			return fmt.Errorf("exec migration %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// statement is one SQL statement of a migration: text is what executes,
// masked is the same bytes with every character inside a single-quoted
// literal replaced by a space (same length, same offsets) for matching.
type statement struct {
	text, masked string
}

// splitStatements splits comment-free SQL on semicolons outside
// single-quoted literals (” inside a literal is two toggles and stays
// balanced). A final statement without a trailing semicolon is kept.
// Blank statements are dropped. No migration uses triggers or BEGIN…END
// blocks (checked 2026-09-02); if one ever does, this splitter must learn
// them first.
func splitStatements(s string) []statement {
	var out []statement
	var text, masked strings.Builder
	inQuote := false
	flush := func() {
		if strings.TrimSpace(text.String()) != "" {
			out = append(out, statement{text: text.String(), masked: masked.String()})
		}
		text.Reset()
		masked.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inQuote = !inQuote
			text.WriteByte(c)
			masked.WriteByte(c)
		case inQuote:
			text.WriteByte(c)
			masked.WriteByte(' ')
		case c == ';':
			text.WriteByte(c)
			masked.WriteByte(c)
			flush()
		default:
			text.WriteByte(c)
			masked.WriteByte(c)
		}
	}
	flush()
	return out
}

// stripLineComments removes `-- …` comments outside single-quoted strings.
// (No shipped migration carries `--` inside a literal today; the quote
// tracking is there so one safely can.)
func stripLineComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inQuote = !inQuote
			b.WriteByte(c)
		case !inQuote && c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
