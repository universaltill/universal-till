package plugins

// Plugin blob store for wasm plugins (ADR-0121 §3 and §6 blob:own,
// ut-docs#3870):
//
//	blob_put_open(namePtr, nameLen) -> handle | err
//	blob_write(h, bufPtr, bufLen) -> bytes accepted | err
//	blob_commit(h) -> 0 | err
//	blob_get_open(namePtr, nameLen) -> handle | err
//	blob_read(h, dstPtr, dstCap) -> n (0 = end of blob) | err
//	blob_delete(namePtr, nameLen) -> 0 | err
//	blob_list(dstPtr, dstCap) -> full length of [{"name":…,"size":N},…] | err
//
// A plugin's blobs live in paths.Data("plugin-data", <id>, "blobs"), where
// <id> is the calling plugin's own (host-side) id, never a guest value — one
// plugin cannot reach another's blobs. Names match [a-z0-9._-]{1,128} and
// are a single safe file name (validBlobName): no paths, no "." or "..", no
// Windows-aliased names. Anything else is hostErrInvalid.
//
// A put writes to a temp file (blobTempPrefix, which no valid name can
// start with) and blob_commit renames it into place atomically; a put never
// committed — the guest gave up, the event ended, trapped or timed out —
// leaves nothing behind, and an overwrite leaves the old blob intact until
// the commit. A get handle is released when blob_read reaches the end
// (returns 0); reading it again is hostErrNotFound.
//
// Quota: the plugin's committed blobs may total at most limits.storage_mb
// (Manifest.EffectiveLimits, platform-clamped; the default when the
// manifest cannot be read). Every store has one process-wide blobStore
// (lock + byte counts) shared by all of the plugin's concurrent events.
// blob_write refuses a write that would take the committed blobs plus every
// in-flight put past the quota, so open puts cannot fill the disk beyond it;
// blob_commit re-checks under the same lock against the store as it is at
// commit, so concurrent commits cannot both land past it. blob_list sizes
// count committed blobs only. Over it is hostErrQuota and the put is
// discarded.
// Plugin-owned SQLite (build card 5) does not exist yet; when it lands its
// size comes off the same quota (ADR-0121 §6).
//
// Handles live on the per-event hostState, never process-wide: at most
// maxBlobHandles open per event (one more is hostErrBusy), and handleEvent
// closes them all — discarding uncommitted puts — when the event returns.
//
// The blob:own permission is checked (CheckPermission: audited, revocable
// live) on every call.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

const (
	// permBlobOwn gates every blob_* function.
	permBlobOwn = "blob:own"
	// maxBlobHandles caps a plugin's open blob handles per event.
	maxBlobHandles = 8
	// blobReadCap bounds one blob_read regardless of dstCap.
	blobReadCap = 256 << 10
	// blobTempPrefix names in-flight puts. '~' is outside the name
	// alphabet, so a temp file can never collide with or be read as a blob.
	blobTempPrefix = "~put-"
	// blobTempMaxAge: a temp file older than this is left over from a crash
	// (no event lives this long) and is removed when the store is scanned.
	blobTempMaxAge = time.Hour
)

var blobNamePattern = regexp.MustCompile(`^[a-z0-9._-]{1,128}$`)

// validBlobName reports whether name is a legal blob name: the ADR's
// alphabet and length, and a single file name on every platform ("." and
// ".." and trailing dots and Windows device names are refused —
// windowsUnsafe covers all of them).
func validBlobName(name string) bool {
	return blobNamePattern.MatchString(name) && !windowsUnsafe(name)
}

// blobHandle is one open put or get.
type blobHandle struct {
	f    *os.File
	put  bool
	name string     // put: the final name
	tmp  string     // put: the temp file's path
	st   *blobStore // put: the store whose in-flight count holds n
	old  int64      // put: size of the blob this put replaces, at open
	n    int64      // put: bytes written
}

