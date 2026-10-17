package data

import (
	"database/sql"
	"database/sql/driver"
	"errors"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsStorageError reports whether err is the database failing rather than
// the data being refused: a savepoint statement failing (ErrSavepoint) or
// a dead connection or transaction (driver.ErrBadConn, sql.ErrTxDone,
// sql.ErrConnDone), or any SQLite driver error except a constraint violation (a constraint is
// the caller's input — a duplicate SKU, a missing parent — not storage).
// A caller applying rows it did not author (the cloud catalogue pull,
// ut-docs#4463) aborts and retries on a storage error instead of recording
// the row as refused.
//
// It needs the driver error in the chain (wrapped with %w): one a
// repository flattened with %v reads as not a storage error.
func IsStorageError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSavepoint) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, sql.ErrTxDone) || errors.Is(err, sql.ErrConnDone) {
		return true
	}
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff != sqlite3.SQLITE_CONSTRAINT
}
