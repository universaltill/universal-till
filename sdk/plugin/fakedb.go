//go:build !wasip1

package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// The fake's db_* host functions. The till runs real SQLite; the fake keeps
// its refusals (plugindb.checkStatement, DecodeArgs, the result cap) and
// ABI, and hands the statement to FakeHost.DBExec / DBQuery /
// ImportDBQuery.

const (
	dbArgsMax     = 64
	dbArgsJSONMax = 256 << 10
	dbErrMax      = 512
	dbImportsMax  = 2
)

var dbRefusedFirst = map[string]bool{
	"ATTACH": true, "DETACH": true, "VACUUM": true, "PRAGMA": true,
	"BEGIN": true, "COMMIT": true, "END": true, "ROLLBACK": true, "SAVEPOINT": true, "RELEASE": true,
}

var dbRefusedFragments = []string{"_ut_migrations", "sqlite_dbpage", "load_extension", "readfile", "writefile", "fts3_tokenizer", "sqlite_dbdata", "zipfile("}

// dbCheckStatement reports why q is refused ("" = fine): empty, a refused
// first keyword, a second statement, or a refused fragment.
func dbCheckStatement(q string) string {
	low := strings.ToLower(q)
	for _, f := range dbRefusedFragments {
		if strings.Contains(low, f) {
			return "statement refused"
		}
	}
	i, first, semi := 0, "", false
	for i < len(q) {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case strings.HasPrefix(q[i:], "--"):
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				i = len(q)
			} else {
				i += j + 1
			}
		case strings.HasPrefix(q[i:], "/*"):
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				i = len(q)
			} else {
				i += 2 + j + 2
			}
		case c == ';':
			semi = true
			i++
		default:
			if semi {
				return "statement refused"
			}
			if first == "" {
				j := i
				for j < len(q) && (q[j] >= 'A' && q[j] <= 'Z' || q[j] >= 'a' && q[j] <= 'z') {
					j++
				}
				if j == i || dbRefusedFirst[strings.ToUpper(q[i:j])] {
					return "statement refused"
				}
				first = q[i:j]
				i = j
				continue
			}
			if c == '\'' || c == '"' || c == '`' {
				j := i + 1
				for j < len(q) {
					if q[j] == c {
						if j+1 < len(q) && q[j+1] == c {
							j += 2
							continue
						}
						break
					}
					j++
				}
				i = j + 1
			} else if c == '[' {
				j := strings.IndexByte(q[i:], ']')
				if j < 0 {
					return ""
				}
				i += j + 1
			} else {
				i++
			}
		}
	}
	if first == "" {
		return "empty statement"
	}
	return ""
}

// dbDecodeArgs mirrors plugindb.DecodeArgs: nil, bool, string, int64,
// float64, {"b64":..} as []byte; anything else is refused.
func dbDecodeArgs(raw []byte) ([]any, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, ""
	}
	if len(raw) > dbArgsJSONMax {
		return nil, "arguments over 262144 bytes"
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var items []json.RawMessage
	if err := dec.Decode(&items); err != nil {
		return nil, "arguments are not a JSON array"
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, "trailing data after arguments"
	}
	if len(items) > dbArgsMax {
		return nil, "more than 64 arguments"
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		v, ok := dbDecodeArg(bytes.TrimSpace(it))
		if !ok {
			return nil, "unsupported argument type"
		}
		out = append(out, v)
	}
	return out, ""
}

func dbDecodeArg(it []byte) (any, bool) {
	d := json.NewDecoder(bytes.NewReader(it))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return nil, false
	}
	switch x := v.(type) {
	case nil, bool, string:
		return x, true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		f, err := x.Float64()
		return f, err == nil
	case map[string]any:
		s, ok := x["b64"].(string)
		if !ok || len(x) != 1 {
			return nil, false
		}
		b, err := base64Decode(s)
		return b, err == nil
	}
	return nil, false
}

// dbFail records db_error's text and returns the code.
func (h *FakeHost) dbFail(code int32, msg string) int32 {
	if len(msg) > dbErrMax {
		msg = msg[:dbErrMax]
	}
	h.dbErr = msg
	return code
}

// dbPrepare is the shared front half of db_exec / db_query / db_query_h.
func (h *FakeHost) dbPrepare(sql, args []byte) (string, []any, int32) {
	if len(sql) == 0 {
		return "", nil, h.dbFail(ErrInvalid.Code, "empty statement")
	}
	q := string(sql)
	if msg := dbCheckStatement(q); msg != "" {
		return "", nil, h.dbFail(ErrInvalid.Code, msg)
	}
	a, msg := dbDecodeArgs(args)
	if msg != "" {
		return "", nil, h.dbFail(ErrInvalid.Code, msg)
	}
	return q, a, 0
}

