package plugins

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/db/plugindb"
	"github.com/universaltill/universal-till/internal/logging"
)

// db_exec / db_query / db_error / db_open_import / db_query_h (ADR-0121 §3,
// ut-docs#3157). The store is chosen ONLY from hostState.pluginID: the guest
// passes no id and no path, so it cannot name another plugin's file. Every
// call re-checks db:own (denials audited, revocation live) and that the
// installed manifest declares a db block, so no file is ever created for a
// plugin without one.
//
// Return codes: db_exec -> rows affected or <0; db_query -> full result
// length or <0 (buffer ABI); -2 no db:own / no db block, -3 internal or
// deadline, -4 refused statement, bad args or a result over 1 MiB, -5 quota,
// -6 busy. db_error copies the last error text of THIS event.

const (
	permDBOwn = "db:own"
	// dbCallCap bounds one statement; the event deadline may be shorter.
	dbCallCap = 10 * time.Second
	// dbQueryCap bounds one db_query. It is below plugindb's BusyWait (5 s)
	// on purpose: the plugin has ONE connection, and a long report query must
	// be interrupted before a sale-path db_exec that is waiting for it gives
	// up with busy (ut-docs#3157 performance MUST-FIX 3).
	dbQueryCap = 3 * time.Second
	// dbErrCap bounds the stored error text.
	dbErrCap = 512
	// maxDBImportHandles caps import databases open at once per event.
	maxDBImportHandles = 2
)

// dbEvent is the per-event state of the db host functions. It lives inside
// hostState, which handleEvent creates fresh for every event.
type dbEvent struct {
	once     sync.Once
	declared bool
	quota    int64
	keep     int // manifest retention.keep_years: > 0 opens synchronous=FULL
	// used: the event touched the live store, so handleEvent checkpoints.
	used bool
	// lastErr is db_error's text.
	lastErr string
	// retry cache (buffer-ABI, like http_request's): one entry, kept only
	// when its result overflowed the guest's buffer; a write is never re-run.
	cacheKey []byte
	cacheOut []byte
	imports  dbImports
}

// dbCall resolves the caller, checks db:own and the db block. Returns the
// state, or a negative host code.
func dbCall(ctx context.Context) (*hostState, int32) {
	s, ok := stateFrom(ctx)
	if !ok {
		return nil, stateErr(ctx)
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, permDBOwn); err != nil {
		return nil, hostErrDenied
	}
	s.dbev.once.Do(func() {
		m, ok, err := InstalledManifest(ctx, s.db, s.pluginID)
		if err != nil {
			logging.L().Warnf("[wasm:%s] manifest unreadable, db access refused: %v", s.pluginID, err)
		}
		if err != nil || !ok || m.DB == nil {
			return
		}
		s.dbev.declared = true
		s.dbev.quota = int64(m.EffectiveLimits(goruntime.GOOS).StorageMB) << 20
		if m.Retention != nil {
			s.dbev.keep = m.Retention.KeepYears
		}
	})
	if !s.dbev.declared {
		s.dbev.lastErr = "plugin manifest declares no db block"
		return nil, hostErrDenied
	}
	return s, 0
}

// dbCtx bounds one statement by min(event deadline, dbCallCap).
func dbCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return dbCtxCap(ctx, dbCallCap)
}

func dbCtxCap(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem < d {
			d = rem
		}
	}
	if d < 0 {
		d = 0
	}
	return context.WithTimeout(ctx, d)
}

// dbCode maps a plugindb error to a host code and records db_error's text.
func (e *dbEvent) fail(err error) int32 {
	msg := err.Error()
	if len(msg) > dbErrCap {
		msg = msg[:dbErrCap]
	}
	e.lastErr = msg
	switch {
	case errors.Is(err, plugindb.ErrQuota):
		return hostErrQuota
	case errors.Is(err, plugindb.ErrBusy):
		return hostErrBusy
	case errors.Is(err, plugindb.ErrStatementRefused),
		errors.Is(err, plugindb.ErrResultTooLarge):
		return hostErrInvalid
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.Is(err, plugindb.ErrClosed):
		return hostErrInternal
	}
	// A constraint or syntax error is the guest's mistake, not the host's.
	low := strings.ToLower(msg)
	if strings.Contains(low, "constraint") || strings.Contains(low, "syntax error") ||
		strings.Contains(low, "no such table") || strings.Contains(low, "no such column") {
		return hostErrInvalid
	}
	return hostErrInternal
}

// openStore opens the calling plugin's own store (id from the host only).
func (s *hostState) openDBStore(ctx context.Context) (*plugindb.Store, error) {
	return DataManager().Open(ctx, s.pluginID, plugindb.Options{QuotaBytes: s.dbev.quota, NoMigrate: true, KeepYears: s.dbev.keep})
}

// withDBStore runs fn on the caller's store. A store handed out a moment
// before the idle sweep (CloseIdle) or an LRU eviction closed it reports
// ErrClosed from its acquire, i.e. before the statement started, so opening
// again and running fn once more can never repeat a write. Without this a
// sale-path write could fail only because a sweep ran in the same instant.
func (s *hostState) withDBStore(ctx context.Context, fn func(*plugindb.Store) error) error {
	return retryClosedStore(func() (*plugindb.Store, error) { return s.openDBStore(ctx) }, fn)
}

