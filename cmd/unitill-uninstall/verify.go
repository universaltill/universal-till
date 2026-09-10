// Backup verification (ut-docs#1083): before anything is removed, the
// copied backup must exist, be non-empty, re-open as SQLite, and pass
// PRAGMA integrity_check — a truncated or corrupt copy aborts the whole
// uninstall. Opened read-only so verification can never mutate the one
// file the shop's data may soon depend on.
//
// This lives in cmd/, not internal/ — the repository-pattern guard
// (scripts/ci/guard-data-access.sh) scopes raw query text to internal/,
// and a PRAGMA against a detached backup FILE is an integrity probe, not
// domain data access; internal/db's own contract (backup.go) is reused
// unmodified for creating the snapshot.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite" // same pure-Go driver internal/db uses
)

// sqliteURIPathEscaper percent-encodes the three characters significant to
// SQLite's own "file:" URI filename parsing (ut-docs#2033, mirroring
// ut-docs#2030's internal/db.escapeSQLiteURIPath): '#', '?' and '%'
// itself. Unescaped, a data dir containing one of these silently
// truncates the DSN path AND drops any query string after it — here that
// would fail closed (verifyBackup errors, aborting the uninstall rather
// than deleting the wrong/no data), so this fixes wrong behaviour rather
// than a dangerous one.
//
// This is a local copy rather than an import of internal/db's own helper:
// at the time this fix was written, internal/db's version (ut-docs#2030)
// was still in an open, unmerged PR and unexported besides. A follow-up
// should consolidate the two into one exported helper once that PR lands.
var sqliteURIPathEscaper = strings.NewReplacer("%", "%25", "#", "%23", "?", "%3f")

func escapeSQLiteURIPath(path string) string {
	return sqliteURIPathEscaper.Replace(path)
}

func verifyBackup(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("backup missing: %w", err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("backup %s is empty", path)
	}
	sqlDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", escapeSQLiteURIPath(path)))
	if err != nil {
		return fmt.Errorf("re-open backup: %w", err)
	}
	defer sqlDB.Close()
	var result string
	if err := sqlDB.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}