// dbResult maps a hook's answer to the buffer ABI. The one-entry retry cache
// (kept only when the result did not fit dst, served on the next identical
// call) is why DBQuery's retry never re-runs an INSERT ... RETURNING.
func (h *FakeHost) dbResult(key string, out []byte, err error, dst []byte) int32 {
	if err != nil {
		return h.dbFail(hookCode(err), err.Error())
	}
	if len(out) > dbMaxResult {
		return h.dbFail(ErrInvalid.Code, "result too large")
	}
	if len(out) > len(dst) {
		h.dbCache.key, h.dbCache.out = key, bytes.Clone(out)
	}
	return fill(dst, out)
}

func (h *FakeHost) dbServe(key string, dst []byte) (int32, bool) {
	if h.dbCache.out != nil && h.dbCache.key == key {
		out := h.dbCache.out
		h.dbCache.key, h.dbCache.out = "", nil
		return fill(dst, out), true
	}
	return 0, false
}

func dbKey(h int32, sql, args []byte) string {
	return string(rune(h+1)) + "\x00" + string(sql) + "\x00" + string(args)
}

func rawDBExec(sql, args []byte) int64 {
	h, denied := enter("db_exec")
	defer h.mu.Unlock()
	h.dbCache.key, h.dbCache.out = "", nil
	if denied || h.DBExec == nil {
		return int64(h.dbFail(ErrDenied.Code, "db:own not granted"))
	}
	q, a, code := h.dbPrepare(sql, args)
	if code != 0 {
		return int64(code)
	}
	var n int64
	var err error
	h.unlocked(func() { n, err = h.DBExec(q, a) })
	if err != nil {
		return int64(h.dbFail(hookCode(err), err.Error()))
	}
	return n
}

func rawDBQuery(sql, args, dst []byte) int32 {
	h, denied := enter("db_query")
	defer h.mu.Unlock()
	if denied || h.DBQuery == nil {
		return h.dbFail(ErrDenied.Code, "db:own not granted")
	}
	q, a, code := h.dbPrepare(sql, args)
	if code != 0 {
		return code
	}
	key := dbKey(-1, sql, args)
	if n, ok := h.dbServe(key, dst); ok {
		return n
	}
	h.dbCache.key, h.dbCache.out = "", nil
	var out []byte
	var err error
	h.unlocked(func() { out, err = h.DBQuery(q, a) })
	return h.dbResult(key, out, err, dst)
}

func rawDBError(dst []byte) int32 {
	h, _ := enter("db_error")
	defer h.mu.Unlock()
	return fill(dst, []byte(h.dbErr))
}

func rawDBOpenImport(importHandle int32) int32 {
	h, denied := enter("db_open_import")
	defer h.mu.Unlock()
	if denied || h.ImportDBQuery == nil {
		return h.dbFail(ErrDenied.Code, "db:own not granted")
	}
	if _, ok := h.ImportFiles[importHandle]; !ok {
		return h.dbFail(ErrNotFound.Code, "unknown import handle")
	}
	if len(h.dbImports) >= dbImportsMax {
		return h.dbFail(ErrBusy.Code, "too many open import databases")
	}
	if h.dbImports == nil {
		h.dbImports = map[int32]int32{}
	}
	id := h.handle()
	h.dbImports[id] = importHandle
	return id
}

func rawDBQueryH(dbh int32, sql, args, dst []byte) int32 {
	h, denied := enter("db_query_h")
	defer h.mu.Unlock()
	if denied || h.ImportDBQuery == nil {
		return h.dbFail(ErrDenied.Code, "db:own not granted")
	}
	src, ok := h.dbImports[dbh]
	if !ok {
		return h.dbFail(ErrNotFound.Code, "unknown database handle")
	}
	file, ok := h.ImportFiles[src]
	if !ok {
		return h.dbFail(ErrNotFound.Code, "unknown database handle")
	}
	q, a, code := h.dbPrepare(sql, args)
	if code != 0 {
		return code
	}
	key := dbKey(dbh, sql, args)
	if n, ok := h.dbServe(key, dst); ok {
		return n
	}
	h.dbCache.key, h.dbCache.out = "", nil
	var out []byte
	var err error
	h.unlocked(func() { out, err = h.ImportDBQuery(file, q, a) })
	return h.dbResult(key, out, err, dst)
}