func retryClosedStore(open func() (*plugindb.Store, error), fn func(*plugindb.Store) error) error {
	for attempt := 0; ; attempt++ {
		st, err := open()
		if err != nil {
			return err
		}
		if err = fn(st); attempt == 0 && errors.Is(err, plugindb.ErrClosed) {
			continue
		}
		return err
	}
}

func hostDBExec(ctx context.Context, m api.Module, sqlPtr, sqlLen, argsPtr, argsLen uint32) int64 {
	s, code := dbCall(ctx)
	if code != 0 {
		return int64(code)
	}
	s.dbev.cacheKey, s.dbev.cacheOut = nil, nil
	q, args, code := readDBStatement(m, sqlPtr, sqlLen, argsPtr, argsLen, &s.dbev)
	if code != 0 {
		return int64(code)
	}
	cctx, cancel := dbCtx(ctx)
	defer cancel()
	var n int64
	err := s.withDBStore(cctx, func(st *plugindb.Store) (e error) {
		s.dbev.used = true
		n, e = st.Exec(cctx, q, args...)
		return e
	})
	if err != nil {
		return int64(s.dbev.fail(err))
	}
	return n
}

func hostDBQuery(ctx context.Context, m api.Module, sqlPtr, sqlLen, argsPtr, argsLen, dstPtr, dstCap uint32) int32 {
	s, code := dbCall(ctx)
	if code != 0 {
		return code
	}
	q, args, code := readDBStatement(m, sqlPtr, sqlLen, argsPtr, argsLen, &s.dbev)
	if code != 0 {
		return code
	}
	key := dbCacheKey(-1, sqlPtr, sqlLen, argsPtr, argsLen, m)
	if out, hit := s.dbev.serveCache(key, dstCap); hit {
		return writeGuest(m, dstPtr, dstCap, out)
	}
	cctx, cancel := dbCtxCap(ctx, dbQueryCap)
	defer cancel()
	var out []byte
	err := s.withDBStore(cctx, func(st *plugindb.Store) (e error) {
		s.dbev.used = true
		out, e = st.Query(cctx, q, args...)
		return e
	})
	if err != nil {
		return s.dbev.fail(err)
	}
	s.dbev.keepCache(key, out, dstCap)
	return writeGuest(m, dstPtr, dstCap, out)
}

// hostDBError copies the last db error text of this event.
func hostDBError(ctx context.Context, m api.Module, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return stateErr(ctx)
	}
	return writeGuest(m, dstPtr, dstCap, []byte(s.dbev.lastErr))
}

func readDBStatement(m api.Module, sqlPtr, sqlLen, argsPtr, argsLen uint32, e *dbEvent) (string, []any, int32) {
	raw, ok := readGuest(m, sqlPtr, sqlLen)
	if !ok || len(raw) == 0 {
		e.lastErr = "empty statement"
		return "", nil, hostErrInvalid
	}
	q := string(raw) // copy: the guest may overwrite its memory
	var args []any
	if argsLen > 0 {
		a, ok := readGuest(m, argsPtr, argsLen)
		if !ok {
			return "", nil, hostErrInvalid
		}
		var err error
		if args, err = plugindb.DecodeArgs(a); err != nil {
			return "", nil, e.fail(err)
		}
	}
	return q, args, 0
}

func dbCacheKey(h int32, sqlPtr, sqlLen, argsPtr, argsLen uint32, m api.Module) []byte {
	q, _ := readGuest(m, sqlPtr, sqlLen)
	a, _ := readGuest(m, argsPtr, argsLen)
	k := make([]byte, 0, len(q)+len(a)+6)
	k = append(k, byte(h), byte(h>>8), byte(h>>16), byte(h>>24))
	k = append(k, q...)
	k = append(k, 0)
	return append(k, a...)
}

// serveCache answers an identical retry of an overflowed read.
func (e *dbEvent) serveCache(key []byte, dstCap uint32) ([]byte, bool) {
	if e.cacheOut == nil || string(e.cacheKey) != string(key) {
		e.cacheKey, e.cacheOut = nil, nil // any other call clears it
		return nil, false
	}
	out := e.cacheOut
	if dstCap >= uint32(len(out)) {
		e.cacheKey, e.cacheOut = nil, nil
	}
	return out, true
}

func (e *dbEvent) keepCache(key, out []byte, dstCap uint32) {
	e.cacheKey, e.cacheOut = nil, nil
	if uint32(len(out)) > dstCap {
		e.cacheKey, e.cacheOut = key, out
	}
}

// finish runs when the event ends: import databases are closed, and a store
// the event wrote through is checkpointed (PASSIVE, never blocks).
func (e *dbEvent) finish(pluginID string) {
	e.imports.closeAll()
	if !e.used {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if st, err := DataManager().Open(ctx, pluginID, plugindb.Options{QuotaBytes: e.quota, NoMigrate: true, KeepYears: e.keep}); err == nil {
		_ = st.Checkpoint(ctx)
	}
}