// release closes the file; an uncommitted put's temp file is removed, and a
// put's bytes leave the store's in-flight count.
func (b *blobHandle) release() {
	if b.f != nil {
		_ = b.f.Close()
		b.f = nil
	}
	if b.put && b.tmp != "" {
		_ = os.Remove(b.tmp)
		b.tmp = ""
	}
	if b.st != nil {
		b.st.mu.Lock()
		b.st.inflight -= b.n
		b.st.mu.Unlock()
		b.st = nil
	}
}

// blobStore is the process-wide accounting for one blob dir: every event of
// the plugin shares it, so quota checks see each other's puts and commits.
type blobStore struct {
	mu        sync.Mutex
	committed int64 // committed bytes, rescanned at each put_open and commit
	inflight  int64 // bytes written by open, uncommitted puts
}

// blobStores maps a blob dir to its *blobStore.
var blobStores sync.Map

func storeFor(dir string) *blobStore {
	st, _ := blobStores.LoadOrStore(dir, &blobStore{})
	return st.(*blobStore)
}

// blobHandles is the per-event handle registry (on hostState). Handles count
// from 0 within the event and are never reused within it.
type blobHandles struct {
	mu   sync.Mutex
	next int32
	open map[int32]*blobHandle
}

func (r *blobHandles) add(b *blobHandle) (int32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.open) >= maxBlobHandles {
		return 0, false
	}
	if r.open == nil {
		r.open = map[int32]*blobHandle{}
	}
	h := r.next
	r.next++
	r.open[h] = b
	return h, true
}

func (r *blobHandles) get(h int32) (*blobHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.open[h]
	return b, ok
}

// take removes a handle from the registry without releasing it.
func (r *blobHandles) take(h int32) (*blobHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.open[h]
	delete(r.open, h)
	return b, ok
}

func (r *blobHandles) close(h int32) {
	if b, ok := r.take(h); ok {
		b.release()
	}
}

// closeAll releases every handle — handleEvent defers it so no handle, and
// no uncommitted put, outlives its event.
func (r *blobHandles) closeAll() {
	r.mu.Lock()
	open := r.open
	r.open = nil
	r.mu.Unlock()
	for _, b := range open {
		b.release()
	}
}

// blobDir is the calling plugin's blob root.
func (s *hostState) blobDir() string {
	return paths.Data("plugin-data", s.pluginID, "blobs")
}

// blobPath joins a validated name onto dir and confirms the result is still
// directly inside it (defence in depth behind validBlobName).
func blobPath(dir, name string) (string, bool) {
	p := filepath.Clean(filepath.Join(dir, name))
	if filepath.Dir(p) != filepath.Clean(dir) {
		return "", false
	}
	return p, true
}

// blobQuota is this event's blob byte quota, read lazily (once per event)
// from the plugin's installed manifest.
func (s *hostState) blobQuota(ctx context.Context) int64 {
	s.blobQuotaOnce.Do(func() {
		m, ok, err := InstalledManifest(ctx, s.db, s.pluginID)
		if err != nil {
			logging.L().Warnf("[wasm:%s] manifest unreadable, blob:own uses the default storage quota: %v", s.pluginID, err)
		}
		if err != nil || !ok {
			m = &Manifest{}
		}
		s.blobQuotaBytes = int64(m.EffectiveLimits(goruntime.GOOS).StorageMB) << 20
	})
	return s.blobQuotaBytes
}

type blobEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// scanBlobs lists the committed blobs in dir, sorted by name, and removes
// temp files left over from a crash. A missing dir is an empty store.
func scanBlobs(dir string) ([]blobEntry, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []blobEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []blobEntry{}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed meanwhile
		}
		if strings.HasPrefix(e.Name(), blobTempPrefix) {
			if time.Since(info.ModTime()) > blobTempMaxAge {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
			continue
		}
		if !validBlobName(e.Name()) {
			continue
		}
		out = append(out, blobEntry{Name: e.Name(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// blobCall resolves the caller and checks blob:own for one call.
func blobCall(ctx context.Context) (*hostState, int32) {
	s, ok := stateFrom(ctx)
	if !ok {
		return nil, hostErrInternal
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, permBlobOwn); err != nil {
		return nil, hostErrDenied
	}
	return s, 0
}

// blobName reads and validates a name argument; ok=false → hostErrInvalid.
func blobName(m api.Module, ptr, length uint32) (string, bool) {
	raw, ok := readGuest(m, ptr, length)
	if !ok {
		return "", false
	}
	name := string(raw)
	return name, validBlobName(name)
}

func hostBlobPutOpen(ctx context.Context, m api.Module, namePtr, nameLen uint32) int32 {
	s, code := blobCall(ctx)
	if code != 0 {
		return code
	}
	name, ok := blobName(m, namePtr, nameLen)
	if !ok {
		return hostErrInvalid
	}
	dir := s.blobDir()
	if _, ok := blobPath(dir, name); !ok {
		return hostErrInvalid
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logging.L().Warnf("[wasm:%s] blob dir: %v", s.pluginID, err)
		return hostErrInternal
	}
	st := storeFor(dir)
	st.mu.Lock()
	ents, err := scanBlobs(dir)
	var committed, old int64
	for _, e := range ents {
		committed += e.Size
		if e.Name == name {
			old = e.Size
		}
	}
	if err == nil {
		st.committed = committed
	}
	st.mu.Unlock()
	if err != nil {
		logging.L().Warnf("[wasm:%s] blob scan: %v", s.pluginID, err)
		return hostErrInternal
	}
	f, err := os.CreateTemp(dir, blobTempPrefix+"*")
	if err != nil {
		logging.L().Warnf("[wasm:%s] blob temp file: %v", s.pluginID, err)
		return hostErrInternal
	}
	b := &blobHandle{f: f, put: true, name: name, tmp: f.Name(), st: st, old: old}
	h, ok := s.blobs.add(b)
	if !ok {
		b.release()
		return hostErrBusy
	}
	return h
}

func hostBlobWrite(ctx context.Context, m api.Module, h int32, bufPtr, bufLen uint32) int32 {
	s, code := blobCall(ctx)
	if code != 0 {
		return code
	}
	b, ok := s.blobs.get(h)
	if !ok || !b.put {
		return hostErrNotFound
	}
	buf, ok := readGuest(m, bufPtr, bufLen)
	if !ok {
		return hostErrInvalid
	}
	quota := s.blobQuota(ctx)
	add := int64(len(buf))
	// Reserve the bytes before writing them: committed blobs (less the one
	// this put replaces) plus every open put, of every event, stay within
	// the quota.
	b.st.mu.Lock()
	if b.st.committed-b.old+b.st.inflight+add > quota {
		b.st.mu.Unlock()
		s.blobs.close(h)
		return hostErrQuota
	}
	b.st.inflight += add
	b.n += add
	b.st.mu.Unlock()
	if _, err := b.f.Write(buf); err != nil {
		logging.L().Warnf("[wasm:%s] blob write: %v", s.pluginID, err)
		s.blobs.close(h)
		return hostErrInternal
	}
	return int32(len(buf))
}

func hostBlobCommit(ctx context.Context, _ api.Module, h int32) int32 {
	s, code := blobCall(ctx)
	if code != 0 {
		return code
	}
	b, ok := s.blobs.get(h)
	if !ok || !b.put {
		return hostErrNotFound
	}
	// Whatever happens next, the handle is spent.
	s.blobs.take(h)
	defer b.release()
	dir := s.blobDir()
	dst, ok := blobPath(dir, b.name)
	if !ok {
		return hostErrInvalid
	}
	quota := s.blobQuota(ctx)
	// The check and the rename happen under the store lock, so concurrent
	// commits of one plugin cannot both land past the quota.
	b.st.mu.Lock()
	defer b.st.mu.Unlock()
	ents, err := scanBlobs(dir)
	if err != nil {
		logging.L().Warnf("[wasm:%s] blob scan: %v", s.pluginID, err)
		return hostErrInternal
	}
	var used, old int64
	for _, e := range ents {
		if e.Name == b.name {
			old = e.Size
			continue
		}
		used += e.Size
	}
	b.st.committed = used + old
	if used+b.n > quota {
		return hostErrQuota
	}
	if err := b.f.Sync(); err != nil {
		logging.L().Warnf("[wasm:%s] blob sync: %v", s.pluginID, err)
		return hostErrInternal
	}
	if err := b.f.Close(); err != nil {
		b.f = nil
		logging.L().Warnf("[wasm:%s] blob close: %v", s.pluginID, err)
		return hostErrInternal
	}
	b.f = nil
	if err := os.Rename(b.tmp, dst); err != nil {
		logging.L().Warnf("[wasm:%s] blob commit: %v", s.pluginID, err)
		return hostErrInternal
	}
	b.tmp = "" // committed: release must not remove it
	b.st.committed = used + b.n
	// release (deferred, after the unlock) drops b.n from inflight.
	return 0
}

func hostBlobGetOpen(ctx context.Context, m api.Module, namePtr, nameLen uint32) int32 {
	s, code := blobCall(ctx)
	if code != 0 {
		return code
	}
	name, ok := blobName(m, namePtr, nameLen)
	if !ok {
		return hostErrInvalid
	}
	p, ok := blobPath(s.blobDir(), name)
	if !ok {
		return hostErrInvalid
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return hostErrNotFound
	}
	if err != nil {
		logging.L().Warnf("[wasm:%s] blob open: %v", s.pluginID, err)
		return hostErrInternal
	}
	h, ok := s.blobs.add(&blobHandle{f: f, name: name})
	if !ok {
		_ = f.Close()
		return hostErrBusy
	}
	return h
}

func hostBlobRead(ctx context.Context, m api.Module, h int32, dstPtr, dstCap uint32) int32 {
	s, code := blobCall(ctx)
	if code != 0 {
		return code
	}
	b, ok := s.blobs.get(h)
	if !ok || b.put {
		return hostErrNotFound
	}
	n := dstCap
	if n > blobReadCap {
		n = blobReadCap
	}
	if n == 0 {
		return hostErrInvalid
	}
	// Check the destination BEFORE reading, so a bad buffer loses no bytes.
	if _, ok := m.Memory().Read(dstPtr, n); !ok {
		return hostErrInvalid
	}
	buf := make([]byte, n)
	got, err := b.f.Read(buf)
	if got == 0 && errors.Is(err, io.EOF) {
		s.blobs.close(h)
		return 0
	}
	if err != nil && !errors.Is(err, io.EOF) {
		logging.L().Warnf("[wasm:%s] blob read: %v", s.pluginID, err)
		s.blobs.close(h)
		return hostErrInternal
	}
	if !m.Memory().Write(dstPtr, buf[:got]) {
		return hostErrInvalid
	}
	return int32(got)
}

func hostBlobDelete(ctx context.Context, m api.Module, namePtr, nameLen uint32) int32 {
	s, code := blobCall(ctx)
	if code != 0 {
		return code
	}
	name, ok := blobName(m, namePtr, nameLen)
	if !ok {
		return hostErrInvalid
	}
	p, ok := blobPath(s.blobDir(), name)
	if !ok {
		return hostErrInvalid
	}
	st := storeFor(s.blobDir())
	st.mu.Lock()
	var size int64
	if info, serr := os.Stat(p); serr == nil {
		size = info.Size()
	}
	err := os.Remove(p)
	if err == nil {
		st.committed -= size
	}
	st.mu.Unlock()
	if errors.Is(err, fs.ErrNotExist) {
		return hostErrNotFound
	}
	if err != nil {
		logging.L().Warnf("[wasm:%s] blob delete: %v", s.pluginID, err)
		return hostErrInternal
	}
	return 0
}

func hostBlobList(ctx context.Context, m api.Module, dstPtr, dstCap uint32) int32 {
	s, code := blobCall(ctx)
	if code != 0 {
		return code
	}
	ents, err := scanBlobs(s.blobDir())
	if err != nil {
		logging.L().Warnf("[wasm:%s] blob list: %v", s.pluginID, err)
		return hostErrInternal
	}
	raw, err := json.Marshal(ents)
	if err != nil {
		return hostErrInternal
	}
	return writeGuest(m, dstPtr, dstCap, raw)
}
