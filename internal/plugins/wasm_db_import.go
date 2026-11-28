package plugins

import (
	"context"
	"sync"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/db/plugindb"
)

// db_open_import / db_query_h: a read-only window onto a staged SQLite file
// the plugin received through the import handle transport. The path comes
// ONLY from importFiles.Path(pluginID, handle): the registry is keyed by the
// caller's id, so another plugin's staged file cannot be addressed. There is
// no write path (no db_exec variant). Handles are per event.

// dbImports are one event's open import databases.
type dbImports struct {
	mu   sync.Mutex
	open map[int32]*plugindb.ImportDB
	next int32
}

func (r *dbImports) add(d *plugindb.ImportDB) (int32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.open) >= maxDBImportHandles {
		return 0, false
	}
	if r.open == nil {
		r.open = map[int32]*plugindb.ImportDB{}
	}
	h := r.next
	r.next++
	r.open[h] = d
	return h, true
}

func (r *dbImports) get(h int32) (*plugindb.ImportDB, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.open[h]
	return d, ok
}

func (r *dbImports) closeAll() {
	r.mu.Lock()
	open := r.open
	r.open = nil
	r.mu.Unlock()
	for _, d := range open {
		_ = d.Close()
	}
}

func hostDBOpenImport(ctx context.Context, importHandle int32) int32 {
	s, code := dbCall(ctx)
	if code != 0 {
		return code
	}
	path, ok := importFiles.Path(s.pluginID, importHandle)
	if !ok {
		s.dbev.lastErr = "unknown import handle"
		return hostErrNotFound
	}
	cctx, cancel := dbCtx(ctx)
	defer cancel()
	d, err := plugindb.OpenImport(cctx, path)
	if err != nil {
		return s.dbev.fail(err)
	}
	h, ok := s.dbev.imports.add(d)
	if !ok {
		_ = d.Close()
		s.dbev.lastErr = "too many open import databases"
		return hostErrBusy
	}
	return h
}

func hostDBQueryH(ctx context.Context, m api.Module, dbh int32, sqlPtr, sqlLen, argsPtr, argsLen, dstPtr, dstCap uint32) int32 {
	s, code := dbCall(ctx)
	if code != 0 {
		return code
	}
	d, ok := s.dbev.imports.get(dbh)
	if !ok {
		s.dbev.lastErr = "unknown database handle"
		return hostErrNotFound
	}
	q, args, code := readDBStatement(m, sqlPtr, sqlLen, argsPtr, argsLen, &s.dbev)
	if code != 0 {
		return code
	}
	key := dbCacheKey(dbh, sqlPtr, sqlLen, argsPtr, argsLen, m)
	if out, hit := s.dbev.serveCache(key, dstCap); hit {
		return writeGuest(m, dstPtr, dstCap, out)
	}
	cctx, cancel := dbCtx(ctx)
	defer cancel()
	out, err := d.Query(cctx, q, args...)
	if err != nil {
		return s.dbev.fail(err)
	}
	s.dbev.keepCache(key, out, dstCap)
	return writeGuest(m, dstPtr, dstCap, out)
}
