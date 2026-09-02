package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
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

// ErrDatabasePredatesReset is returned (wrapped) by Open when the database
// carries schema_migrations rows but no schema_lineage marker: it was
// created by the pre-2026-09 78-migration ledger, and there is no supported
// path from that schema to the squashed baseline (ADR-0074 Decision 3). The
// message is deliberately plain and actionable — it is what the operator
// sees.
var ErrDatabasePredatesReset = errors.New("database predates the schema reset — delete the data directory and start again")

// idempotentRerunVersions lists migration versions that may be re-applied
// in place when their on-disk name/checksum no longer matches what the
// ledger recorded, instead of hard-failing boot. ADR-0074 Decision 3 leaves
// that per-file determination to the build sub-card (ut-docs#1425); none is
// currently known to need it, so this starts — and should stay — empty
// unless a specific file is proven idempotent and reviewed as such. A
// listed version's SQL re-runs through execMigrationStatements against the
// live schema, so it must be safe to execute twice.
var idempotentRerunVersions = map[int]bool{}

// migrationChecksum is the value recorded beside each applied migration's
// version and name, and compared against the on-disk file on every boot.
// Computed over the comment-stripped text with trailing whitespace and
// blank lines removed (stripLineComments leaves a bare newline where each
// comment was), so adding, editing or removing a `--` comment in an
// already-applied file is not drift; changing a statement is.
func migrationChecksum(sqlText string) string {
	var b strings.Builder
	for _, line := range strings.Split(stripLineComments(sqlText), "\n") {
		if line = strings.TrimRight(line, " \t\r"); line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func (db *DB) migrate() error {
	// ensure schema_migrations table exists. name + checksum (ut-docs#1425,
	// ADR-0074 Decision 3) let verifyAppliedMigrations catch a file renamed
	// or edited under an already-applied version number. No ALTER TABLE
	// back-compat path is needed for the two newer columns: every database
	// created before they existed is rejected by checkSchemaLineage below
	// before anything reads them.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			name       TEXT NOT NULL DEFAULT '',
			checksum   TEXT NOT NULL DEFAULT ''
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

	// A ledger with versions but no lineage marker is a pre-reset database.
	// The only remaining migration is version 1, which would otherwise be
	// silently skipped (1 <= any old watermark) — exactly the misread
	// ADR-0074 exists to prevent. Refuse before touching anything.
	if current > 0 {
		if err := db.checkSchemaLineage(); err != nil {
			return err
		}
	}

	migs, err := loadMigrations()
	if err != nil {
		return err
	}

	if err := db.verifyAppliedMigrations(migs, current); err != nil {
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

// checkSchemaLineage returns ErrDatabasePredatesReset unless the
// schema_lineage marker row written by 001_init.sql is present.
func (db *DB) checkSchemaLineage() error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_lineage'`).Scan(&n); err != nil {
		return fmt.Errorf("check schema lineage: %w", err)
	}
	if n == 0 {
		return ErrDatabasePredatesReset
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_lineage`).Scan(&n); err != nil {
		return fmt.Errorf("check schema lineage: %w", err)
	}
	if n == 0 {
		return ErrDatabasePredatesReset
	}
	return nil
}

// verifyAppliedMigrations compares every on-disk migration at or below the
// ledger watermark against what the ledger recorded when it was applied
// (ut-docs#1412 found a real device whose ledger had drifted from the file
// sequence; ADR-0074 Decision 3). A version the ledger says is applied must
// still be on disk under the same name with the same statements; a file
// renumbered under an already-applied version (so it never ran, and never
// will — the watermark skips it) has no ledger row at all. Either way boot
// fails loudly rather than silently skipping a step — unless the version is
// in idempotentRerunVersions, in which case the file is re-applied in place.
//
// This runs both directions (ut-docs#1425 review finding F2): the loop below
// catches a file present on disk with no ledger row (downward renumbering —
// a version slips below an existing watermark). The mirror case — a ledger
// row exists but no on-disk file has that version anymore, because a file
// was renumbered UPWARD out from under an applied version — is invisible to
// a files-first loop: the renumbered file's new version is simply above the
// watermark and gets silently applied as "new", while the orphaned ledger
// row is never inspected. loadedVersions + the second loop below closes that
// gap by walking the ledger itself for every row <= current.
func (db *DB) verifyAppliedMigrations(migs []migration, current int) error {
	loadedVersions := make(map[int]bool, len(migs))
	for _, m := range migs {
		loadedVersions[m.Version] = true
		if m.Version > current {
			continue
		}
		var name, checksum string
		err := db.QueryRow(`SELECT name, checksum FROM schema_migrations WHERE version = ?`, m.Version).Scan(&name, &checksum)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("migration %d (%s) is below the applied watermark %d but was never recorded as applied — a migration file was renumbered under an already-applied version; delete the data directory and start again (ADR-0074)", m.Version, m.Name, current)
		}
		if err != nil {
			return fmt.Errorf("read ledger row for migration %d: %w", m.Version, err)
		}
		want := migrationChecksum(m.SQL)
		if name == m.Name && checksum == want {
			continue
		}
		if !idempotentRerunVersions[m.Version] {
			return fmt.Errorf("migration %d: recorded as %q (checksum %s) but on-disk file is %q (checksum %s) — a migration file was renamed or edited after being applied; delete the data directory and start again (ADR-0074)", m.Version, name, checksum, m.Name, want)
		}
		logging.L().Warnf("migration %d: recorded as %q (checksum %s) but on-disk file is %q (checksum %s); version is allowlisted as idempotent, re-applying in place (ADR-0074)", m.Version, name, checksum, m.Name, want)
		if err := db.reapplyMigration(m); err != nil {
			return err
		}
	}

	rows, err := db.Query(`SELECT version, name FROM schema_migrations WHERE version <= ?`, current)
	if err != nil {
		return fmt.Errorf("read ledger versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return fmt.Errorf("scan ledger version: %w", err)
		}
		if !loadedVersions[version] {
			return fmt.Errorf("migration %d (%s) is recorded as applied but no on-disk file carries that version anymore — it was likely renumbered to a different version; delete the data directory and start again (ADR-0074)", version, name)
		}
	}
	return rows.Err()
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

	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, checksum) VALUES (?, ?, ?)`, m.Version, m.Name, migrationChecksum(m.SQL)); err != nil {
		return fmt.Errorf("record migration %d: %w", m.Version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.Version, err)
	}

	return nil
}

// reapplyMigration re-runs an already-recorded, allowlisted migration's
// statements in place and refreshes its ledger name/checksum, all in one
// transaction.
func (db *DB) reapplyMigration(m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin re-apply migration %d: %w", m.Version, err)
	}
	defer tx.Rollback()

	if err := execMigrationStatements(tx, m); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE schema_migrations SET name = ?, checksum = ? WHERE version = ?`, m.Name, migrationChecksum(m.SQL), m.Version); err != nil {
		return fmt.Errorf("re-record migration %d: %w", m.Version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit re-apply migration %d: %w", m.Version, err)
	}

	return nil
}

// BaselineStatementsFor returns every statement of the embedded 001_init.sql
// baseline whose target is table — its CREATE TABLE, its CREATE INDEXes and
// its seed INSERTs — split by the same splitter the migration runner uses.
// It exists for fixtures in other packages that hand-roll a partial schema
// but need one table exactly as production has it (internal/pages'
// seedCountrySettingsTable, which used to execute the real 041/073 files for
// that reason): reading the real baseline can't drift, because it IS the
// schema. Test-support only; nothing at runtime calls it.
func BaselineStatementsFor(table string) ([]string, error) {
	migs, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	if len(migs) == 0 || migs[0].Version != 1 {
		return nil, fmt.Errorf("baseline migration 001 not found")
	}
	q := regexp.QuoteMeta(table)
	target := regexp.MustCompile(`(?is)^\s*(?:CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?|CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?\S+\s+ON\s+|INSERT\s+(?:OR\s+\w+\s+)?INTO\s+)["` + "`" + `]?` + q + `["` + "`" + `]?\b`)
	var out []string
	for _, st := range splitStatements(stripLineComments(migs[0].SQL)) {
		if target.MatchString(st.masked) {
			out = append(out, strings.TrimSpace(st.text))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("baseline has no statement targeting table %q", table)
	}
	return out, nil
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
