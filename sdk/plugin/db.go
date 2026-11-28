package plugin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Own database (permission "db:own", manifest `db` block; ADR-0121 §4).
// Every plugin has one private SQLite file; the host picks it from the
// plugin id, so nothing here names a file. One statement per call: ATTACH,
// PRAGMA, VACUUM, transaction control and multi-statement strings are
// refused (ErrInvalid), as is a result over 1 MiB. A refusal's reason is
// readable with DBError, and is appended to the returned error's text.

// dbMaxResult is the host's db_query result cap (plugindb.MaxResultBytes).
const dbMaxResult = 1 << 20

// dbBuf is the first result buffer; a larger result is retried once at the
// reported size (the host replays the cached result, ADR-0121 F4).
const dbBuf = 64 << 10

// DBBlob is a BLOB statement argument; query results return one as
// {"b64":"..."}.
type DBBlob []byte

// MarshalJSON encodes the host's BLOB argument form {"b64":"..."}.
func (b DBBlob) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"b64": base64.StdEncoding.EncodeToString(b)})
}

// encodeDBArgs is the JSON array the host decodes: nil, bool, string,
// integers, floats and DBBlob only (nothing nested). No args is no bytes.
func encodeDBArgs(args []any) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}
	for _, a := range args {
		switch a.(type) {
		case nil, bool, string, DBBlob, int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64, float32, float64:
		default:
			return nil, fmt.Errorf("ut db: argument type %T: %w", a, ErrInvalid)
		}
	}
	return json.Marshal(args)
}

// dbErr adds db_error's text (the last failure of this event) to a failed
// call; the code still matches errors.Is(err, ErrInvalid) etc.
func dbErr(err error) error {
	var e *Error
	if err == nil || !errors.As(err, &e) {
		return err
	}
	buf := make([]byte, 512)
	n := rawDBError(buf)
	if n <= 0 {
		return err
	}
	if int(n) > len(buf) {
		n = int32(len(buf))
	}
	return fmt.Errorf("%w: %s", err, buf[:n])
}

// DBError is the host's text for the last failed db call of this event.
func DBError() string {
	buf := make([]byte, 512)
	n := rawDBError(buf)
	if n <= 0 {
		return ""
	}
	if int(n) > len(buf) {
		n = int32(len(buf))
	}
	return string(buf[:n])
}

// DBExec runs one write statement and returns the rows it changed. Errors:
// ErrDenied (no db:own / no db block), ErrInvalid (refused statement, bad
// SQL, constraint failure), ErrQuota (database full), ErrBusy, ErrInternal
// (timeout). Writes are never retried.
func DBExec(sql string, args ...any) (int64, error) {
	a, err := encodeDBArgs(args)
	if err != nil {
		return 0, err
	}
	n := rawDBExec([]byte(sql), a)
	if n < 0 {
		return 0, dbErr(codeErr("db_exec", int32(n)))
	}
	return n, nil
}

// DBQuery runs one statement and returns its rows as a JSON array of
// objects (column name -> value; a BLOB is {"b64":"..."}). Use
// DBQueryInto to decode it.
func DBQuery(sql string, args ...any) ([]byte, error) {
	a, err := encodeDBArgs(args)
	if err != nil {
		return nil, err
	}
	s := []byte(sql)
	out, err := bufCall("db_query", dbBuf, func(dst []byte) int32 { return rawDBQuery(s, a, dst) })
	return out, dbErr(err)
}

// DBQueryInto is DBQuery decoded into out (typically *[]map[string]any or a
// slice of structs with json tags).
func DBQueryInto(out any, sql string, args ...any) error {
	raw, err := DBQuery(sql, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ut db_query: decode: %w", err)
	}
	return nil
}

// ImportDB is a read-only window onto a staged SQLite import file (the
// file behind an import ask's file_handle). It lives until the event ends
// (at most 2 open per event) and has no write path.
type ImportDB struct{ h int32 }

// OpenImportDB opens the staged file as a SQLite database (permission
// "db:own"). A handle the plugin does not own is ErrNotFound; a file that is
// not SQLite is ErrInvalid.
func OpenImportDB(f *ImportFile) (*ImportDB, error) {
	h := rawDBOpenImport(f.h)
	if h < 0 {
		return nil, dbErr(codeErr("db_open_import", h))
	}
	return &ImportDB{h: h}, nil
}

// Query is DBQuery against the import file; writes fail as ErrInvalid.
func (d *ImportDB) Query(sql string, args ...any) ([]byte, error) {
	a, err := encodeDBArgs(args)
	if err != nil {
		return nil, err
	}
	s := []byte(sql)
	out, err := bufCall("db_query_h", dbBuf, func(dst []byte) int32 { return rawDBQueryH(d.h, s, a, dst) })
	return out, dbErr(err)
}

// QueryInto is Query decoded into out.
func (d *ImportDB) QueryInto(out any, sql string, args ...any) error {
	raw, err := d.Query(sql, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ut db_query_h: decode: %w", err)
	}
	return nil
}
